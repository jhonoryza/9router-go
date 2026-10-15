package proxy

import (
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// OpenCode canonical session ids look like ses_<12 hex><14 base62> where the
// hex prefix decodes (inverted) to a recent millisecond timestamp times 4096.
var opencodeCanonicalSessionRegex = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)

func decodeOpenCodeSessionTimestamp(t *testing.T, id string) uint64 {
	t.Helper()
	prefix := id[len("ses_"):][:12]
	var value uint64
	for i := 0; i < 12; i += 2 {
		b, err := hex.DecodeString(prefix[i : i+2])
		if err != nil {
			t.Fatalf("bad hex prefix %q: %v", prefix, err)
		}
		value = (value << 8) | uint64(b[0])
	}
	return (^value & (uint64(1)<<48 - 1))
}

func TestTranslateOpenCodeSessionIDMintsCanonicalRecentID(t *testing.T) {
	got := TranslateOpenCodeSessionID("downstream-session-abc", "claude-code")
	if !opencodeCanonicalSessionRegex.MatchString(got) {
		t.Fatalf("expected canonical ses_ id, got %q", got)
	}
	decoded := decodeOpenCodeSessionTimestamp(t, got)
	expected := (uint64(time.Now().UnixMilli())*0x1000 + 1) & (uint64(1)<<48 - 1)
	if decoded != expected {
		t.Fatalf("session id does not encode the current timestamp: got %d want %d", decoded, expected)
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

func TestTranslateOpenCodeSessionIDCacheEviction(t *testing.T) {
	previousMax := opencodeSessionMaxLen
	opencodeSessionMaxLen = 2
	t.Cleanup(func() { opencodeSessionMaxLen = previousMax })
	opencodeSessionMu.Lock()
	opencodeSessionCache = map[string]opencodeSessionEntry{}
	opencodeSessionMu.Unlock()
	t.Cleanup(func() {
		opencodeSessionMu.Lock()
		opencodeSessionCache = map[string]opencodeSessionEntry{}
		opencodeSessionMu.Unlock()
	})

	TranslateOpenCodeSessionID("s1", "")
	TranslateOpenCodeSessionID("s2", "")
	TranslateOpenCodeSessionID("s3", "") // triggers eviction

	opencodeSessionMu.Lock()
	size := len(opencodeSessionCache)
	opencodeSessionMu.Unlock()
	if size > previousMax && size > 2 {
		t.Fatalf("cache exceeded cap: %d entries", size)
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
