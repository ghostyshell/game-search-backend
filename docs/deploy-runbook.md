# Deploy runbook - game-search-backend

game-search-backend ships to Sliplane via a **GHCR image build**, not `git push`
(Sliplane's GitHub build integration is broken for this stack). Build the amd64
image, push to GHCR, and fire the Sliplane deploy hook.

The **credentials** (GHCR PAT, Sliplane service ID + deploy hook secret) live in
the **gitignored** `.claude/agents/deploy-registry.md` (uncommitted, never
pushed). This file describes the flow only - no secrets here.

## Build + push

```bash
docker build --platform linux/amd64 -f deployments/Dockerfile -t ghcr.io/akshatsinghkaushik/game-search-backend:latest .
docker tag ghcr.io/akshatsinghkaushik/game-search-backend:latest \
           ghcr.io/akshatsinghkaushik/game-search-backend:$(git rev-parse --short HEAD)
docker push ghcr.io/akshatsinghkaushik/game-search-backend:latest
docker push ghcr.io/akshatsinghkaushik/game-search-backend:$(git rev-parse --short HEAD)
```

## Fire the Sliplane deploy hook

```bash
curl -sS "https://api.sliplane.io/deploy/<service-id>/<hook-secret>" -o /dev/null -w "%{http_code}\n"
```

Expect `200`/`204`. The hook pulls the new image and restarts the container.

## Verify

```bash
# Over SSH (creds in gitignored sliplane-ssh.md):
ssh ... "md5sum /app/server"
# Compare to the local md5 of /out/server from the build stage.
curl -sS https://<backend-public-url>/health
```

## Required Sliplane env vars (set in the Sliplane dashboard)

- `MONGODB_URI` (+ `MONGO_USERNAME`/`MONGO_PASSWORD` if not embedded) - shared Sliplane Mongo
- `MONGODB_DB=game_search`
- `PORT=3011`
- `ENVIRONMENT=production`
- `FRONTEND_URL=https://<frontend-public-url>`
- `CORS_ALLOWED_ORIGINS=https://<frontend-public-url>`
- `RAWG_API_KEY` (optional; enrichment skipped if unset)
- `THEGAMESDB_API_KEY` (optional; keyless works at lower limits)
- Per-job interval overrides (`MINERVA_SYNC_INTERVAL_MS`, `DEBRID_HOSTS_SYNC_INTERVAL_MS`, ...)

No debrid keys, no OAuth secrets - the backend never sees user keys.

## Git sync

After every deploy, commit the shipped changes and push so git stays in sync
with what is running in prod. Push origin granular (never force-push) and alt
flattened:

```bash
git push origin <branch>
sh ~/Code/scripts/push-alt-flatten.sh <branch>
```