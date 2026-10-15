# fix(opencode): zen free-tier reasoning placeholder + canonical session ids

## Problem

Two OpenCode free-tier bugs ported from decolua/9router:

1. **#4805** — multi-turn chats on `oc/` free models served via the keyed zen lane 400 with `missing_required_parameter: 'messages[N].reasoning_content'`. `InjectReasoningContent` only matched providers `opencode`/`opencode-go` (or deepseek/kimi models), so `opencode-zen` free ids never got the placeholder on assistant turns.
2. **#4791** — `TranslateOpenCodeSessionID` hashed the downstream session id into the 12-hex prefix. Zen decodes that prefix to an inverted timestamp and rejects ids whose decoded value is not recent (`403 FreeTierError`) for any client that is not OpenCode itself.

## Fix

1. `InjectReasoningContent` (executor/transform.go): the keyed zen lane now qualifies when the request's bare model id is an OpenCode free-tier id (catalog-driven via `IsOpencodeFreeTierModel` across all three lanes). Paid zen models stay untouched; real `reasoning_content` values are left alone; the placeholder is only `" "`.
2. `TranslateOpenCodeSessionID` (proxy/opencode.go): mints a canonical id via the existing `GenerateOpenCodeSessionID()` once per downstream session id + client tool and reuses it. In-memory cache, 1h TTL, 1000-entry cap with expired-first eviction. Canonical `ses_` ids still pass through untouched. Parity with decolua/9router#4791.

## Testing

- New `internal/proxy/opencode_session_test.go`: minted id decodes to the current timestamp (counter pinned to 1), stability per session, pass-through of canonical ids, cache eviction cap, concurrent use.
- New `internal/proxy/executor/opencode_zen_reasoning_test.go`: zen free model gets the placeholder, paid zen model untouched, real values preserved.
- `go test ./internal/proxy/... ./internal/translator/ ./internal/handlers/chat/` green.
