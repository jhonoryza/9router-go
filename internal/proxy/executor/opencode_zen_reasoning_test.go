package executor

import (
	"strings"
	"testing"
)

func TestInjectReasoningContentZenFreeModel(t *testing.T) {
	body := []byte(`{"model":"mimo-v2.6-flash-free","messages":[{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`)
	got := InjectReasoningContent(body, "opencode-zen")
	if !strings.Contains(string(got), `"reasoning_content":" "`) {
		t.Fatalf("expected reasoning_content placeholder injected for zen free model, got %s", string(got))
	}
}

func TestInjectReasoningContentZenPaidModelUntouched(t *testing.T) {
	// DIVERGENCE: upstream scopes the whole opencode-zen provider (PR #5290
	// provider rule wins), so paid zen models DO get the placeholder now.
	// The free-tier-only distinction from decolua #4805 is no longer enforced.
	body := []byte(`{"model":"some-paid-zen-model","messages":[{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`)
	got := InjectReasoningContent(body, "opencode-zen")
	if !strings.Contains(string(got), `"reasoning_content":" "`) {
		t.Fatalf("upstream provider rule injects placeholder even for paid zen models, got %s", string(got))
	}
}

func TestInjectReasoningContentRealValueKept(t *testing.T) {
	body := []byte(`{"model":"mimo-v2.6-flash-free","messages":[{"role":"assistant","content":"hi","reasoning_content":"real reasoning"}]}`)
	got := InjectReasoningContent(body, "opencode-zen")
	if !strings.Contains(string(got), `"reasoning_content":"real reasoning"`) {
		t.Fatalf("real reasoning_content must be left alone, got %s", string(got))
	}
}
