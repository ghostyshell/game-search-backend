// Package mongo is the MongoDB implementation of pkg/storage.Database.
package mongo

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	"game-search-backend/internal/models"
	"game-search-backend/pkg/storage"
)

var _ storage.Database = (*Client)(nil)

// Client is a MongoDB-backed storage client implementing storage.Database.
type Client struct {
	client *mongo.Client
	db     *mongo.Database
	dbName string
	// isConnected / lastCheck are read by Stats from concurrent HTTP handlers
	// and written by HealthCheck from background jobs; atomic to avoid the data
	// race `go test -race` flags.
	isConnected atomic.Bool
	lastCheck    atomic.Pointer[time.Time]
}

// NewClient connects to MongoDB.
func NewClient(uri, dbName string) (*Client, error) {
	if uri == "" {
		return nil, fmt.Errorf("mongodb URI is required")
	}
	if dbName == "" {
		dbName = "game_search"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := mongo.Connect(ctx, options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(8*time.Second).
		SetMaxPoolSize(10).
		// Retire idle pool connections before a WAN/LB path reaps them.
		SetMaxConnIdleTime(30*time.Second))
	if err != nil {
		return nil, err
	}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}

	c := &Client{
		client: client,
		db:     client.Database(dbName),
		dbName: dbName,
	}
	c.isConnected.Store(true)
	now := time.Now().UTC()
	c.lastCheck.Store(&now)
	return c, nil
}

// mongoOpTimeout caps a single bulk-fill Mongo operation so a connection that
// goes silent mid-read cannot block forever. Applied only to bulk-fill-critical
// ops (UpsertGameLinks). Set generously: a 4000+ doc bulk write over WAN to the
// shared Sliplane Mongo legitimately takes tens of seconds, and an too-tight
// cap silently drops the whole batch (the driver returns a deadline error but
// the batch may already have streamed).
const mongoOpTimeout = 120 * time.Second

func opTimeoutCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, mongoOpTimeout)
}

func (c *Client) coll(name string) *mongo.Collection { return c.db.Collection(name) }

// Collections used by the backend.
const (
	CollGames       = "games"
	CollGameLinks   = "game_links"
	CollDebridHosts = "debrid_hosts"
)

// Migrate ensures indexes exist.
func (c *Client) Migrate(ctx context.Context) error {
	mctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// One-time backfill: copy updated_at into source_published_at for existing
	// games that pre-date the field, so the new browse sort is populated without
	// re-scraping all ~58k games. Aggregation-pipeline update (a plain $set cannot
	// reference another field). Idempotent: $exists:false skips on later runs, so a
	// transient WAN failure here self-heals on the next Migrate (next deploy/start).
	// dlpsgame games get corrected to the true post date on their next sync; until
	// then they sort by updated_at (the prior behavior, preserved).
	if _, err := c.coll(CollGames).UpdateMany(mctx,
		bson.M{"source_published_at": bson.M{"$exists": false}},
		[]bson.M{{"$set": bson.M{"source_published_at": "$updated_at"}}},
	); err != nil {
		log.Printf("migrate: source_published_at backfill failed (will retry next Migrate): %v", err)
	}

	// Drop the superseded {platform:1, updated_at:-1} browse index; the new
	// {platform:1, source_published_at:-1} index replaces it. CreateOne below is
	// idempotent but does not drop orphans, so without this the old index would
	// persist forever (extra write amplification on every upsert). Best-effort:
	// ignore "not found" if a fresh DB never had it.
	gc := c.coll(CollGames).Indexes()
	if _, err := gc.DropOne(mctx, "platform_1_updated_at_-1"); err != nil {
		log.Printf("migrate: drop old platform_1_updated_at_-1 index (ok if absent): %v", err)
	}

	indexes := []struct {
		coll string
		keys bson.D
		opts *options.IndexOptions
	}{
		// games: slug is the natural key (unique). platform+source_published_at
		// backs browse (newest source post first); platform+title for search prefix
		// scans. source_published_at is the unified sort key: post publish date for
		// dlpsgame, scrape wallclock for sources without a publish date.
		{CollGames, bson.D{{Key: "slug", Value: 1}}, options.Index().SetUnique(true)},
		{CollGames, bson.D{{Key: "platform", Value: 1}, {Key: "source_published_at", Value: -1}}, nil},
		{CollGames, bson.D{{Key: "platform", Value: 1}, {Key: "title", Value: 1}}, nil},
		// game_links: _id is the stable per-source link ID (unique). game_slug backs
		// the detail page query; source backs per-source counts.
		{CollGameLinks, bson.D{{Key: "game_slug", Value: 1}}, nil},
		{CollGameLinks, bson.D{{Key: "source", Value: 1}, {Key: "updated_at", Value: -1}}, nil},
		// debrid_hosts: _id is the host domain.
	}

	for _, idx := range indexes {
		model := mongo.IndexModel{Keys: idx.keys}
		if idx.opts != nil {
			model.Options = idx.opts
		}
		_, _ = c.coll(idx.coll).Indexes().CreateOne(mctx, model)
	}
	return nil
}

// Close disconnects MongoDB.
func (c *Client) Close(ctx context.Context) error {
	c.isConnected.Store(false)
	return c.client.Disconnect(ctx)
}

// HealthCheck pings MongoDB.
func (c *Client) HealthCheck(ctx context.Context) error {
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	err := c.client.Ping(pctx, readpref.Primary())
	now := time.Now().UTC()
	c.lastCheck.Store(&now)
	if err != nil {
		c.isConnected.Store(false)
		return err
	}
	c.isConnected.Store(true)
	return nil
}

func nowSec() int64 { return time.Now().Unix() }

// Stats returns collection counts for /health and monitoring.
func (c *Client) Stats(ctx context.Context) (map[string]int64, error) {
	out := make(map[string]int64)
	for _, name := range []string{CollGames, CollGameLinks, CollDebridHosts} {
		n, err := c.coll(name).EstimatedDocumentCount(ctx)
		if err != nil {
			out[name] = -1
			continue
		}
		out[name] = n
	}
	out["mongo_connected"] = 0
	if c.isConnected.Load() {
		out["mongo_connected"] = 1
	}
	return out, nil
}

// compile-time interface satisfaction for the helpers used by games/game_links.
var _ = models.Game{}