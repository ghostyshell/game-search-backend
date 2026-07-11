// Package storage defines the persistence interface for game-search-backend.
// The concrete implementation lives in pkg/mongo.
package storage

import (
	"context"
	"time"

	"game-search-backend/internal/models"
)

// Database is the persistence interface. Slim for games: games, game_links,
// debrid_hosts only.
type Database interface {
	Migrate(ctx context.Context) error
	Close(ctx context.Context) error
	HealthCheck(ctx context.Context) error

	UpsertGame(ctx context.Context, game models.Game) error
	UpsertGames(ctx context.Context, games []models.Game) error
	UpsertGameLinks(ctx context.Context, links []models.GameLink) error
	GetGameBySlug(ctx context.Context, slug string) (*models.Game, []models.GameLink, error)
	SearchGames(ctx context.Context, query string, platform models.Platform, limit, offset int) ([]models.Game, error)
	BrowseGames(ctx context.Context, platform models.Platform, limit, offset int) ([]models.Game, error)
	GetGamesMissingRawgEnrich(ctx context.Context, limit, offset int) ([]models.Game, error)
	SetGameEnrichment(ctx context.Context, slug string, f models.RawgFields) error

	GetDebridHosts(ctx context.Context) (map[string][]models.DebridProvider, error)
	SetDebridHosts(ctx context.Context, hosts map[string][]models.DebridProvider) error

	// Stats returns collection counts for /health and monitoring.
	Stats(ctx context.Context) (map[string]int64, error)
}

// JobScheduleConfig is the interval + initial delay for a periodic job.
// Re-declared here (matching config) for callers that import storage only.
type JobScheduleConfig struct {
	Interval     time.Duration
	InitialDelay time.Duration
}