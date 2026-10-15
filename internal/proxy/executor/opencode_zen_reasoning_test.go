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
	body := []byte(`{"model":"some-paid-zen-model","messages":[{"role":"assistant","content":"hi"},{"role":"user","content":"again"}]}`)
	got := InjectReasoningContent(body, "opencode-zen")
	if strings.Contains(string(got), "reasoning_content") {
		t.Fatalf("paid zen model must not be touched, got %s", string(got))
	}
}

func TestInjectReasoningContentRealValueKept(t *testing.T) {
	body := []byte(`{"model":"mimo-v2.6-flash-free","messages":[{"role":"assistant","content":"hi","reasoning_content":"real reasoning"}]}`)
	got := InjectReasoningContent(body, "opencode-zen")
	if !strings.Contains(string(got), `"reasoning_content":"real reasoning"`) {
		t.Fatalf("real reasoning_content must be left alone, got %s", string(got))
	}
}
