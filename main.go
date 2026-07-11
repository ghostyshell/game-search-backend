// Command game-search-backend is the API server.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"game-search-backend/internal/config"
	"game-search-backend/internal/handlers"
	"game-search-backend/internal/middleware"
	"game-search-backend/internal/services/debrid"
	"game-search-backend/internal/services/jobs"
	"game-search-backend/internal/services/metadata"
	"game-search-backend/internal/services/scraper"
	"game-search-backend/pkg/mongo"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config load: %v", err)
	}

	errs, warnings := cfg.Validate()
	for _, w := range warnings {
		log.Printf("config warning: %v", w)
	}
	if len(errs) > 0 {
		for _, e := range errs {
			log.Printf("config error: %v", e)
		}
		os.Exit(1)
	}

	// Mongo.
	db, err := mongo.NewClient(cfg.Database.Mongo.URI, cfg.Database.Mongo.DBName)
	if err != nil {
		log.Fatalf("mongo connect: %v", err)
	}
	defer db.Close(context.Background())
	if err := db.Migrate(context.Background()); err != nil {
		log.Fatalf("mongo migrate: %v", err)
	}
	log.Printf("mongo connected (db=%s)", cfg.Database.Mongo.DBName)

	// Scrapers.
	svc := scraper.NewService()
	svc.RegisterScraper(scraper.NewMinervaScraper(svc.Client()))
	svc.RegisterScraper(scraper.NewRRomsScraper(svc.Client()))
	svc.RegisterScraper(scraper.NewFitGirlScraper(svc.Client()))
	svc.RegisterScraper(scraper.NewDLPsGameScraper(svc.Client()))
	svc.RegisterScraper(scraper.NewNSWGameScraper(svc.Client()))

	// Runner + jobs.
	hostSyncer := debrid.NewHostSyncer()
	runner := jobs.NewRunner(db, svc, hostSyncer)
	runner.Enricher = metadata.NewEnricher(
		metadata.NewRawgClient(cfg.Metadata.RAWGAPIURL, cfg.Metadata.RAWGAPIKeys),
	)

	scheduler := jobs.NewScheduler()
	scheduler.Register(jobs.Job{
		Name:     "debridHostsSync",
		Schedule: jobs.Schedule{Interval: cfg.BackgroundJobs.DebridHostsSync.Interval, InitialDelay: cfg.BackgroundJobs.DebridHostsSync.InitialDelay},
		Run:      runner.DebridHostsSync,
	})
	scheduler.Register(jobs.Job{
		Name:     "minervaSync",
		Schedule: jobs.Schedule{Interval: cfg.BackgroundJobs.MinervaSync.Interval, InitialDelay: cfg.BackgroundJobs.MinervaSync.InitialDelay},
		Run:      runner.MinervaSync,
	})
	scheduler.Register(jobs.Job{
		Name:     "rromsSync",
		Schedule: jobs.Schedule{Interval: cfg.BackgroundJobs.RRomsSync.Interval, InitialDelay: cfg.BackgroundJobs.RRomsSync.InitialDelay},
		Run:      runner.RRomsSync,
	})
	// dlpsGameSync, fitGirlSync, nswGameSync are NOT registered here: those
	// sources (dlpsgame.com, fitgirl-repacks.site, nswgame.com) are all
	// Cloudflare-fronted and block Sliplane's Hetzner egress (403 / TLS fail),
	// so a prod-side sync errors every tick and never upserts. They are
	// synced from the Mac instead (which can reach them) via caffeinate-wrapped
	// launchd cron jobs running cmd/{dlpsgame,fitgirl,nswgame}ingest against prod
	// Mongo. The cmd tools call runner.<Source>Sync directly, so they share one
	// code path with this scheduler entry. minerva + rroms are NOT CF-blocked
	// and keep running in prod below.
	if len(cfg.Metadata.RAWGAPIKeys) > 0 {
		scheduler.Register(jobs.Job{
			Name:     "metaEnricher",
			Schedule: jobs.Schedule{Interval: cfg.BackgroundJobs.MetaEnricher.Interval, InitialDelay: cfg.BackgroundJobs.MetaEnricher.InitialDelay},
			Run:      runner.MetaEnrich,
		})
	} else {
		log.Printf("RAWG_API_KEYS not set; metaEnricher job disabled")
	}
	stopScheduler := scheduler.Start(context.Background())
	defer stopScheduler()

	// HTTP server.
	srv := middleware.NewServer(cfg,
		&handlers.GamesHandler{DB: db},
		&handlers.DebridHandler{DB: db},
		&handlers.HealthHandler{DB: db},
	)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		log.Printf("server listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}