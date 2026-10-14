package dashboard

import (
	"net/http"

	"9router/proxy/internal/handlerutil"
)

// HandleRefreshLiveCatalogs handles POST /api/models/live-catalog/refresh.
//
// The only operator-facing way to fetch a provider's live catalog: /v1/models
// itself answers from cache only, so a slow or dead provider can no longer
// stall model discovery. `provider` query is optional ("" = every connection
// whose provider has a live resolver); the dashboard provider-detail Sync
// button passes the provider it is open on.
//
// Each successful discovery is persisted to the kv scope liveCatalogs keyed by
// connection id, and the in-memory cache is refreshed, so the next /v1/models
// serves the new list immediately.
func (h *DashboardHandler) HandleRefreshLiveCatalogs(w http.ResponseWriter, r *http.Request) {
	if h.ChatHandler == nil {
		handlerutil.WriteJSONError(w, http.StatusNotImplemented, "live catalog refresh unavailable")
		return
	}
	refreshed, failed, skipped, lastErr := h.ChatHandler.RefreshLiveCatalogs(r.Context(), r.URL.Query().Get("provider"))
	if refreshed == 0 && skipped == 0 && failed > 0 {
		handlerutil.WriteJSONError(w, http.StatusBadGateway, "live catalog refresh failed: "+lastErr.Error())
		return
	}
	handlerutil.WriteJSON(w, http.StatusOK, map[string]any{
		"success":   true,
		"refreshed": refreshed,
		"failed":    failed,
		"skipped":   skipped,
	})
}
