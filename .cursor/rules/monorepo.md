# GameSearch - Cursor Rules

## Repositories

| Repo | Role | Default port | Stack |
|------|------|--------------|-------|
| `game-search-frontend/` | NextJS frontend (auth, account, debrid, browse) | 3010 | NextJS App Router, TypeScript, Tailwind, shadcn/ui |
| `game-search-backend/` | Go backend (sole API) | 3011 | Go 1.25, goquery scrapers, MongoDB |

The frontend talks to the backend at `BACKEND_URL` (default `http://localhost:3011`).

## Cross-repo constraints

- **Scrapers**: live in `game-search-backend/internal/services/scraper/`. When fixing a source site, update there.
- **API compatibility**: frontend routes and response shapes must stay aligned with `game-search-backend`.
- **Auth**: custom Mongo-based (register + email verify), lives entirely in the frontend. The backend has no auth.
- **Debrid**: backend precomputes per-link `supportedBy` (TorBox/Real-Debrid) via a host-sync job; the frontend resolves the DDL server-side using the user's encrypted key. Per-user debrid keys live only in the frontend.
- **Metadata**: backend enriches games with RAWG + TheGamesDB (keys via env).
- **Secrets**: Never commit `.env`, API keys, or debrid keys. Use `.env.example` as reference.

## Recommended subagents by repo

| Repo | Primary agents |
|------|----------------|
| `game-search-frontend` | `game-frontend-context`, `nextjs-developer`, `typescript-pro`, `security-auditor`, `test-automator` |
| `game-search-backend` | `game-backend-context`, `golang-pro`, `backend-developer`, `security-auditor`, `test-automator` |
| Cross-repo | `game-monorepo-context`, `code-reviewer`, `codebase-orchestrator` |

## Testing

| Repo | Command | Location |
|------|---------|----------|
| Frontend | `npm run build` (tsc strict) + `npm test` | `game-search-frontend/` |
| Backend | `go test ./...` | `game-search-backend/` |

## Deployment

- **Go backend**: ships via GHCR image build + Sliplane GET deploy hook (Sliplane's GitHub build is broken). Runbook in the gitignored `.claude/agents/deploy-registry.md`. After every deploy, commit + push so git stays in sync with prod.
- **NextJS frontend**: ships via Sliplane push (Sliplane builds from the committed `Dockerfile`). Env set in the Sliplane dashboard; `.env.production` keeps placeholder URLs only.

## When making changes

1. Identify which repo owns the behavior (UI vs backend vs scraper).
2. Run the relevant test suite before finishing.
3. Use `code-reviewer` after substantive changes; `security-auditor` for auth/debrid/secrets.