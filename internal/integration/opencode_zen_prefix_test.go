//go:build integration

package integration

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"9router/proxy/internal/proxy/executor"
	"9router/proxy/internal/translator"
)

// DeepSeek's upstream prompt cache keys on the longest byte-identical prefix
// of the serialized request body. The zen/deepseek lane rewrites every request
// (InjectReasoningContent adds reasoning_content, ConcealFingerprintTools
// renames/merges tools, then the lane stamps stream and model) and each rewrite
// round-trips through generic maps. encoding/json/v2 randomizes map member
// order on every marshal, so a logically IDENTICAL request serializes
// differently each time — measured at ~197 distinct bodies out of 200 — and
// every request pays a full cache miss.
//
// This test pins the lane's rewrite chain to a single canonical serialization.
// It calls the real executor and translator functions; no provider is
// contacted and no API token is needed.
func TestZenRewriteChainDeterministic(t *testing.T) {
	in := []byte(`{"model":"deepseek-v4-pro","stream":false,"messages":[
		{"role":"system","content":"You are a coding agent."},
		{"role":"user","content":"Read the config file."},
		{"role":"assistant","content":"Reading it now."}]}`)

	seen := map[string]int{}
	for i := 0; i < 200; i++ {
		b := executor.InjectReasoningContent(in, "opencode-zen")
		b, _ = translator.ConcealFingerprintTools(b)
		seen[fmt.Sprintf("%x", sha256.Sum256(b))]++
	}
	if len(seen) != 1 {
		t.Errorf("the zen rewrite chain produced %d distinct bodies over 200 identical requests; "+
			"prefix caching needs exactly one deterministic serialization", len(seen))
	}
}
