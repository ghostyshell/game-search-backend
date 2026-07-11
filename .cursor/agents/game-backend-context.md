---
name: game-backend-context
description: game-search-backend context. Use for Go handlers, goquery scrapers, MongoDB persistence, background ingest jobs, metadata enrichment, or debrid host sync.
---

You are working in **game-search-backend** - a Go backend that scrapes per-platform game sources, enriches with metadata, and serves a REST API for the game-search-frontend.

## Stack

- Go 1.25, standard library `net/http` with custom router middleware
- goquery for HTML scraping; archive.org Metadata API for IA collections
- MongoDB for persistent storage (collections: `games`, `game_links`, `debrid_hosts`)
- JobScheduler for periodic ingest/enrich/host-sync (ticker + initialDelay + TryLock skip-overlap)
- RAWG + TheGamesDB metadata enrichment
- Debrid host sync (TorBox `/webdl/hosters` + Real-Debrid `/hosts/domains`, both public, no key)
- Docker + GHCR deploy + Sliplane GET hook

## Sources

| Platform | Source | Scraper | Link type |
|----------|--------|---------|-----------|
| Xbox 360 | Minerva Archive | `scraper/minerva.go` | BitTorrent `.torrent` |
| Xbox 360 | Internet Archive | `scraper/ia.go` | direct DDL (archive.org) |
| PC | FitGirl | `scraper/fitgirl.go` | magnet + file-host mirrors |
| PS3/4/5, Switch | DLPsGame | `scraper/dlpsgame.go` | file-host DDL (+ some torrents) |
| PS2, PSP, PSVita | r-roms | `scraper/rroms.go` | file-host DDL only |

Source registry mirrors the local `~/Code/godsend-360` pattern: Go map `platform -> source -> []collectionID|browseURL` (`src/server/app/config.go`).

## Key paths

| Area | Path |
|------|------|
| Entry point | `main.go` |
| Config | `internal/config/config.go`, `internal/config/environment.go` |
| Handlers | `internal/handlers/` (games, search, browse, debrid_hosts, health, monitoring) |
| Middleware | `internal/middleware/` (router, cors, logger, recovery, ip_allowlist) |
| Scrapers | `internal/services/scraper/` (service.go + per-source adapters) |
| Metadata | `internal/services/metadata/` (rawg.go, thegamesdb.go, release_match.go) |
| Jobs | `internal/services/jobs/` (runner.go + per-source ingest/enrich + debridHostsSync) |
| Debrid host sync | `internal/services/debrid/hostsync.go` |
| Models | `internal/models/` |
| MongoDB client | `pkg/mongo/` (client.go, games.go, game_links.go, debrid_hosts.go) |
| Storage interface | `pkg/storage/database.go` |
| Ops tools | `cmd/` is gitignored local-only tooling - runbook in gitignored `.claude/agents/local-cmd-tools.md` |
| Docker | `Dockerfile`, `deployments/Dockerfile` |

## Environment

- `PORT=3011`, `MONGODB_URI`, `MONGO_USERNAME`, `MONGO_PASSWORD`
- `RAWG_API_KEY`, `THEGAMESDB_API_KEY` (optional)
- Per-job interval overrides: `<SOURCE>_SYNC_INTERVAL_MS`, `DEBRID_HOSTS_SYNC_INTERVAL_MS`
- Per-source ingest concurrency: `<SOURCE>_INGEST_CONCURRENCY` (default 6)
- No debrid keys, no Google OAuth (auth lives in the frontend)

## Commands

```bash
go run .            # start server on :3011
go test ./...       # all tests
go build -o server . # build binary
```

## Conventions

- Routes + JSON responses follow `docs/api-reference.md`.
- Add new routes via the route-registration functions in `main.go`; use the existing middleware chain.
- Scraper service is centralized in `internal/services/scraper/service.go` (RegisterScraper + SearchAll goroutine fan-out).
- Mongo upsert uses `$set` for listing fields and `$setOnInsert` for enrichment fields so re-ingest never clobbers RAWG/TheGamesDB data (copy `pkg/mongo/games.go` upsert pattern).
- Each `game_links` doc carries `supportedBy: ["torbox"|"realdebrid"]` computed from live host lists; torrent links are always both. Links whose host is in neither provider's list are dropped at ingest.
- Never commit `.env` or credentials.

## Deploy + git sync

game-search-backend ships to Sliplane via a GHCR image build, **not** `git push` (Sliplane's GitHub build is broken). Build amd64, push to `ghcr.io/akshatsinghkaushik/game-search-backend:latest`, fire the Sliplane deploy hook per the gitignored `.claude/agents/deploy-registry.md` (creds + hook secret live there, uncommitted). **After every deploy, commit + push** so git stays in sync with prod. Push origin granular (never force-push) and alt flattened via `sh ~/Code/scripts/push-alt-flatten.sh`.

## Bulk-fill ingests

One-shot `cmd/<source>ingest` binaries bulk-fill existing catalogs into prod `games_*` against the public Mongo endpoint, reusing the deployed `Runner.<source>Sync` paths so bulk-fill and scheduled sync share one code path. Tooling is local-only and gitignored (`cmd/` in `.gitignore`). Runbook + creds live in the gitignored `.claude/agents/local-cmd-tools.md` + `sliplane-ssh.md`. The deployed sync jobs run recent-only after the bulk-fill.