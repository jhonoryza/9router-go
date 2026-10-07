package executor

import (
	json "encoding/json/v2"
)

// marshalStable serializes with json.Deterministic(true): map members are
// emitted in sorted key order, so the same logical request always produces
// byte-identical output. DeepSeek (and other prefix-caching upstreams) key
// their prompt cache on the longest byte-identical request prefix; the
// default json/v2 marshaling randomizes map member order per call, which
// reshuffles the whole body on every rewrite and turns every request after
// the first into a full cache miss. Use marshalStable for every
// unmarshal->mutate->remarshal cycle on request bodies headed to
// prefix-caching upstreams (deepseek, kimi).
func marshalStable(v any) ([]byte, error) {
	return json.Marshal(v, json.Deterministic(true))
}
