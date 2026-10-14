package chat

import (
	"context"
	"strings"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/log"
	"9router/proxy/internal/models"
)

// Live catalog refresh — the only paths that ever fetch a provider's live
// catalog. /v1/models itself never fetches: resolveLiveCatalog answers from
// the in-memory cache or the persisted KV copy, so a slow or dead provider
// cannot stall model discovery (previously a kiro connection behind a slow
// upstream held every /v1/models request for its full 30s timeout).
//
// Fetch happens in exactly two places:
//   - POST /api/models/live-catalog/refresh (dashboard "Sync Models" button)
//   - StartBackgroundLiveCatalogRefresh, once per day
//
// A successful fetch is persisted to the kv scope liveCatalogs keyed by
// connection id, so the list is warm even after a restart. When nothing has
// been fetched yet the model list falls back to the static registry — a
// client still gets a usable catalog, never a fetch wait.

const (
	liveCatalogKVScope      = "liveCatalogs"
	liveCatalogRefreshEvery = 24 * time.Hour
)

// persistedLiveCatalog is the KV record written by a successful refresh.
type persistedLiveCatalog struct {
	SyncedAt string      `json:"syncedAt"`
	Models   []LiveModel `json:"models"`
}

// hasLiveCatalogResolver reports whether a provider's connection would be
// fetched by a refresh (the same set resolveLiveCatalog used to fetch).
func hasLiveCatalogResolver(providerID string) bool {
	return providerID == "kiro" || providerID == "grok-cli" || isCompatibleProviderID(providerID)
}

// RefreshLiveCatalogs fetches the live catalog for every active connection of
// the given provider ("" = every provider that has a resolver), persists each
// result to KV, and refreshes the in-memory cache. It returns the counts the
// dashboard endpoint reports.
func (h *ChatHandler) RefreshLiveCatalogs(ctx context.Context, providerFilter string) (refreshed, failed, skipped int, lastErr error) {
	if h.Repo == nil {
		return 0, 0, 0, nil
	}
	providerFilter = strings.TrimSpace(providerFilter)
	conns, err := h.Repo.GetProviderConnections(providerFilter, true)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, conn := range conns {
		if conn == nil || conn.Provider == "" || conn.IsActive == 0 {
			continue
		}
		if !hasLiveCatalogResolver(conn.Provider) {
			skipped++
			continue
		}
		var connData shared.ConnectionData
		if conn.Data != "" {
			_ = json.Unmarshal([]byte(conn.Data), &connData)
		}
		discovered := h.fetchAndStoreLiveCatalog(ctx, conn, &connData, conn.Provider)
		if len(discovered) == 0 {
			failed++
			lastErr = errLiveCatalogUnavailable
			log.Warn("live-catalog", "refresh failed", "provider", conn.Provider, "conn", conn.ID)
			continue
		}
		refreshed++
		log.Info("live-catalog", "catalog refreshed", "provider", conn.Provider, "conn", conn.ID, "models", len(discovered))
	}
	return refreshed, failed, skipped, lastErr
}

// fetchAndStoreLiveCatalog performs one discovery attempt for a connection,
// including the upstream retry-on-401 token refresh, and stores the result in
// the memory cache and the KV persistence layer. Returns nil when discovery
// produced nothing.
func (h *ChatHandler) fetchAndStoreLiveCatalog(ctx context.Context, conn *models.ProviderConnection, connData *shared.ConnectionData, providerID string) []LiveModel {
	if conn == nil || connData == nil {
		return nil
	}
	accessToken := connData.AccessToken
	if accessToken == "" {
		accessToken = connData.APIKey
	}
	if accessToken == "" {
		return nil
	}

	fetch := h.liveCatalogFetcher(conn, connData, providerID)
	if fetch == nil {
		return nil
	}

	discovered := fetch(ctx, accessToken)
	if len(discovered) == 0 && liveCatalogRefreshToken(conn) != "" {
		// Upstream refreshes the credential on 401/403 and retries once.
		if refreshed, _, rErr := h.forceRefreshOAuthToken(conn.ID); rErr == nil && refreshed != "" {
			discovered = fetch(ctx, refreshed)
		} else {
			log.Warn("live-catalog", "token refresh failed", "provider", providerID, "conn", conn.ID, "error", rErr)
		}
	}
	if len(discovered) == 0 {
		return nil
	}

	cacheKey := liveCatalogKey(providerID, conn, connData)
	liveCatalogStoreModels(cacheKey, discovered)
	h.persistLiveCatalog(conn.ID, discovered)
	return discovered
}

// liveCatalogFetcher returns the discovery function for a provider connection,
// or nil when the provider has no live resolver.
func (h *ChatHandler) liveCatalogFetcher(conn *models.ProviderConnection, connData *shared.ConnectionData, providerID string) func(context.Context, string) []LiveModel {
	switch {
	case providerID == "kiro":
		return func(ctx context.Context, token string) []LiveModel {
			return h.fetchKiroLiveCatalog(ctx, conn, connData, token)
		}
	case providerID == "grok-cli":
		return func(ctx context.Context, token string) []LiveModel {
			return h.fetchGrokCLILiveCatalog(ctx, conn, connData, token)
		}
	case isCompatibleProviderID(providerID):
		return func(ctx context.Context, _ string) []LiveModel {
			return h.fetchCompatibleNodeModels(ctx, conn, connData, providerID)
		}
	default:
		return nil
	}
}

// persistLiveCatalog writes the discovered list to the kv scope so a restart
// does not lose it. Failures are logged, not fatal: the in-memory cache still
// serves until the next refresh.
func (h *ChatHandler) persistLiveCatalog(connID string, discovered []LiveModel) {
	if h.Repo == nil || connID == "" {
		return
	}
	record := persistedLiveCatalog{
		SyncedAt: time.Now().UTC().Format(time.RFC3339),
		Models:   discovered,
	}
	b, err := json.Marshal(record)
	if err != nil {
		log.Warn("live-catalog", "persist marshal failed", "conn", connID, "error", err)
		return
	}
	if err := h.Repo.SetKV(liveCatalogKVScope, connID, string(b)); err != nil {
		log.Warn("live-catalog", "persist failed", "conn", connID, "error", err)
	}
}

// loadPersistedLiveCatalog reads the KV record for a connection. Returns nil
// when nothing has ever been refreshed for it.
func (h *ChatHandler) loadPersistedLiveCatalog(connID string) []LiveModel {
	if h.Repo == nil || connID == "" {
		return nil
	}
	kv, err := h.Repo.GetKVScope(liveCatalogKVScope)
	if err != nil {
		return nil
	}
	raw := kv[connID]
	if raw == "" {
		return nil
	}
	var record persistedLiveCatalog
	if json.Unmarshal([]byte(raw), &record) != nil {
		return nil
	}
	return record.Models
}

// StartBackgroundLiveCatalogRefresh refreshes every connection's live catalog
// once at boot (after a short grace period) and then once per day. It never
// touches the request path: a slow provider only slows this loop.
func StartBackgroundLiveCatalogRefresh(ctx context.Context, repo *db.Repo) {
	if repo == nil {
		return
	}
	h := NewChatHandler(repo)
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(60 * time.Second):
		}
		if refreshed, failed, _, _ := h.RefreshLiveCatalogs(ctx, ""); refreshed+failed > 0 {
			log.Info("live-catalog", "background refresh done", "refreshed", refreshed, "failed", failed)
		}

		ticker := time.NewTicker(liveCatalogRefreshEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if refreshed, failed, _, _ := h.RefreshLiveCatalogs(ctx, ""); refreshed+failed > 0 {
					log.Info("live-catalog", "background refresh done", "refreshed", refreshed, "failed", failed)
				}
			}
		}
	}()
}

// errLiveCatalogUnavailable marks a connection whose discovery produced
// nothing; the refresh endpoint reports the last such error.
var errLiveCatalogUnavailable = &liveCatalogError{"live catalog unavailable"}

type liveCatalogError struct{ msg string }

func (e *liveCatalogError) Error() string { return e.msg }

// resolveLiveCatalogForLookup is the request-path entry point: memory cache
// first, then the persisted KV copy. It NEVER fetches.
func (h *ChatHandler) resolveLiveCatalogForLookup(conn *models.ProviderConnection, connData *shared.ConnectionData, providerID string) []LiveModel {
	if conn == nil || connData == nil {
		return nil
	}
	cacheKey := liveCatalogKey(providerID, conn, connData)
	if cached, ok := liveCatalogCached(cacheKey); ok {
		return cached
	}
	persisted := h.loadPersistedLiveCatalog(conn.ID)
	if len(persisted) == 0 {
		return nil
	}
	liveCatalogStoreModels(cacheKey, persisted)
	return persisted
}
