package chat

import "testing"

// DIVERGENCE (fork): upstream's resolveLiveCatalog fetches in the request
// path, and these two tests pin that behaviour (a pinned connection makes zero
// upstream calls; a failed fetch is negatively cached). This build's
// resolveLiveCatalog is cache-only — it never fetches, so "upstream calls"
// is always 0 regardless of pins or failures and the upstream assertions are
// meaningless here. The pinned-skip property is still enforced (see the
// pinnedEnabledModels check in live_catalog.go, upstream d329f9f4) and negative
// caching is moot because there is no fetch to cache.

func TestResolveLiveCatalogPinnedEnabledModelsSkipsFetch(t *testing.T) {
	t.Skip("DIVERGENCE: cache-only catalog never fetches in the request path")
}

func TestResolveLiveCatalogCachesFailure(t *testing.T) {
	t.Skip("DIVERGENCE: cache-only catalog never fetches in the request path")
}
