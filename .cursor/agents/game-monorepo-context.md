---
name: game-monorepo-context
description: GameSearch repo map and cross-repo coordination. Use when a task spans game-search-frontend and game-search-backend, or when unsure which repo owns a feature.
---

You are working across the two GameSearch repos under `~/Code`:

## Repo map

| Directory | Purpose | Port |
|-----------|---------|------|
| `game-search-frontend/` | NextJS frontend (browse, game detail, account, auth, debrid resolution) | 3010 |
| `game-search-backend/` | Go backend - per-platform game scrapers, metadata enrichment, REST API, debrid host sync | 3011 |

## Coordination rules

1. **Scraper + ingest changes** live in `game-search-backend/internal/services/scraper/` and `internal/services/jobs/`.
2. **UI changes** that depend on API shape must match `game-search-backend`'s routes and response shapes.
3. **Auth + account + debrid keys** live in the frontend (`game-search-frontend/lib/auth`, `lib/debrid`). The backend never holds user debrid keys.
4. **Never commit secrets** - `.env`, API keys, debrid keys, service account JSON.

## Delegation guide

- NextJS UI/auth/debrid/accounts -> stay in `game-search-frontend`, use `game-frontend-context`
- Go handlers/scrapers/jobs/performance -> `game-search-backend`, use `game-backend-context`
- Cross-repo refactors -> coordinate with `codebase-orchestrator`

## Key integration points

- Frontend reads games from backend: `BACKEND_URL` (default `http://localhost:3011`)
- Auth: custom Mongo-based (register + email verify) in the frontend; backend has no auth
- Debrid: backend precomputes per-link `supportedBy` (TorBox/Real-Debrid) from live host lists; frontend resolves the DDL server-side using the user's encrypted key
- MongoDB: one shared Sliplane Mongo - frontend owns `users`/`sessions`/`accounts`, backend owns `games_*`

When invoked, first identify which repo(s) are affected, then proceed in the correct directory with the repo-specific context agent.