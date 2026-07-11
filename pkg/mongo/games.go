package mongo

import (
	"context"
	"fmt"
	"regexp"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"game-search-backend/internal/models"
)

// UpsertGame upserts a catalog game. $set refreshes listing fields (title,
// platform, source, link_count, updated_at); $setOnInsert seeds the enrichment
// fields + created_at so re-ingest never clobbers RAWG/TheGamesDB data.
func (c *Client) UpsertGame(ctx context.Context, game models.Game) error {
	if game.Slug == "" {
		return fmt.Errorf("upsert game: empty slug")
	}
	now := nowSec()
	game.UpdatedAt = now
	// Defensive: no current caller passes a publish date to the singular upsert
	// (ingestLinks uses the bulk path with its own fallback), but a future direct
	// caller with SourcePublishedAt == 0 would otherwise persist 0 and sort to the
	// bottom under desc sort. Mirror ingestLinks: fall back to scrape wallclock.
	if game.SourcePublishedAt == 0 {
		game.SourcePublishedAt = now
	}
	filter := bson.M{"slug": game.Slug}
	update := bson.M{
		"$set": bson.M{
			"title":                game.Title,
			"platform":             game.Platform,
			"source":               game.Source,
			"link_count":           game.LinkCount,
			"source_published_at":  game.SourcePublishedAt,
			"updated_at":           game.UpdatedAt,
		},
		"$setOnInsert": bson.M{
			"created_at":     now,
			"enriched_rawg":  false,
			"enriched_tgdb":  false,
			"rawg_id":         0,
			"tgdb_id":         0,
			"rawg_detail_fetched": false,
		},
	}
	_, err := c.coll(CollGames).UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	return err
}

// UpsertGames bulk-upserts games by slug. Chunked like the links writer so a
// large platform ingest (r-roms PS2 is thousands of games) does one BulkWrite
// per chunk instead of one WAN round-trip per game - both faster and far less
// exposed to a mid-run connection reset over the shared Sliplane Mongo.
func (c *Client) UpsertGames(ctx context.Context, games []models.Game) error {
	if len(games) == 0 {
		return nil
	}
	for start := 0; start < len(games); start += upsertChunk {
		end := start + upsertChunk
		if end > len(games) {
			end = len(games)
		}
		if err := c.upsertGamesChunk(ctx, games[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) upsertGamesChunk(ctx context.Context, games []models.Game) error {
	octx, cancel := opTimeoutCtx(ctx)
	defer cancel()
	writes := make([]mongo.WriteModel, 0, len(games))
	now := nowSec()
	for _, g := range games {
		if g.Slug == "" {
			return fmt.Errorf("upsert games: empty slug")
		}
		f := bson.M{"slug": g.Slug}
		u := bson.M{
			"$set": bson.M{
				"title":               g.Title,
				"platform":            g.Platform,
				"source":              g.Source,
				"link_count":          g.LinkCount,
				"source_published_at": g.SourcePublishedAt,
				"updated_at":          now,
			},
			"$setOnInsert": bson.M{
				"created_at":    now,
				"enriched_rawg":  false,
				"enriched_tgdb":  false,
				"rawg_id":        0,
				"tgdb_id":        0,
				"rawg_detail_fetched": false,
			},
		}
		writes = append(writes, mongo.NewUpdateOneModel().SetFilter(f).SetUpdate(u).SetUpsert(true))
	}
	_, err := c.coll(CollGames).BulkWrite(octx, writes, options.BulkWrite().SetOrdered(false))
	return err
}

// GetGameBySlug returns the game and its links.
func (c *Client) GetGameBySlug(ctx context.Context, slug string) (*models.Game, []models.GameLink, error) {
	var g models.Game
	if err := c.coll(CollGames).FindOne(ctx, bson.M{"slug": slug}).Decode(&g); err != nil {
		return nil, nil, err
	}
	cur, err := c.coll(CollGameLinks).Find(ctx, bson.M{"game_slug": slug})
	if err != nil {
		return nil, nil, err
	}
	defer cur.Close(ctx)
	var links []models.GameLink
	if err := cur.All(ctx, &links); err != nil {
		return nil, nil, err
	}
	return &g, links, nil
}

// SearchGames does a case-insensitive title prefix search, optionally filtered
// by platform. ponytail: regex prefix; a real text index can replace this if
// volume demands.
func (c *Client) SearchGames(ctx context.Context, query string, platform models.Platform, limit, offset int) ([]models.Game, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	f := bson.M{}
	if query != "" {
		// QuoteMeta neutralizes user regex metacharacters (ReDoS / pattern
		// injection); the ^ anchor makes it a prefix search so a broad query
		// can't be crafted into a catastrophic scan.
		f["title"] = bson.M{"$regex": "^" + regexp.QuoteMeta(query), "$options": "i"}
	}
	if platform != "" {
		f["platform"] = platform
	}
	opts := options.Find().SetLimit(int64(limit)).SetSkip(int64(offset)).SetSort(bson.D{{Key: "source_published_at", Value: -1}})
	cur, err := c.coll(CollGames).Find(ctx, f, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.Game
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// BrowseGames lists games for a platform, newest first by source publish date
// (dlpsgame) falling back to scrape wallclock for sources without a publish date.
func (c *Client) BrowseGames(ctx context.Context, platform models.Platform, limit, offset int) ([]models.Game, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	f := bson.M{}
	if platform != "" {
		f["platform"] = platform
	}
	opts := options.Find().SetLimit(int64(limit)).SetSkip(int64(offset)).SetSort(bson.D{{Key: "source_published_at", Value: -1}})
	cur, err := c.coll(CollGames).Find(ctx, f, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.Game
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetGamesMissingRawgEnrich returns up to limit games that need RAWG enrichment
// or a detail refresh, oldest-first so re-runs resume where the last run stopped.
// A game is selected when it is not yet RAWG-enriched OR its detail fetch has not
// completed (rawg_detail_fetched != true). The $or is what lets the existing
// ragenrich run backfill pre-detail-fetched entries in place.
func (c *Client) GetGamesMissingRawgEnrich(ctx context.Context, limit, offset int) ([]models.Game, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	f := bson.M{"$or": bson.A{
		bson.M{"enriched_rawg": bson.M{"$ne": true}},
		bson.M{"rawg_detail_fetched": bson.M{"$ne": true}},
	}}
	opts := options.Find().
		SetLimit(int64(limit)).
		SetSkip(int64(offset)).
		SetSort(bson.D{{Key: "created_at", Value: 1}})
	cur, err := c.coll(CollGames).Find(ctx, f, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.Game
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetGameEnrichment writes RAWG enrichment fields (list + detail) onto a game
// and marks it enriched. rawg_id is $setOnInsert-protected at ingest; here we
// overwrite the enrichment slice since this is the authoritative enrich write.
// rawg_detail_fetched reflects whether the /games/{id} detail call succeeded;
// the widened missing-query re-selects games where it is still false.
func (c *Client) SetGameEnrichment(ctx context.Context, slug string, f models.RawgFields) error {
	if slug == "" {
		return fmt.Errorf("set enrichment: empty slug")
	}
	filter := bson.M{"slug": slug}
	update := bson.M{
		"$set": bson.M{
			"rawg_id":             f.ID,
			"rawg_rating":         f.Rating,
			"rawg_cover":          f.Cover,
			"rawg_released":       f.Released,
			"rawg_genres":         f.Genres,
			"rawg_description":    f.Description,
			"rawg_metacritic":     f.Metacritic,
			"rawg_metacritic_url": f.MetacriticURL,
			"rawg_esrb":           f.ESRB,
			"rawg_developers":     f.Developers,
			"rawg_publishers":     f.Publishers,
			"rawg_tags":           f.Tags,
			"rawg_stores":         f.Stores,
			"rawg_screenshots":    f.Screenshots,
			"rawg_playtime":       f.Playtime,
			"rawg_reddit_url":     f.RedditURL,
			"rawg_detail_fetched": f.DetailFetched,
			"enriched_rawg":       true,
			"enriched_at":         nowSec(),
		},
	}
	_, err := c.coll(CollGames).UpdateOne(ctx, filter, update)
	return err
}

// CountEnrichedRawg returns how many games are marked enriched_rawg=true.
// Diagnostic counter for tracking the ragenrich run.
func (c *Client) CountEnrichedRawg(ctx context.Context) (int64, error) {
	return c.coll(CollGames).CountDocuments(ctx, bson.M{"enriched_rawg": true})
}

// CountRawgDetailFetched returns how many games have completed the /games/{id}
// detail fetch (rawg_detail_fetched=true). Diagnostic for the backfill: the
// remaining set is total games minus this count (the widened missing-query
// selects every game that lacks it).
func (c *Client) CountRawgDetailFetched(ctx context.Context) (int64, error) {
	return c.coll(CollGames).CountDocuments(ctx, bson.M{"rawg_detail_fetched": true})
}

// CountMissingRawgEnrich returns the size of the backfill target set: games
// matching the same $or GetGamesMissingRawgEnrich uses. Diagnostic for ETA.
func (c *Client) CountMissingRawgEnrich(ctx context.Context) (int64, error) {
	return c.coll(CollGames).CountDocuments(ctx, bson.M{"$or": bson.A{
		bson.M{"enriched_rawg": bson.M{"$ne": true}},
		bson.M{"rawg_detail_fetched": bson.M{"$ne": true}},
	}})
}

// GetGamesBySource returns every game sourced from the given source, with
// only the fields the source-page backfill needs (slug, platform, title).
// Walked once per backfill run against the games collection.
func (c *Client) GetGamesBySource(ctx context.Context, source models.Source) ([]models.Game, error) {
	cur, err := c.coll(CollGames).Find(ctx, bson.M{"source": source},
		options.Find().SetProjection(bson.M{"slug": 1, "platform": 1, "source": 1, "title": 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []models.Game
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ResetEnrichmentForPlatform clears the RAWG-enrichment flags for every game on
// the given platform so the widened missing-query re-selects them and ragenrich
// re-runs the (fixed) search+detail against each. One-off backfill hook for
// re-correcting wrong matches from the old pickBest fallback; idempotent.
// Returns the number of games flagged for re-enrichment.
func (c *Client) ResetEnrichmentForPlatform(ctx context.Context, platform models.Platform) (int64, error) {
	res, err := c.coll(CollGames).UpdateMany(ctx,
		bson.M{"platform": platform},
		bson.M{"$set": bson.M{"enriched_rawg": false, "rawg_detail_fetched": false}},
	)
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// CountPlatform returns the total/enriched/detail-fetched/having-rawg_id
// counts for a platform - the before/after snapshot for the re-enrich one-off.
func (c *Client) CountPlatform(ctx context.Context, platform models.Platform) (total, enriched, detailed, withID int64, err error) {
	f := bson.M{"platform": platform}
	if total, err = c.coll(CollGames).CountDocuments(ctx, f); err != nil {
		return
	}
	if enriched, err = c.coll(CollGames).CountDocuments(ctx, bson.M{"platform": platform, "enriched_rawg": true}); err != nil {
		return
	}
	if detailed, err = c.coll(CollGames).CountDocuments(ctx, bson.M{"platform": platform, "rawg_detail_fetched": true}); err != nil {
		return
	}
	withID, _ = c.coll(CollGames).CountDocuments(ctx, bson.M{"platform": platform, "rawg_id": bson.M{"$gt": 0}})
	return
}

// SourcePubAtUpdate is one {slug -> publish date} write for the source-page
// publish-date backfill. PubAt is Unix seconds.
type SourcePubAtUpdate struct {
	Slug  string
	PubAt int64
}

// SetGamesSourcePublishedAt bulk-updates source_published_at on the games
// collection by slug. Used by the one-off cmd/pubdatedfill backfill to correct
// existing nswgame/fitgirl games from scrape wallclock to the real post date.
// $set touches only source_published_at (NOT updated_at - this is a backfill,
// not a re-scrape). Chunked at upsertChunk so a few-thousand-game WAN bulk
// write stays under the 16MB wire limit. Idempotent: re-running writes the same
// date. Slugs with PubAt <= 0 are dropped (no meta tag parsed -> leave as-is).
func (c *Client) SetGamesSourcePublishedAt(ctx context.Context, updates []SourcePubAtUpdate) error {
	var valid []SourcePubAtUpdate
	for _, u := range updates {
		if u.Slug == "" || u.PubAt <= 0 {
			continue
		}
		valid = append(valid, u)
	}
	if len(valid) == 0 {
		return nil
	}
	for start := 0; start < len(valid); start += upsertChunk {
		end := start + upsertChunk
		if end > len(valid) {
			end = len(valid)
		}
		if err := c.setSourcePublishedAtChunk(ctx, valid[start:end]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) setSourcePublishedAtChunk(ctx context.Context, updates []SourcePubAtUpdate) error {
	octx, cancel := opTimeoutCtx(ctx)
	defer cancel()
	writes := make([]mongo.WriteModel, 0, len(updates))
	for _, u := range updates {
		f := bson.M{"slug": u.Slug}
		uq := bson.M{"$set": bson.M{"source_published_at": u.PubAt}}
		writes = append(writes, mongo.NewUpdateOneModel().SetFilter(f).SetUpdate(uq))
	}
	_, err := c.coll(CollGames).BulkWrite(octx, writes, options.BulkWrite().SetOrdered(false))
	return err
}