## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

Rules:
- For codebase questions, first run `graphify query "<question>"` when graphify-out/graph.json exists. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).

## Deploy + git sync

game-search-backend ships to Sliplane via a GHCR image build, **not** `git push` (Sliplane's GitHub build integration is broken). Build the amd64 image, push to `ghcr.io/akshatsinghkaushik/game-search-backend:latest`, and fire the Sliplane deploy hook per the gitignored `.claude/agents/deploy-registry.md` (creds + hook secret live there, uncommitted). **After every deploy, commit the shipped changes and push** so git stays in sync with what is running in prod; a GHCR deploy without a commit leaves prod ahead of git. Push origin granular (never force-push) and alt flattened via `sh ~/Code/scripts/push-alt-flatten.sh`.

## Bulk-fill ingests

Existing game catalogs are bulk-filled into prod `games_*` by one-shot `cmd/<source>ingest` binaries run locally on the Mac against the public Mongo endpoint. The bulk-fill tooling is local-only and gitignored (`cmd/` is in `.gitignore`) and reuses the deployed `Runner.<source>Sync` paths so bulk-fill and scheduled sync share one code path. The runbook (env vars, ETA monitoring, resume procedure) lives in the gitignored `.claude/agents/local-cmd-tools.md`; creds live in the gitignored `sliplane-ssh.md`. The deployed sync jobs run recent-only after the bulk-fill.