# game-search-backend

Go backend for GameSearch. Scrapes per-platform game sources, enriches with metadata (RAWG + TheGamesDB), and serves a REST API consumed by [game-search-frontend](https://github.com/akshatsinghkaushik/game-search-frontend).

## Platforms and sources

| Platform | Source | Link type |
|----------|--------|-----------|
| Xbox 360 | Minerva Archive (`minerva-archive.org`) | BitTorrent `.torrent` |
| Xbox 360 | Internet Archive (`archive.org`) | direct DDL |
| PC | FitGirl (`fitgirl-repacks.site`) | magnet + file-host mirrors |
| PS3 / PS4 / PS5 / Switch | DLPsGame (`dlpsgame.com`) | file-host DDL |
| PS2 / PSP / PSVita | r-roms (`r-roms.github.io`) | file-host DDL |

Each scraped link is tagged with its host domain. A `debridHostsSync` job fetches the live TorBox + Real-Debrid host lists and stamps each `game_links` doc with `supportedBy` so the frontend can offer debrid resolution for the providers that actually support a given host. Torrent links are always supported by both providers. Links whose host is supported by neither provider are dropped at ingest.

## Stack

- Go 1.25, stdlib `net/http`, goquery, mongo-driver
- MongoDB (collections: `games`, `game_links`, `debrid_hosts`)
- JobScheduler (ticker + initialDelay + TryLock skip-overlap) for periodic ingest/enrich/host-sync
- One-shot `cmd/<source>ingest` binaries (gitignored, local-only) for bulk-fill against the public Mongo endpoint

## Develop

```bash
sh scripts/install-hooks.sh     # enable pre-commit (changelog + AGENTS.md sync)
cp .env.example .env             # fill in keys
go run .                         # server on :3011
go test ./...
```

See `AGENTS.md` for the full contributor guide and `.cursor/agents/game-backend-context.md` for the repo map.

## Deploy

Ships to Sliplane via a GHCR image build + GET deploy hook (Sliplane's GitHub build is broken). Build amd64, push to `ghcr.io/akshatsinghkaushik/game-search-backend:latest`, fire the deploy hook. Runbook + creds live in the gitignored `.claude/agents/deploy-registry.md`. After every deploy, commit + push so git stays in sync with prod.

Push both remotes: `sh ~/Code/scripts/push-both-remotes.sh main` (origin granular, alt flattened).

## Related

- Frontend: [game-search-frontend](https://github.com/akshatsinghkaushik/game-search-frontend)
- Cross-repo coordination: `.cursor/agents/game-monorepo-context.md`