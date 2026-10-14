### Changed
- **`/v1/models` no longer fetches live catalogs in the request path.** A slow or dead provider (kiro's 30s AWS timeout being the worst case) previously stalled every model-list request whenever the 5-minute in-memory cache expired, because a failed fetch was not cached at all — the same connection could be re-fetched on every request indefinitely.

### How it works now
- `/v1/models` (and combo capability resolution) answers from cache only: the in-memory store, then the persisted KV copy (`liveCatalogs` scope, keyed by connection id).
- Live discovery happens in exactly two places, both off the request path:
  - **`POST /api/models/live-catalog/refresh?provider=<id>`** — new dashboard endpoint, wired to a "Refresh Live Catalog" button in the provider detail view (shown for kiro, grok-cli, and openai/anthropic/custom-embedding-compatible nodes).
  - **A background job** that refreshes every connection with a live resolver once at boot (60s grace) and then once per day.
- A provider whose catalog has never been refreshed falls back to the static registry, so a client still gets a usable catalog immediately.
- Successful refreshes persist to KV, so the catalog survives restarts.

### Parity note
This is a deliberate divergence from upstream `decolua/9router` (LIVE_MODEL_RESOLVERS in `src/app/api/v1/models/route.js`), which performs live discovery inside the request. Trade-off: a model a provider adds appears in `/v1/models` only after the next refresh (button or daily job), not on the next request. Model *calls* are unaffected — routing does not read this cache.

### Verification
- `go test ./internal/handlers/chat/ -run 'TestHandleModels|TestRefreshLiveCatalogs'` — new tests prove the request path issues zero network calls, restart restores the catalog from KV, failure keeps the static fallback, and grok-cli/kiro discovery behaves as before through the refresh path.
- Full `go test ./...` and `go test -tags=integration` show no new failures beyond the pre-existing SPA/PWA baseline (`web/dist` not built).
- `bun test src/lib/` (178 pass) + `npx tsc -b` clean.
