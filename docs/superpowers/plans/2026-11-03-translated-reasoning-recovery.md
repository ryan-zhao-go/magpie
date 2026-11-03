
# Translated Reasoning Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Recover translated requests from unreadable carried reasoning once, and prevent non-streaming clients from receiving SSE keepalives.

**Architecture:** Reuse the existing carried-reasoning refusal bookkeeping in the group candidate loop, widening only its error predicate to the already-known Responses `Item ... not found` shape. Make `holdWriter.keepAlive` enforce the original client streaming contract at its single output boundary, so every caller is safe without duplicating checks.

**Tech Stack:** Go, `net/http`, `httptest`, Magpie gateway protocol translators and fallback writer.

## Global Constraints

- Do not change routing-group endpoint order or session affinity.
- A translated same-candidate recovery is allowed at most once and only when `refuseCarried` finds removable carried reasoning.
- Existing fallback behavior handles an unassociated error or a failed cleaned retry.
- `stream: false` must retain JSON framing and the real HTTP status.
- `stream: true` keepalive behavior and answer-timeout semantics remain unchanged.
- Do not modify `.github/workflows/fork-release.yml` or the dev auto-update configuration.
- Run each new regression against the pre-fix implementation and observe a behavioral failure before changing production code.

---

### Task 1: Recover translated `Item ... not found` reasoning errors

**Files:**
- Modify: `internal/gateway/codex_backend.go:1682-1707`
- Modify: `internal/gateway/codex_backend.go:3548-3553`
- Modify: `internal/gateway/gateway.go:3688-3701`
- Test: `internal/gateway/foreign_carry_test.go`

**Interfaces:**
- Consumes: `unreadableItem`, `foreignReasoning`, `refuseCarried(key, who string, from provider.Protocol, body []byte) bool`.
- Produces: `itemNotFoundError(status int, body []byte) bool`, shared by translated recovery and the existing recorder wrapper.

- [ ] **Step 1: Add an SSE refusal mode and a translated regression test**

Extend `carrySealer` with an `itemNotFound bool` test-only field. In `ServeHTTP`, when a foreign reasoning item is present and this field is true, return HTTP 200 with `Content-Type: text/event-stream` and this event:

```go
io.WriteString(w, sse(`event: error`+"\n"+`data: {"type":"error","error":{"type":"invalid_request_error","message":"Item with id 'rs_missing' not found."}}`))
return
```

Add `TestUnreadableCarriedReasoningAskedAgainWithout` using an Anthropic
`carryClient`. Save two Responses providers backed by
`carrySealer{name: "grok"}` and
`carrySealer{name: "codex", itemNotFound: true}`, then save an ordered group
containing `grok/m` followed by `codex/m`. Make one successful client turn so
the tool-call ID carries Grok's sealed reasoning. Set Grok's `down` field to
`http.StatusPaymentRequired`, make the next client turn, and then assert:

```go
if code, body := c.ask(t, s); code != http.StatusOK {
    t.Fatalf("after the switch: %d %s", code, body)
}
seals, _ := codex.taken()
if fmt.Sprint(seals) != "[[grok] []]" {
    t.Fatalf("Codex was sent reasoning %v, want Grok's, then none", seals)
}
```

This verifies Anthropic translated to Responses, an HTTP 200 event stream containing the item error, and one cleaned same-provider retry.

- [ ] **Step 2: Run the focused test and observe RED**

Run:

```bash
go test ./internal/gateway -run '^TestUnreadableCarriedReasoningAskedAgainWithout$' -count=1
```

Expected: FAIL because the second provider is asked once and its item-not-found error reaches the client instead of triggering a cleaned retry.

- [ ] **Step 3: Extract the exact item-not-found predicate**

Add beside `unreadableItem` in `codex_backend.go`:

```go
func itemNotFoundError(status int, body []byte) bool {
    return status == http.StatusBadRequest && unreadableItem.Match(body) &&
        bytes.Contains(bytes.ToLower(body), []byte("not found"))
}
```

Keep the recorder-specific wrapper but delegate to the shared predicate:

```go
func itemNotFound(rec *recorder) bool {
    return itemNotFoundError(rec.status, rec.body.Bytes())
}
```

- [ ] **Step 4: Widen only the translated carried-reasoning guard**

In `gateway.go`, retain every existing retry guard and change only its error predicate:

```go
errBody := hw.errBody()
if resealed == 0 && from != provider.Responses && !hw.passing && hw.code() >= 400 &&
    (foreignReasoning.Match(errBody) || itemNotFoundError(hw.code(), errBody)) {
    resealed = 2
    if refuseCarried(stuck, c.vendor(), from, attemptBody) {
        try.Fail = failForeign
        s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
        i--
        continue
    }
}
```

Do not change `refuseCarried`: its `false` result remains the association gate and prevents a blind retry.

- [ ] **Step 5: Run focused reasoning tests and observe GREEN**

Run:

```bash
go test ./internal/gateway -run '^(TestUnreadableCarriedReasoningAskedAgainWithout|TestForeignCarriedReasoningAskedAgainWithout|TestForeignReasoningWords|TestBareReasoningLeftOutWhereRefused|TestRefusesInput)$' -count=1
```

Expected: PASS. The new test must observe exactly two requests at the refusing provider; existing foreign and bare-reasoning behavior remains intact.

---

### Task 2: Gate held-response keepalives on the client stream flag

**Files:**
- Modify: `internal/gateway/fallback.go:2769-2782`
- Test: `internal/gateway/answer_timeout_test.go`
- Test: `internal/gateway/held_keepalive_test.go`

**Interfaces:**
- Consumes: `holdWriter.streams`, which records whether the original client requested a stream.
- Produces: `holdWriter.keepAlive()` with a streaming-only precondition enforced at the output boundary.

- [ ] **Step 1: Add non-streaming and streaming keepalive regression tests**

In `answer_timeout_test.go`, add a delayed-error handler that waits longer than the test keepalive threshold and then returns a JSON 400:

```go
type delayedJSONError struct {
    wait time.Duration
}

func (d delayedJSONError) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    io.ReadAll(r.Body)
    select {
    case <-r.Context().Done():
        return
    case <-time.After(d.wait):
    }
    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusBadRequest)
    io.WriteString(w, `{"error":{"message":"bad input","type":"invalid_request_error"}}`)
}
```

Add `TestGroupAnswerTimeoutNonStreamDoesNotKeepAlive`. Temporarily set `keepHeldAfter = 20 * time.Millisecond` and `watchEvery = 10 * time.Millisecond`, configure a two-member ordered group with `AnswerTimeout: 1`, post a request with `"stream":false`, and assert:

```go
if rec.Code != http.StatusBadRequest {
    t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
}
if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
    t.Fatalf("content type %q, want JSON", got)
}
if strings.Contains(rec.Body.String(), ": keepalive") {
    t.Fatalf("non-stream response contains SSE keepalive: %q", rec.Body.String())
}
```

Add `TestAnswerTimeoutStreamingKeepalive`, a focused unit test that constructs a held writer with `streams=true`, `answerWait` positive, an initialized `keptAlive{proto: provider.Chat, streams: true}`, and calls `keepAlive()` after aging `alive.at`; assert status 200, `text/event-stream`, and a keepalive comment. This pins the positive streaming contract without a one-second answer-timeout wait.

- [ ] **Step 2: Run the keepalive regressions and observe RED**

Run:

```bash
go test ./internal/gateway -run '^(TestGroupAnswerTimeoutNonStreamDoesNotKeepAlive|TestAnswerTimeoutStreamingKeepalive)$' -count=1
```

Expected: the non-streaming test FAILS with status 200 and `text/event-stream`; the streaming assertion passes.

- [ ] **Step 3: Add the keepalive stream gate**

At the top of `holdWriter.keepAlive`, extend the existing guard:

```go
func (h *holdWriter) keepAlive() {
    a := h.alive
    if !h.streams || a == nil || a.proto == provider.Gemini ||
        time.Since(a.at) < min(keepaliveEvery, keepHeldEvery) {
        return
    }
```

Keep the rest of the header-copying and SSE framing logic unchanged.

- [ ] **Step 4: Run focused keepalive and answer-timeout tests and observe GREEN**

Run:

```bash
go test ./internal/gateway -run '^(TestGroupAnswerTimeoutNonStreamDoesNotKeepAlive|TestAnswerTimeoutStreamingKeepalive|TestGroupAnswerTimeoutFallsBack|TestGroupAnswerTimeoutWholeJSON|TestHeldThinkingKeepsTheAgentAlive|TestGroupLastFailureInStreamSaysItPasses)$' -count=1
```

Expected: PASS. Non-streaming keeps its JSON 400; streaming keepalives and fallback remain intact.

---

### Task 3: Document, verify, commit, and publish

**Files:**
- Modify: `docs/subsystems/gateway-routing.md`
- Verify: `.github/workflows/fork-release.yml` (read-only; no diff expected)

**Interfaces:**
- Consumes: the runtime behavior proven in Tasks 1 and 2.
- Produces: subsystem documentation and a fork branch commit that triggers the existing release workflow.

- [ ] **Step 1: Update gateway routing compatibility documentation**

Add two concise compatibility rules to the fallback/holding section:

```markdown
- Transport keepalives are emitted only when the original client requested a
  stream. A held non-streaming request stays silent so its eventual JSON body
  and HTTP status remain intact.
- For a client protocol translated to Responses, a provider that cannot find
  carried reasoning (`Item with id 'rs_...' not found`) is retried once on the
  same candidate without that carried reasoning. If there is no removable
  carry or the cleaned retry fails, normal fallback/error handling resumes.
```

- [ ] **Step 2: Format and run focused regression tests**

Run:

```bash
gofmt -w internal/gateway/codex_backend.go internal/gateway/gateway.go internal/gateway/fallback.go internal/gateway/foreign_carry_test.go internal/gateway/answer_timeout_test.go internal/gateway/held_keepalive_test.go
go test ./internal/gateway -run '^(TestUnreadableCarriedReasoningAskedAgainWithout|TestForeignCarriedReasoningAskedAgainWithout|TestBareReasoningLeftOutWhereRefused|TestRefusesInput|TestGroupAnswerTimeoutNonStreamDoesNotKeepAlive|TestAnswerTimeoutStreamingKeepalive|TestGroupAnswerTimeoutFallsBack|TestGroupAnswerTimeoutWholeJSON|TestHeldThinkingKeepsTheAgentAlive|TestGroupLastFailureInStreamSaysItPasses)$' -count=1
```

Expected: PASS.

- [ ] **Step 3: Run the full gateway suite and repository checks**

Run:

```bash
go test ./internal/gateway -count=1
go test ./... -count=1
git diff --check
git status --short
```

Expected: both Go test commands PASS, `git diff --check` prints nothing, and status lists only the intended source, tests, docs, and plan.

- [ ] **Step 4: Verify the release workflow was not modified**

Run:

```bash
git diff --name-only 3085f2760 -- .github/workflows/fork-release.yml
```

Expected: no output.

- [ ] **Step 5: Commit the implementation with verification evidence**

Run:

```bash
git add internal/gateway/codex_backend.go internal/gateway/gateway.go internal/gateway/fallback.go internal/gateway/foreign_carry_test.go internal/gateway/answer_timeout_test.go internal/gateway/held_keepalive_test.go docs/subsystems/gateway-routing.md
git commit -m "fix: recover translated reasoning failures" -m "Retry translated requests once without unreadable carried reasoning and keep non-streaming held replies out of SSE framing.

Verification: go test ./internal/gateway -count=1
Verification: go test ./... -count=1
Verification: git diff --check"
```

Expected: one implementation commit containing only the intended files.

- [ ] **Step 6: Push and verify fork publication**

Run:

```bash
git push fork feature/answer-timeout
git status --short --branch
git rev-parse HEAD
git rev-parse fork/feature/answer-timeout
```

Expected: local and fork revisions match and the worktree is clean. Then inspect the fork release workflow run for that revision and report whether publication is queued, running, or successful without changing the workflow.

