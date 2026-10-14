package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	json "encoding/json/v2"

	"9router/proxy/internal/db"
)

// resetLiveCatalog clears the process-wide cache and restores the real endpoints
// so each case starts from a cold, production-shaped state.
func resetLiveCatalog(t *testing.T) {
	t.Helper()
	prevKiro, prevGrok := kiroCatalogBaseURL, grokCLICatalogURL
	liveCatalogStore.mu.Lock()
	liveCatalogStore.entries = make(map[string]liveCatalogEntry)
	liveCatalogStore.mu.Unlock()
	t.Cleanup(func() {
		kiroCatalogBaseURL, grokCLICatalogURL = prevKiro, prevGrok
		liveCatalogStore.mu.Lock()
		liveCatalogStore.entries = make(map[string]liveCatalogEntry)
		liveCatalogStore.mu.Unlock()
	})
}

// seedLiveConn inserts one active provider connection and returns a handler
// whose Repo points at the seeded database.
func seedLiveConn(t *testing.T, id, provider, connData string, client *http.Client) *ChatHandler {
	t.Helper()
	database, cleanup := setupChatTestDB(t)
	t.Cleanup(cleanup)
	if _, err := database.Exec(`DELETE FROM providerConnections`); err != nil {
		t.Fatalf("delete connections: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='customModels'`); err != nil {
		t.Fatalf("delete customs: %v", err)
	}
	if _, err := database.Exec(`DELETE FROM kv WHERE scope='liveCatalogs'`); err != nil {
		t.Fatalf("delete liveCatalogs: %v", err)
	}
	if _, err := database.Exec(`INSERT INTO providerConnections (id, provider, authType, name, priority, isActive, data, createdAt, updatedAt) VALUES
		(?, ?, 'oauth', ?, 1, 1, ?, '2026-07-18T00:00:00Z', '2026-07-18T00:00:00Z')`, id, provider, provider, connData); err != nil {
		t.Fatalf("seed %s: %v", provider, err)
	}
	h := NewChatHandler(db.NewRepo(database))
	if client != nil {
		h.Client = client
	}
	return h
}

// TestHandleModels_CacheOnlyAfterRefresh proves the new flow: a refresh
// fetches and persists, then /v1/models serves the persisted list without any
// network call — even on a fresh handler (simulating a restart).
func TestHandleModels_CacheOnlyAfterRefresh(t *testing.T) {
	resetLiveCatalog(t)

	var fetches atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"modelId":"auto","modelName":"Auto","rateMultiplier":1,"tokenLimits":{"maxInputTokens":300000}},{"modelId":"claude-sonnet-4","modelName":"Sonnet 4","rateMultiplier":1}]}`))
	}))
	defer srv.Close()
	kiroCatalogBaseURL = srv.URL + "/%s"

	h := seedLiveConn(t, "conn-kiro-live", "kiro",
		`{"accessToken":"ya29.kiro-test","providerSpecificData":{"profileArn":"arn:aws:codewhisperer:eu-west-1:123456789012:profile/ABC"}}`,
		srv.Client())

	refreshed, failed, _, err := h.RefreshLiveCatalogs(context.Background(), "kiro")
	if err != nil || refreshed != 1 || failed != 0 {
		t.Fatalf("refresh = (%d, %d, %v), want (1, 0, nil)", refreshed, failed, err)
	}
	if fetches.Load() != 1 {
		t.Fatalf("expected 1 upstream fetch during refresh, got %d", fetches.Load())
	}

	// A brand-new handler with a cold in-memory cache — the restart case —
	// still answers from the persisted KV copy.
	fresh := NewChatHandler(h.Repo)
	joined := strings.Join(modelsIDs(t, fresh), "\n")
	if !strings.Contains(joined, "kr/auto") || !strings.Contains(joined, "kr/auto-thinking") {
		t.Errorf("persisted live catalog models missing, got:\n%s", joined)
	}
	if !strings.Contains(joined, "kr/claude-sonnet-4-agentic") {
		t.Errorf("kiro agentic variant missing, got:\n%s", joined)
	}
	if strings.Contains(joined, "kr/claude-opus-5") {
		t.Errorf("static registry entry leaked next to a live catalog, got:\n%s", joined)
	}
	if n := fetches.Load(); n != 1 {
		t.Errorf("/v1/models must never fetch, expected still 1 upstream fetch, got %d", n)
	}
}

// The request path must not reach the network even when nothing has ever been
// refreshed: a dead upstream cannot stall /v1/models (the regression this
// redesign fixes — previously every request waited out the 30s kiro timeout).
func TestHandleModels_NeverFetchesOnRequest(t *testing.T) {
	resetLiveCatalog(t)

	var fetches atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"modelId":"auto"}]}`))
	}))
	defer srv.Close()
	kiroCatalogBaseURL = srv.URL + "/%s"

	h := seedLiveConn(t, "conn-kiro-nofetch", "kiro",
		`{"accessToken":"ya29.kiro-test"}`, srv.Client())

	joined := strings.Join(modelsIDs(t, h), "\n")
	if fetches.Load() != 0 {
		t.Errorf("/v1/models fetched the live catalog %d times; it must answer from cache only", fetches.Load())
	}
	// Without a persisted catalog the static registry is the fallback.
	if !strings.Contains(joined, "kr/claude-opus-5") {
		t.Errorf("static catalog must be served when nothing has been refreshed, got:\n%s", joined)
	}
}

// A failed refresh must not poison anything: /v1/models falls back to the
// static registry until a refresh succeeds.
func TestRefreshLiveCatalogs_FailureKeepsStaticFallback(t *testing.T) {
	resetLiveCatalog(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	kiroCatalogBaseURL = srv.URL + "/%s"

	h := seedLiveConn(t, "conn-kiro-dead", "kiro",
		`{"accessToken":"ya29.dead"}`, srv.Client())

	refreshed, failed, _, _ := h.RefreshLiveCatalogs(context.Background(), "kiro")
	if refreshed != 0 || failed != 1 {
		t.Fatalf("refresh = (%d, %d), want (0, 1)", refreshed, failed)
	}
	joined := strings.Join(modelsIDs(t, h), "\n")
	if !strings.Contains(joined, "kr/claude-opus-5") {
		t.Errorf("static catalog must be used when the refresh failed, got:\n%s", joined)
	}
}

// Upstream only consults the live resolver when enabledModels is not pinned.
func TestHandleModels_EnabledModelsSkipLiveCatalog(t *testing.T) {
	resetLiveCatalog(t)

	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"modelId":"auto"}]}`))
	}))
	defer srv.Close()
	kiroCatalogBaseURL = srv.URL + "/%s"

	h := seedLiveConn(t, "conn-kiro-pinned", "kiro",
		`{"accessToken":"ya29.kiro-test","providerSpecificData":{"enabledModels":["pinned-model"]}}`, srv.Client())

	joined := strings.Join(modelsIDs(t, h), "\n")

	if called {
		t.Error("live catalog must not be fetched when enabledModels is pinned")
	}
	if !strings.Contains(joined, "kr/pinned-model") {
		t.Errorf("pinned model must be listed, got:\n%s", joined)
	}
}

// Grok CLI publishes only what the account may use.
func TestRefreshLiveCatalogs_GrokCLI(t *testing.T) {
	resetLiveCatalog(t)

	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"grok-4.7","context_length":256000}]}`))
	}))
	defer srv.Close()
	grokCLICatalogURL = srv.URL + "/v1/models"

	h := seedLiveConn(t, "conn-grok-live", "grok-cli",
		`{"accessToken":"xai-test-token","providerSpecificData":{"email":"dev@example.com"}}`, srv.Client())

	refreshed, failed, _, _ := h.RefreshLiveCatalogs(context.Background(), "grok-cli")
	if refreshed != 1 || failed != 0 {
		t.Fatalf("refresh = (%d, %d), want (1, 0)", refreshed, failed)
	}
	joined := strings.Join(modelsIDs(t, h), "\n")
	if !strings.Contains(joined, "gcli/grok-4.7") {
		t.Errorf("live grok model missing, got:\n%s", joined)
	}
	if strings.Contains(joined, "gcli/grok-4.5-high") {
		t.Errorf("static grok entries must be replaced by the live catalog, got:\n%s", joined)
	}
	if gotHeaders.Get("x-grok-client-identifier") != grokCLIIdentifier {
		t.Errorf("expected grok client identifier header, got %q", gotHeaders.Get("x-grok-client-identifier"))
	}
	if gotHeaders.Get("x-email") != "dev@example.com" {
		t.Errorf("expected x-email header from providerSpecificData, got %q", gotHeaders.Get("x-email"))
	}
}

// Providers without a live resolver are skipped, not counted as failures.
func TestRefreshLiveCatalogs_SkipsProvidersWithoutResolver(t *testing.T) {
	resetLiveCatalog(t)

	h := seedLiveConn(t, "conn-deepseek", "deepseek", `{"apiKey":"sk-x"}`, nil)
	refreshed, failed, skipped, _ := h.RefreshLiveCatalogs(context.Background(), "")
	if refreshed != 0 || failed != 0 || skipped != 1 {
		t.Fatalf("refresh = (%d, %d, %d), want (0, 0, 1)", refreshed, failed, skipped)
	}
}

// A refresh still coalesces: one connection refreshes exactly once regardless
// of how many refresh calls race (kv write idempotent, cache overwrite fine).
func TestRefreshLiveCatalogs_PersistsForRestart(t *testing.T) {
	resetLiveCatalog(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"modelId":"auto"}]}`))
	}))
	defer srv.Close()
	kiroCatalogBaseURL = srv.URL + "/%s"

	h := seedLiveConn(t, "conn-kiro-persist", "kiro", `{"accessToken":"ya29.kiro-persist"}`, srv.Client())
	if _, failed, _, _ := h.RefreshLiveCatalogs(context.Background(), "kiro"); failed != 0 {
		t.Fatal("refresh failed")
	}

	kv, err := h.Repo.GetKVScope("liveCatalogs")
	if err != nil {
		t.Fatalf("kv scope: %v", err)
	}
	raw, ok := kv["conn-kiro-persist"]
	if !ok || raw == "" {
		t.Fatal("refresh must persist the catalog to the liveCatalogs kv scope")
	}
	var record persistedLiveCatalog
	if err := json.Unmarshal([]byte(raw), &record); err != nil {
		t.Fatalf("persisted record invalid: %v", err)
	}
	if len(record.Models) == 0 || record.Models[0].ID != "auto" {
		t.Errorf("persisted models wrong: %+v", record.Models)
	}
	if _, perr := time.Parse(time.RFC3339, record.SyncedAt); perr != nil {
		t.Errorf("syncedAt not RFC3339: %v", perr)
	}
}

// modelsIDsFromBody decodes a /v1/models response body into model ids.
func modelsIDsFromBody(body []byte) []string {
	var resp struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil
	}
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	return ids
}
