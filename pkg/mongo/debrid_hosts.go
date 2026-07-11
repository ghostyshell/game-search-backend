package mongo

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"game-search-backend/internal/models"
)

// GetDebridHosts returns the cached host -> providers map. An empty/missing
// collection yields an empty map (callers treat empty as "eligibility unknown;
// defer tagging").
func (c *Client) GetDebridHosts(ctx context.Context) (map[string][]models.DebridProvider, error) {
	cur, err := c.coll(CollDebridHosts).Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := make(map[string][]models.DebridProvider)
	for cur.Next(ctx) {
		var h models.DebridHost
		if err := cur.Decode(&h); err != nil {
			continue
		}
		out[h.Host] = h.Providers
	}
	return out, cur.Err()
}

// SetDebridHosts replaces the cached host -> providers map. Each host is one
// doc (_id = host); stale hosts not in the new set are removed.
func (c *Client) SetDebridHosts(ctx context.Context, hosts map[string][]models.DebridProvider) error {
	now := nowSec()
	var writes []mongo.WriteModel
	for host, providers := range hosts {
		f := bson.M{"_id": host}
		u := bson.M{
			"$set": bson.M{
				"providers":  providers,
				"updated_at": now,
			},
		}
		writes = append(writes, mongo.NewUpdateOneModel().SetFilter(f).SetUpdate(u).SetUpsert(true))
	}
	if len(writes) > 0 {
		if _, err := c.coll(CollDebridHosts).BulkWrite(ctx, writes, options.BulkWrite().SetOrdered(false)); err != nil {
			return err
		}
	}
	// ponytail: prune hosts absent from the fresh set by replacing the collection
	// is overkill for v1; stale hosts age out via re-sync overwriting their providers.
	return nil
}