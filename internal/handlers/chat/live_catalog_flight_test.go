package chat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"9router/proxy/internal/db"
	"9router/proxy/internal/handlers/shared"
	"9router/proxy/internal/models"
)

// TestLiveCatalogFlightSurvivesLeaderCancellation pins that the shared catalog
// fetch is not cancelled when the caller who started it disconnects.
//
// DIVERGENCE (this fork): resolveLiveCatalog here is cache-only — it never
// fetches in the request path (PR #1). The fetch happens only in the dashboard
// refresh path (live_catalog_refresh.go, POST /api/models/live-catalog/refresh
// and the daily job), which carries the same WithoutCancel guard from upstream
// #364. This test pins upstream's inline-fetch design, so it would deadlock
// waiting for a fetch that this build intentionally never performs. Skipped
// rather than deleted so the divergence stays visible next to the upstream
// commit.
func TestLiveCatalogFlightSurvivesLeaderCancellation(t *testing.T) {
	t.Skip("this fork is cache-only: resolveLiveCatalog never fetches (PR #1); the #364 WithoutCancel guard lives in the refresh path")
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			w.Write([]byte(`{"data":[{"id":"m-one"}]}`))
			return
		}
		// Any second call means the flight was not shared: it would find
		// nothing, which is exactly the regression this test guards.
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	database, cleanup := setupChatTestDB(t)
	defer cleanup()
	h := NewChatHandler(db.NewRepo(database))

	// A distinct connection id keeps the catalog cache key unique, so a result
	// cached by an earlier test cannot satisfy this one.
	conn := &models.ProviderConnection{
		ID:       "conn-flight-cancel",
		Provider: "openai-compatible-flight",
		IsActive: 1,
	}
	connData := &shared.ConnectionData{
		APIKey:               "sk-flight",
		ProviderSpecificData: map[string]any{"baseUrl": srv.URL},
	}
	const providerID = "openai-compatible-flight"

	leaderCtx, cancelLeader := context.WithCancel(context.Background())
	defer cancelLeader()
	leaderDone := make(chan []LiveModel, 1)
	go func() {
		leaderDone <- h.resolveLiveCatalog(leaderCtx, conn, connData, providerID)
	}()

	// The shared fetch is now in flight. Hang the leader's client up, which is
	// the ctx.Err() != nil state a disconnect leaves behind.
	<-entered
	cancelLeader()
	time.Sleep(50 * time.Millisecond)

	followerDone := make(chan []LiveModel, 1)
	go func() {
		followerDone <- h.resolveLiveCatalog(context.Background(), conn, connData, providerID)
	}()

	// Let the follower reach the in-flight call, then let the upstream answer.
	time.Sleep(50 * time.Millisecond)
	close(release)

	follower := <-followerDone
	<-leaderDone

	if len(follower) != 1 || follower[0].ID != "m-one" {
		t.Fatalf("follower got %v, want the leader's shared result [m-one]", idsOf(follower))
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1 — the follower should join the leader's flight", got)
	}
}
