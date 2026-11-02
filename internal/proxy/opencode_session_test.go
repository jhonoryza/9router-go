package proxy

import (
	"regexp"
	"strings"
	"sync"
	"testing"
)

// OpenCode canonical session ids look like ses_<12 hex><14 base62> where the
// hex prefix decodes (inverted) to a recent millisecond timestamp times 4096.
var opencodeCanonicalSessionRegex = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)


func TestTranslateOpenCodeSessionIDMintsCanonicalRecentID(t *testing.T) {
	got := TranslateOpenCodeSessionID("downstream-session-abc", "claude-code")
	if !opencodeCanonicalSessionRegex.MatchString(got) {
		t.Fatalf("expected canonical ses_ id, got %q", got)
	}
	// Upstream (v1.9.15) derives the time component from a sha256 digest of
	// (tool, session) instead of the wall clock — deterministic per session.
	if again := TranslateOpenCodeSessionID("downstream-session-abc", "claude-code"); again != got {
		t.Fatalf("translation not deterministic: %q vs %q", got, again)
	}
	if other := TranslateOpenCodeSessionID("downstream-session-xyz", "claude-code"); other == got {
		t.Fatalf("distinct sessions collapsed to the same id %q", got)
	}
}

func TestTranslateOpenCodeSessionIDStablePerSession(t *testing.T) {
	a1 := TranslateOpenCodeSessionID("sess-one", "claude-code")
	a2 := TranslateOpenCodeSessionID("sess-one", "claude-code")
	b := TranslateOpenCodeSessionID("sess-two", "claude-code")
	c := TranslateOpenCodeSessionID("sess-one", "other-tool")
	if a1 != a2 {
		t.Fatalf("same session mapped to different ids: %q vs %q", a1, a2)
	}
	if a1 == b {
		t.Fatalf("different sessions mapped to the same id %q", a1)
	}
	if a1 == c {
		t.Fatalf("different client tools mapped to the same id %q", a1)
	}
}

func TestTranslateOpenCodeSessionIDPassesCanonicalThrough(t *testing.T) {
	canonical := GenerateOpenCodeSessionID()
	if got := TranslateOpenCodeSessionID(canonical, "claude-code"); got != canonical {
		t.Fatalf("canonical id altered: %q -> %q", canonical, got)
	}
}

func TestTranslateOpenCodeSessionIDConcurrentUse(t *testing.T) {
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := TranslateOpenCodeSessionID(strings.Repeat("x", i+1), "")
			if !opencodeCanonicalSessionRegex.MatchString(id) {
				t.Errorf("non-canonical id %q", id)
			}
		}()
	}
	wg.Wait()
}
