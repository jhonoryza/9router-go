### feat(deepseek-web): route chat.deepseek.com with a browser-login userToken

Adds the first provider in this build authenticated by a credential a browser
session produces rather than an API key or an OAuth grant, ported from OmniRoute
`open-sse/executors/deepseek-web.ts`. The credential is the `userToken` from
`chat.deepseek.com` → DevTools → Application → Local Storage.

**Why:** the deepseek account a user already has is a free consumer web account,
not an API plan, and it has been unusable here. `grok-web` and `perplexity-web`
were already catalogued but had no executor at all, so a chat request fell
through to the shared passthrough and sent `Authorization: Bearer <cookie>` to a
site that answers 401.

**What it does, per turn:**

- `GET /api/v0/users/current` exchanges the `userToken` for a short-lived Bearer
  access token, memoized an hour and dropped on any auth failure. A pasted
  `{"value":"…"}` localStorage entry is unwrapped, which is the shape a copy out
  of DevTools actually has.
- `POST /api/v0/chat_session/create` opens a session, closed again once the turn
  is done.
- `POST /api/v0/chat/create_pow_challenge` → a **DeepSeekHashV1** proof of work
  is solved and returned in `X-Ds-Pow-Response`. That hash is SHA3-256 with the
  last 23 KECCAK-p[1600] rounds; Go's `crypto/sha3` only exposes 24, so the
  sponge is implemented in `deepseek_web_pow.go` with the prefix absorbed once and
  reused per candidate, bounded by a 30s deadline and cancellable from the
  request context.
- The upstream event stream is translated to OpenAI chunks, keeping the
  reasoning and answer channels separate and folding search citations into the
  answer.

**Details worth knowing:**

- This API reports auth and rate limiting as **HTTP 200 with a code in the
  body** (40003 / 40002). Both are mapped to 401 / 429, so the router cools down
  and rotates the account instead of retrying a dead session.
- A stream that closes without the upstream's `FINISHED` signal is reported as
  an error, not as a completed turn. Serving the partial text with
  `finish_reason: stop` is how a client comes to treat a truncated reply as the
  model's conclusion.
- `Origin` and `Referer` follow the connection's configured base URL, so a
  `baseUrl` override is honoured end to end instead of only for the completion
  call.
- `deepseek-web` and `gemini-web` are separate entries from `deepseek` and
  `gemini`, with their own aliases (`ds-web`, `gweb`) and their own model
  catalogues — pinned by a test, since a cross-alias would send a browser
  credential to an API endpoint.
- `AddConnectionModal` now persists `authType: 'cookie'` for a browser-login
  provider instead of `'apikey'`. The probe dispatcher already branched on
  `cookie`, so every cookie provider was taking the wrong probe path.

**gemini-web is catalog-only.** `gemini.google.com` has no API the web client
calls directly — upstream drives it with an embedded Chromium — and this build
ships no browser. It is listable and manageable; a chat request is refused with
501 and the reason, and the probe says it cannot check rather than reporting a
valid cookie as invalid.

**Verification:** `go build ./...` and `go vet ./...` clean.
`internal/proxy/executor/deepseek_web_pow_test.go` pins the 23-round permutation
against OmniRoute's own vectors (`deepseek-pow-js-only.test.ts`) plus the block
boundary cases those vectors never reach.
`deepseek_web_test.go` drives a whole turn against one `httptest` upstream and
verifies the call order, that the proof-of-work nonce really satisfies the
challenge, the browser fingerprint headers, both response shapes, and that a
truncated stream is not served as complete.
`validate_web_providers_test.go` covers the probe's two misleading answers: a
rejected credential arriving as HTTP 200, and a 200 carrying no token.
`deepseek_web_catalog_test.go` pins the catalog and the provider isolation.
Live: the credential exchange, session creation and proof-of-work were confirmed
against the real endpoint — the nonce for a difficulty-144000 challenge is found
in ~400ms. The completion itself could not be exercised because DeepSeek's status
page showed an active outage (incident 7116348214287, "Identified") throughout,
so the stream translator's handling of a healthy response remains unproven
against the live site. `bun run build`, 344 web tests and `make vet-svelte`
(0 unresolved identifiers) pass.

### fix(deepseek-web): read the answer the site actually sends, and recover tool calls

Two defects found by running a real turn against chat.deepseek.com, both of
which the `httptest` fixtures had been shaped to agree with.

**The answer arrived without a topic.** The stream carries three shapes, and only
one names one:

```
{"v":{"response":{...fragments...}}}                          the answer, topicless
{"p":"response/fragments/-1/content","o":"APPEND","v":"x"}   one character at a time
{"p":"response/status","o":"SET","v":"FINISHED"}               the sign-off
```

The decoder dispatched on `p`, so the answer fell through to a default branch
that handled a bare string and dropped an object — losing the whole reply while
still finding `FINISHED`. The turn therefore reported success with no text, which
is why a live run answered `502 upstream answered 200 without a completion`. A
topicless object carrying `response` is now read before the topic switch, and the
per-character `APPEND` form is folded into the channel already open. A plain
question went from 2 chunks to 55 and now returns `{"content":"ok"}` live.

**Tool calling is a convention here, not a protocol.** The endpoint has no
`tools[]` parameter and the stream has no tool-call topic: a model told a tool
exists answers by *typing* a JSON object one character at a time, in 47 separate
`APPEND` operations. A client treating that as a normal streaming answer would
hold `{"name":"get_weather","arguments":{"` with no way to tell a truncated call
from prose.

So the two paths are kept apart. A request carrying `tools[]` buffers the turn to
completion and parses only whole JSON — the same guarantee the native API gives,
since it does not stream tool arguments either. A request without `tools[]`
reaches none of that code and streams exactly as before. The declarations are
folded into the prompt, because there is no field to send them in.

Recovery is deliberately conservative: the answer must be a single JSON object
naming a tool the client actually declared. An undeclared name, prose around the
object, or malformed JSON is passed through as ordinary content rather than
guessed at — a wrong guess becomes an invocation the client acts on.

Verified live: a `deepseek-v4-pro` turn asked about the weather and returned

```json
"finish_reason":"tool_calls",
"function":{"name":"get_weather","arguments":"{\"city\": \"Jakarta\"}"}
```

**Also fixed:** `parameters` is accepted as either a JSON Schema object or a
string, since the OpenAI shape sends an object while several SDKs stringify it
first; typing it as a string rejected the common form outright.

### fix(deepseek-web): stop the agent loop repeating a tool call forever

An agent driven through this provider called `get_weather`, received the result,
and called it again on every subsequent turn until its retry cap — reported as
`Assistant returned empty stop after retry cap`.

The cause was in the prompt, not the model. A tool-calling turn is replayed to
the web API as text, because the endpoint accepts a single prompt string. An
assistant turn that ends in a tool call carries `content: null`, so it was
dropped from the replay entirely. The transcript reached the model as:

```
User: What is the weather in Jakarta? Use the tool.

Tool result (get_weather) Sunny, 31C, humidity 70%
```

A result with no visible cause. The model reads that as "I have not called
anything yet" and calls again, and the loop cannot terminate because the next
replay is identical.

The call is now rendered into the transcript as `[called <name> with <args>]`
ahead of the result it produced, and the tool instructions state that such a line
means the call already happened. Verified live: the two-turn sequence now ends —
turn one calls the tool, turn two answers *"The weather in Jakarta is sunny with a
temperature of 31°C and humidity at 70%"* with `finish_reason: stop`.

**Also fixed: prose around the object.** The extractor required the answer to
*begin* with `{`, so a model that wrote "Let me check that for you." first had
its whole reply discarded and the client received `content: null` — an empty
stop, which is the other half of the same symptom. The object is now located
inside the answer with brace matching that respects strings and escapes, and the
text around it is returned as the message content rather than dropped.

An undeclared tool name is still refused: a model inventing `delete_everything`
must never become a call the client executes.

### Note on what is and is not verified

The turn is verified end to end against the live site: a plain question returns
`{"content":"ok"}`, a tool question returns a whole `tool_calls` message, and the
two-turn agent sequence terminates instead of repeating the call. Streaming and
non-streaming were both driven through a booted gateway, directly and through a
`combo` — 109 chunks, `finish_reason: stop`, `[DONE]`.

Not verified: behaviour under a client's own retry injection. A client that
appends "Stopped without actionable output; Attempt #N/3" turns on the gateway's
answer being absent has, in that observed run, appended the notice while the
gateway had in fact returned the full reply with `status: success` and the
content recorded. That is a client-side observation, not a finding against this
provider, and it is recorded here so the next reader does not re-derive it.

### Scope: this provider only

Earlier drafts of this branch also removed `grok-web`, `perplexity-web` and
`gemini-web` on the grounds that none of them can route here. That removal is
reverted and out of scope for this PR: it changes three providers a reader did not
ask about, and each deserves its own discussion. They remain as they were on
`main` — `grok-web` and `perplexity-web` catalogued with a probe but no executor,
and `gemini-web` catalogued only. Whether that is the right end state is an open
question, not something this PR settles.

`deepseek-web` is displayed as "DeepSeek Web" with no product claim, and its
icon is the same artwork the `deepseek` provider uses.

### fix(deepseek-web): a cut-off tool turn crashed the server instead of answering

The buffered tool path reported an aborted upstream stream by passing a nil
writer to `deepSeekReportStreamAbort`, which dereferenced it. Every tool turn the
upstream cut off therefore panicked, and the process died mid-request — the
client saw a request that never came back rather than a 502, which reads to an
agent as a hang.

The streaming path was unaffected: it passes the live writer it had been
streaming through, and only the buffered path ever passed nil. That is why plain
chats kept working while any request declaring `tools[]` was fatal, and why the
failure looked intermittent.

`deepSeekReportStreamAbort` now treats a nil writer as "nothing has been written
yet" and returns the cause, and both tool-path aborts return an ordinary upstream
failure — which is correct there, since the path buffers and can still answer
with a status. The nil guard is kept because the shared helper is the one place
that can be handed a nil from any caller.

`deepseek_web_loop_test.go` pins all three: a cut-off tool turn returns an error
and writes no SSE bytes, the shared abort helper survives a nil writer, and a body
that fails mid-read does not hand back a partial accumulator. Verified against the
pre-fix code — reintroducing it reproduces the same nil dereference.

### fix(deepseek-web): a streaming client got one JSON document and waited forever

The buffered tool path wrote a single `chat.completion` document whatever the
client had asked for. A client that sent `stream:true` therefore received a body
with no `data:` frames and no terminator, and waited for an end that was never
going to arrive — which is what a request that never returns looks like from the
other end.

Tool arguments are still buffered, because a truncated JSON object is
indistinguishable from prose and the native API does not stream them either. What
was wrong was the framing, not the buffering: the buffered turn is now emitted as
SSE for a streaming client, as one delta followed by a finish chunk and `[DONE]`.
A client reassembles deltas in order, so re-fragmenting the text would only cost
allocations. Non-streaming requests keep the single-document shape.

`deepseek_web_loop_test.go` pins the contract in both directions and fails against
the pre-fix code: a streaming tool request must produce `chat.completion.chunk`
frames, a `text/event-stream` content type and a `[DONE]`, and a non-streaming one
must not.

### fix(deepseek-web): read the DSML tool calls the site models actually emit

Asked for a tool call, the models answer in DSML — the site's own element
syntax — rather than the JSON object the prompt asks for:

```
<｜｜DSML｜｜ invoke name="web_search">
<｜｜DSML｜｜ parameter name="query" string="true">cuaca Jakarta hari ini<｜｜DSML｜｜ parameter>
<｜｜DSML｜｜ invoke>
```

The extractor only looked for JSON, so the whole block reached the client as
literal content. The client had no call to run, saw markup ending in
`parameter>`, and read the turn as a refusal — which is what produced a tool call
printed instead of performed.

Both dialects are now read, DSML first because it is unambiguous where a
JSON-shaped object sitting in prose has to be guessed at. Each invoke becomes an
ordinary call; parameter bodies become JSON string values, and `string="day"`-style
attributes stay attributes, so they never land where the tool expects a value. An
invoke naming a tool the client never declared is still rejected and still
reaches the client as content — but stripped of the markup, since a wall of
`parameter>` tags is not something to show a user.

The markers use a fullwidth bar (U+FF5C) and are written with padding, so the
matchers key on the text between the bars rather than a fixed spelling.

`deepseek_web_dsml_test.go` drives the exact block a live answer produced, and
pins: verbatim parameter text including quotes, undeclared names rejected, a
truncated block parsed as nothing, prose left alone, and the JSON path still
working when no markers are present.

### fix(deepseek-web): read Claude-shaped tool declarations

A tool declaration arrives in one of two spellings. OpenAI wraps it —
`{"type":"function","function":{...}}`. The Claude/Anthropic form puts `name`,
`description` and `input_schema` at the top level with no wrapper, and clients
send it just as readily.

Only the first was parsed. A Claude-shaped declaration produced a spec with an
empty name, which the prompt builder skips — so the model was told about no tools
at all, fell back on its native DSML, and the markup reached the client as if it
were the answer. The client had no call to run and no question answered, which is
what a printed tool call looks like from its side.

Both spellings are folded into one shape at unmarshal time, so nothing downstream
has to know which arrived. A declaration carrying both resolves to the nested
one, and a DSML call now matches against a flat declaration as readily as a
wrapped one.

`deepseek_web_toolshape_test.go` pins: the Claude shape reaching the rendered
prompt, a DSML call matching it, the OpenAI shape unaffected, a mixed list, both
spellings at once preferring the nested one, and a malformed declaration failing
loudly rather than parsing into something empty.

### fix(deepseek-web): match the closing tags the model actually writes

The first version of the DSML parser matched closing tags against the opener
spelled without a slash — `<｜｜DSML｜｜ invoke>` — which is how the notation reads
when it is described in prose. A live answer closes them as XML does:

```
<｜｜DSML｜｜ calls>
<｜｜DSML｜｜ invoke name="read">
<｜｜DSML｜｜ parameter name="path" string="true">https://wttr.in/Jakarta</｜｜DSML｜｜ parameter>
</｜｜DSML｜｜ invoke>
</｜｜DSML｜｜ calls>
```

`strings.Index` therefore returned -1 on every block, nothing was parsed, and each
DSML call went to the client as literal markup — the failure the DSML support was
written to prevent. The test fixture had been hand-written from the same wrong
reading, so it agreed with the parser and passed.

Tags are now matched by pattern: the bar may be full-width or ASCII, the marker
may be padded, and a closing tag may or may not carry its slash. Opening tags
cannot match a terminator because theirs is followed by `/`, and the stripper
searches for the terminator from after the opening element so it cannot truncate a
block at its own first tag.

`deepseek_web_dsml_test.go` now drives a block copied byte for byte out of a live
answer rather than reconstructed, and additionally pins both close spellings, the
ASCII pipe, and that both invokes in the live block are recovered in order. The
suite fails against the previous parser.

### fix(deepseek-web): stop replaying leaked markup back into the conversation

A leaked DSML block is not only shown to the user, it is fed back to the model.
The client returns the assistant turn in the next request, and the transcript
renderer replayed its content verbatim, so the model read its own unparsed
`<｜｜DSML｜｜ invoke>` block as the way this conversation calls tools and answered
with another one. Every turn leaked again, and each round described the previous
one: the calls the user saw executed (`Read Jakarta`, `Read Tokyo`) came a turn
behind the answer that followed them.

An assistant turn is now repaired before it is replayed. A DSML block is stripped,
and a JSON object is dropped only when it is shaped like a call — `name` plus
`arguments` or `parameters` — so prose that merely contains JSON is left alone.
When nothing but markup remains, the turn is recorded as `[called read]` rather
than dropped, because an empty assistant turn reads to the model as never having
called anything, which is the retry loop fixed earlier.

The tool prompt also states that native markup is not transmitted and that a reply
made of `invoke`/`parameter`/`calls` elements is discarded unread, which is what
the upstream OmniRoute prompt does.

`deepseek_web_transcript_test.go` drives the transcript with the assistant turn
copied byte for byte out of the leaking request, and fails against the previous
renderer.

### fix(deepseek-web): stop leaking the DSML wrapper and run every call in an answer

A model wraps its invokes in a `.calls` element, and the extractor consumed only
the `invoke`. The wrapper was left over, and the text around a call is returned to
the client as the assistant's preamble — correct for prose that a model writes
before its call, wrong for markup that happened to enclose it. So every tool turn
was answered with `content: "<｜｜DSML｜｜ calls></｜｜DSML｜｜ calls>"` alongside a
perfectly good `tool_calls` array. The user saw an empty pair of tags under their
own question on every round.

Two changes:

- `deepSeekStripDSML` removes a whole element, from its opening tag to its
  terminator, for `calls`, `invoke` and `parameter`. Removing only the tags would
  leave the parameter bodies behind as prose the model never wrote.
- `deepSeekSurroundingText` strips markup from what it returns, so a wrapper
  cannot reach the client however it is spelled. It now takes every raw call at
  once, since the text between several calls is only correct when they are removed
  together.

An answer is also no longer assumed to hold one call. A model asked about two
cities writes two invokes in a single reply; the gateway returned the first and
the client paid a round trip for each, with the remaining call left in the answer
as markup. `deepSeekExtractToolCalls` returns all of them, de-duplicated by name
and arguments so a repeated invoke is not executed twice, and the streaming path
puts them in one frame so the client runs them together.

`go test ./...` green; 6 live turns through `combo-wombo` returned zero leaked
markup, and the exact polluted history the user reported now answers in prose.

### fix(deepseek-web): read every call in an answer, in both dialects

The plural rule was applied to DSML only. On the JSON path a model asked about two
cities writes two call objects, and the gateway returned the first while the second
stayed in the answer as text — so the assistant's content carried a bare
`{"name": "shell", "arguments": …}` fragment that the user read as a leak. Both
paths now return every call they find, de-duplicated by name and arguments.

`deepSeekFindJSONObjects` replaces the single-object finder: it returns each
balanced top-level object and skips past it, so a nested object inside a call's
arguments is part of its parent rather than a call of its own.

The tool prompt also states that the declared tools are installed and runnable and
that a command tool executes on a machine with network access. Without that, a
model read its own tool list as a description of what it lacked and refused with
"I cannot check live weather, I have no such capability" — or "I have no internet
access" — while the `shell` tool it needed sat in the same prompt.

`go test ./...` green. Ten live turns through `combo-wombo` with omp's own tool
set: 10/10 returned `tool_calls`, 0 leaked markup, 0 refusals.

### fix(deepseek-web): require answers to be grounded in the tool results

A failed command followed by a confident table. In the stored turn
`1792065306584` the tool result was `ls: cannot access '/tmp/jkt.json': No such
file or directory` and the model answered with a 959-character comparison of both
cities — temperatures, humidity, wind, UV, sunrise — presented as though wttr.in
had returned them. Nothing in the client can separate those numbers from real
ones.

The tool prompt now states that a `Tool result` line is the only source of fact,
that no figure may be stated which is not in one of those lines, and that an
empty, failed or not-found result must be reported as such rather than replaced
from memory.

Verified against the polluted history itself: five live turns with the failed
result in context, four now detect the failure and re-fetch instead of inventing
data. One still answered from memory — a prompt rule is a strong nudge, not a
guarantee, which is why the transcript repair above matters too.

### fix(deepseek-web): number streamed tool calls so a client can tell them apart

`tool_calls[].index` was missing. The OpenAI streaming protocol numbers every
parallel call in `tool_calls[].index` and clients key their accumulator by that
number, so a client receiving two calls with no index had nothing to separate them
with and concatenated both argument objects:

```
Validation failed for tool "bash": Tool call arguments are not valid JSON.
Parse Error: Unexpected trailing characters at position 2
Raw JSON: {}{}
```

The two calls were both correct; they were simply indistinguishable. The stream
now emits one frame per call, each carrying `index` 0..n-1, which is the shape
every other executor in this repository already uses
(`internal/proxy/executor/stream.go`, tool-call emission). The model's prose still
travels with the first frame, and the turn keeps its `finish_reason: tool_calls`
and `[DONE]` terminator.

`deepseek_web_stream_test.go` parses the emitted frames the way a streaming client
does and fails against the previous single-frame writer.

### fix(deepseek-web): mint unique tool-call ids

The call ids came from `time.Now().UnixNano() % 1_000_000`. Two calls minted in
the same turn received the same id **every time**, because the clock does not
advance between them — on Windows it moves in steps of hundreds of microseconds:

```
first  = "call_shell_969000"
second = "call_shell_969000"
```

A client that groups parallel calls by `id` — the natural reading of a field called
`id` — merged the two into one, and the concatenated argument objects were rejected
before either command ran:

```
Validation failed for tool "bash": Tool call arguments are not valid JSON.
Parse Error: Unexpected trailing characters at position 2
Raw JSON: {}{}
```

The id is now minted from a process-wide counter, which cannot repeat. The tool name
is still part of the id so a log line names what was called.

Live, eight turns that each produced two parallel `shell` calls: distinct ids
(`call_shell_15`, `call_shell_16`), distinct `index`, both argument objects valid.
The previous code produced one id twice and the client merged them.

`deepseek_web_callid_test.go` fails against the clock-derived id.
