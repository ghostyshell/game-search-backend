package mongo

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"game-search-backend/internal/models"
)

// UpsertGameLinks bulk-upserts a batch of GameLinks by their stable _id.
// $set refreshes listing fields; $setOnInsert seeds created_at so re-ingest
// preserves the first-seen timestamp. Chunked at upsertChunk so a large
// platform ingest (r-roms PS2 can be tens of thousands of files) stays under
// the 16MB wire limit and each WAN bulk write completes within the op timeout.
func (c *Client) UpsertGameLinks(ctx context.Context, links []models.GameLink) error {
	if len(links) == 0 {
		return nil
	}
	for start := 0; start < len(links); start += upsertChunk {
		end := start + upsertChunk
		if end > len(links) {
			end = len(links)
		}
		if err := c.upsertGameLinksChunk(ctx, links[start:end]); err != nil {
			return err
		}
	}
	return nil
}

const upsertChunk = 2000

func (c *Client) upsertGameLinksChunk(ctx context.Context, links []models.GameLink) error {
	octx, cancel := opTimeoutCtx(ctx)
	defer cancel()

	writes := make([]mongo.WriteModel, 0, len(links))
	now := nowSec()
	for _, l := range links {
		if l.ID == "" {
			return fmt.Errorf("upsert game_links: empty id")
		}
		l.UpdatedAt = now
		f := bson.M{"_id": l.ID}
		u := bson.M{
			"$set": bson.M{
				"game_slug":    l.GameSlug,
				"platform":     l.Platform,
				"source":       l.Source,
				"title":        l.Title,
				"host":          l.Host,
				"url":           l.URL,
				"link_type":     l.LinkType,
				"supported_by":  l.SupportedBy,
				"updated_at":   l.UpdatedAt,
			},
			"$setOnInsert": bson.M{
				"created_at": now,
			},
		}
		writes = append(writes, mongo.NewUpdateOneModel().SetFilter(f).SetUpdate(u).SetUpsert(true))
	}
	_, err := c.coll(CollGameLinks).BulkWrite(octx, writes, options.BulkWrite().SetOrdered(false))
	return err
}

// SourceLinkSlugs returns the set of game slugs that already have a "source"
// link (link_type="source") for the given source. Used by the source-page
// backfill to skip games already done, so re-runs are idempotent without
// re-HEAD-ing every game.
func (c *Client) SourceLinkSlugs(ctx context.Context, source models.Source) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	cur, err := c.coll(CollGameLinks).Find(ctx, bson.M{
		"source":     source,
		"link_type":   models.LinkTypeSource,
	}, options.Find().SetProjection(bson.M{"game_slug": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var doc struct {
			GameSlug string `bson:"game_slug"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, err
		}
		if doc.GameSlug != "" {
			out[doc.GameSlug] = struct{}{}
		}
	}
	return out, cur.Err()
}