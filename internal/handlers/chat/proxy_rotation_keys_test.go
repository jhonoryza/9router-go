package chat

import (
	"testing"

	"9router/proxy/internal/db"
)

// Pool rotation and connection rotation are independent operators that used to
// share the `rotateStrategy` key, so saving one silently armed the other. Each
// case below pins one half of the separation: a client a consumer depends on.
func TestProxyPoolRotation_DoesNotArmConnectionRotation(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings: %v", err)
	}

	tests := []struct {
		name          string
		settings      string
		wantConnRot   string
		wantPoolCount int
	}{
		{
			name:          "pool rotation alone leaves connection rotation off",
			settings:      `{"providerStrategies":{"opencode":{"proxyRotateStrategy":"round-robin"}}}`,
			wantConnRot:   "",
			wantPoolCount: 1,
		},
		{
			name:          "connection rotation alone leaves pool rotation off",
			settings:      `{"providerStrategies":{"opencode":{"fallbackStrategy":"round-robin","stickyRoundRobinLimit":3}}}`,
			wantConnRot:   "round-robin",
			wantPoolCount: 0,
		},
		{
			name:          "both can be set independently",
			settings:      `{"providerStrategies":{"opencode":{"fallbackStrategy":"round-robin","proxyRotateStrategy":"random"}}}`,
			wantConnRot:   "round-robin",
			wantPoolCount: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := db.NewRepo(database)
			if _, err := repo.InsertProxyPool(db.ProxyPoolData{
				Name: "warp", ProxyURL: "http://172.17.0.1:8001", Type: "http",
			}); err != nil {
				t.Fatalf("insert pool: %v", err)
			}
			if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, tt.settings); err != nil {
				t.Fatalf("insert settings: %v", err)
			}

			s, err := repo.GetSettings()
			if err != nil {
				t.Fatalf("GetSettings: %v", err)
			}
			strat := s.ProviderStrategies["opencode"]

			// Connection rotation is what ApplyConnectionStrategy consumes; a
			// non-empty value here is what makes accounts start cycling.
			connArmed := strat.RotateStrategy != "" && strat.RotateStrategy != "none"
			if connArmed != (tt.wantConnRot != "") {
				t.Errorf("connection rotation armed = %v (RotateStrategy=%q), want %v",
					connArmed, strat.RotateStrategy, tt.wantConnRot != "")
			}

			h := NewChatHandler(repo)
			got := h.ResolveProviderProxyPoolID("opencode")
			poolCount := 0
			if got != "" {
				poolCount = 1
			}
			if poolCount != tt.wantPoolCount {
				t.Errorf("pool rotation selected a pool = %v (id %q), want %v",
					poolCount, got, tt.wantPoolCount)
			}
		})
	}
}

// SetProviderStrategy held both rotations in one field, so the second write
// overwrote the first and connection rotation could not survive alongside pool
// rotation.
func TestSetProviderStrategy_KeepsBothRotations(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings: %v", err)
	}
	repo := db.NewRepo(database)

	err := repo.SetProviderStrategy("opencode", db.ProviderStrategy{
		RotateStrategy:      "round-robin",
		StickyLimit:         3,
		ProxyRotateStrategy: "random",
	})
	if err != nil {
		t.Fatalf("SetProviderStrategy: %v", err)
	}

	s, err := repo.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	strat := s.ProviderStrategies["opencode"]

	if strat.RotateStrategy != "round-robin" {
		t.Errorf("connection rotation = %q, want round-robin", strat.RotateStrategy)
	}
	if strat.ProxyRotateStrategy != "random" {
		t.Errorf("pool rotation = %q, want random", strat.ProxyRotateStrategy)
	}
	if strat.StickyLimit != 3 {
		t.Errorf("sticky limit = %d, want 3", strat.StickyLimit)
	}
}

// The dashboard wrote pool rotation to `rotateStrategy` before a dedicated key
// existed. An operator who saved it that way must keep rotating rather than
// silently fall back to one pool.
func TestProxyPoolRotation_LegacyKeyStillRotates(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings: %v", err)
	}

	repo := db.NewRepo(database)
	ids := make([]string, 2)
	for i, name := range []string{"warp", "warp 2"} {
		pool, err := repo.InsertProxyPool(db.ProxyPoolData{
			Name: name, ProxyURL: "http://172.17.0.1:8001", Type: "http",
		})
		if err != nil {
			t.Fatalf("insert pool: %v", err)
		}
		ids[i] = pool["id"].(string)
	}

	legacy := `{"providerStrategies":{"opencode":{"rotateStrategy":"round-robin"}}}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, legacy); err != nil {
		t.Fatalf("insert settings: %v", err)
	}

	h := NewChatHandler(repo)
	seen := map[string]int{}
	for i := range 4 {
		got := h.ResolveProviderProxyPoolID("opencode")
		if got != ids[0] && got != ids[1] {
			t.Fatalf("call %d: got %q, want one of the two active pools", i, got)
		}
		seen[got]++
	}
	if len(seen) != 2 {
		t.Errorf("legacy pool rotation used %d of 2 pools, want both: %v", len(seen), seen)
	}
}

// "sticky" was accepted as a pool rotation strategy and then served as
// round-robin, promising affinity this resolver does not implement. It is a
// connection-rotation value, so pool rotation must not take it.
func TestIsProxyPoolRotation_RejectsSticky(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"round-robin", true},
		{"roundrobin", true},
		{"random", true},
		{" Round-Robin ", true},
		{"sticky", false},
		{"Sticky", false},
		{"none", false},
		{"", false},
		{"fill-first", false},
	}

	for _, tt := range tests {
		if got := isProxyPoolRotation(tt.value); got != tt.want {
			t.Errorf("isProxyPoolRotation(%q) = %v, want %v", tt.value, got, tt.want)
		}
	}
}

// One shared cursor let one provider's traffic advance another's, so a provider
// with a different pool count skipped positions.
func TestPoolRotation_CursorsAreIndependentPerProvider(t *testing.T) {
	database, cleanup := setupChatTestDB(t)
	defer cleanup()

	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS proxyPools (
		id TEXT PRIMARY KEY,
		isActive INTEGER DEFAULT 1,
		testStatus TEXT,
		data TEXT NOT NULL,
		createdAt TEXT NOT NULL,
		updatedAt TEXT NOT NULL
	);`); err != nil {
		t.Fatalf("create proxyPools: %v", err)
	}
	if _, err := database.Exec(`CREATE TABLE IF NOT EXISTS settings (id INTEGER PRIMARY KEY, data TEXT);`); err != nil {
		t.Fatalf("create settings: %v", err)
	}

	repo := db.NewRepo(database)
	ids := make([]string, 3)
	for i, name := range []string{"a", "b", "c"} {
		pool, err := repo.InsertProxyPool(db.ProxyPoolData{
			Name: name, ProxyURL: "http://172.17.0.1:8001", Type: "http",
		})
		if err != nil {
			t.Fatalf("insert pool: %v", err)
		}
		ids[i] = pool["id"].(string)
	}

	settings := `{"providerStrategies":{"opencode":{"proxyRotateStrategy":"round-robin"},"cline":{"proxyRotateStrategy":"round-robin"}}}`
	if _, err := database.Exec(`INSERT INTO settings (id, data) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET data = excluded.data`, settings); err != nil {
		t.Fatalf("insert settings: %v", err)
	}

	h := NewChatHandler(repo)
	pos := func(id string) int {
		for i, v := range ids {
			if v == id {
				return i
			}
		}
		return -1
	}

	// A full cycle must visit every pool. Which pool comes first depends on the
	// query's ordering, so this asserts coverage, not a fixed starting pool.
	for _, provider := range []string{"opencode", "cline"} {
		seen := map[int]bool{}
		var order []int
		for range 3 {
			order = append(order, pos(h.ResolveProviderProxyPoolID(provider)))
		}
		for _, p := range order {
			seen[p] = true
		}
		if len(seen) != 3 {
			t.Errorf("%s rotation visited %d of 3 pools (%v), want all of them", provider, len(seen), order)
		}
	}

	// Interleaving another provider's traffic must not advance this provider's
	// cursor. One cline request is enough to tell the two apart: with a shared
	// counter it would shift opencode's next pick by one.
	pick := func(provider string, n int) []string {
		out := make([]string, 0, n)
		for range n {
			out = append(out, h.ResolveProviderProxyPoolID(provider))
		}
		return out
	}
	before := pick("opencode", 3)
	_ = pick("cline", 1)
	after := pick("opencode", 3)
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("cline's traffic moved opencode's cursor: %v became %v", before, after)
		}
	}
}