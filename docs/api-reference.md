# game-search-backend API reference

Base URL: `http://localhost:3011` (local). CORS allowlist via `CORS_ALLOWED_ORIGINS`.

## Health + stats

- `GET /health` -> `200 {"status":"ok","database":"ok","responseTimeMs":N,"timestamp":...}` (degraded + db error if Mongo down)
- `GET /stats` -> `200 {"games":N,"game_links":N,"debrid_hosts":N,"mongo_connected":1}`

## Games

- `GET /games?platform=<platform>&q=<query>&limit=<n>&offset=<n>` - browse or search. If `q` is set, case-insensitive title search; otherwise newest-first browse. `platform` optional in both.
- `GET /games/<slug>` - game detail with its `game_links` (each carries `host`, `linkType`, `supportedBy`).

## Debrid

- `GET /debrid/hosts` - cached host -> providers map from the live TorBox + Real-Debrid host lists. `{"hosts":{"archive.org":["torbox","realdebrid"],...}}`

## Platforms

`xbox360`, `pc`, `ps3`, `ps4`, `ps5`, `ps2`, `psp`, `psvita`, `switch`.

## Link shape

```json
{
  "id": "<stable sha1>",
  "gameSlug": "halo-3",
  "platform": "xbox360",
  "source": "minerva",
  "title": "Halo 3",
  "host": "minerva-archive.org",
  "url": "https://minerva-archive.org/rom?name=...",
  "linkType": "ddl|magnet|torrent",
  "supportedBy": ["torbox", "realdebrid"]
}
```

`supportedBy` is computed at ingest by the `debridHostsSync` job + per-link tagger
from the live TorBox + Real-Debrid host lists. Torrent/magnet links are always
both. DDL links get the host's providers (empty if the host is in neither list).
The frontend resolves the DDL server-side using the user's encrypted debrid key
for a chosen provider in `supportedBy`.