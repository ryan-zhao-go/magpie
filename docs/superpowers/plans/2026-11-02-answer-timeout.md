# Routing Group Answer Timeout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add group-level `answerTimeout`, handing a non-final try to the next endpoint when no answer text or tool call appears in time while preserving fallback order and final-member behavior.

**Architecture:** Persist the field on `provider.Group`, carry the innermost value on each gateway candidate, and add an answer detector beside the unchanged first-token detector. `holdWriter` keeps every upstream event private until text or a tool call, and its watcher atomically cancels an unanswered try so the existing candidate loop can continue without resting that endpoint.

**Tech Stack:** Go 1.24, `net/http`/`httptest`, Magpie gateway routing and trace packages, vanilla JavaScript routing plugin, Node test runner, Playwright Chromium/WebKit.

## Global Constraints

- `answerTimeout` is integer seconds; absent or zero is disabled, and saving normalizes negatives to zero.
- Answer text or tool-call start counts. Visible/hidden reasoning, headers, role-only or empty frames, SSE comments, keepalives, and queue events do not.
- Every upstream try gets its full duration; Magpie lane/RPM waiting before the attempt does not consume it.
- The innermost group with a positive value wins; otherwise the group originally requested supplies its value.
- Timed tries leak no upstream bytes. Existing transport keepalive comments may still be sent.
- The final candidate is never answer-timed and continues waiting.
- Timeout does not rest or cool down a provider, key, account, or model.
- `firstToken` and `patience` retain their current meanings.
- Tests use local fixtures and isolated HOME; runtime work begins with a behavioral RED and includes race verification.
- Update `docs/subsystems/gateway-routing.md` in the implementation.
- Dev changes only `sol-stable`, `opus-stable`, and `astra-stable` to 60 seconds; member order stays unchanged.

---

## File map

- `internal/provider/group.go`: stored field and normalization.
- `internal/provider/group_answer_timeout_test.go`: configuration contract.
- `internal/gateway/ttft.go`: protocol-aware answer predicate, separate from first-token timing.
- `internal/gateway/answer_timeout_kind_test.go`: streamed and whole-reply predicate tests.
- `internal/gateway/fallback.go`: candidate inheritance, held state, watcher, and timer race.
- `internal/gateway/gateway.go`: effective duration and candidate-loop outcome.
- `internal/gateway/routing.go`: distinct failure and usage error types.
- `internal/gateway/answer_timeout_test.go`: end-to-end behavior across protocols and edges.
- `internal/gui/assets/plugins/routing/client.js`: draft/save/card/editor support.
- `internal/gui/assets/plugins/routing/i18n.js`: zh, zh-TW, ja, and de strings.
- `internal/gui/tests/group-answer-timeout.test.cjs`: browser, locale, validation, and round-trip coverage.
- `docs/subsystems/gateway-routing.md`: observable behavior contract.

### Task 1: Persist and inherit the group setting

**Files:**
- Create: `internal/provider/group_answer_timeout_test.go`
- Modify: `internal/provider/group.go:168-215,1377-1400`
- Modify: `internal/gateway/fallback.go:42-80,631-690`

**Interfaces:**
- Produces: `provider.Group.AnswerTimeout int`, JSON key `answerTimeout`.
- Produces: `candidate.answerTimeout int` and `innerAnswerTimeout(provider.Member) int`.
- Consumes: `provider.Member.Via []provider.Group`, outermost first.

- [ ] **Step 1: Write the failing provider test**

Create a local provider, save group `g` first with `AnswerTimeout: -7` and then
with `AnswerTimeout: 60`, and assert normalization, reload, and JSON round-trip:

```go
func TestGroupAnswerTimeoutSaved(t *testing.T) {
    fresh(t)
    if err := Save(Provider{ID: "a", Name: "A", Key: "k", Models: []string{"m"}}); err != nil { t.Fatal(err) }
    if err := SaveGroup(Group{ID: "g", Name: "G", Members: []string{"a/m"}, AnswerTimeout: -7}); err != nil { t.Fatal(err) }
    if got := mustGroup(t, "g").AnswerTimeout; got != 0 { t.Fatalf("negative timeout = %d", got) }
    if err := SaveGroup(Group{ID: "g", Name: "G", Members: []string{"a/m"}, AnswerTimeout: 60}); err != nil { t.Fatal(err) }
    g := mustGroup(t, "g")
    raw, err := json.Marshal(g)
    if err != nil { t.Fatal(err) }
    if g.AnswerTimeout != 60 || !strings.Contains(string(raw), `"answerTimeout":60`) {
        t.Fatalf("group %+v JSON %s", g, raw)
    }
}
```

- [ ] **Step 2: Run the provider test and capture RED**

Run inside the repository's isolated-HOME wrapper:

```sh
go test -tags nogui ./internal/provider -run '^TestGroupAnswerTimeoutSaved$' -count=1
```

Expected: build failure because the field is absent. The mandatory behavioral
RED is captured before runtime implementation in Task 3.

- [ ] **Step 3: Add the field and normalization**

Add beside `FirstToken`:

```go
// AnswerTimeout is how many seconds a non-final member may run without
// answer text or a tool call before the next member is asked. Reasoning
// does not count. Zero waits; the last member is always waited for.
AnswerTimeout int `json:"answerTimeout,omitempty"`
```

In `SaveGroup`:

```go
g.FirstToken = max(g.FirstToken, 0)
g.AnswerTimeout = max(g.AnswerTimeout, 0)
```

- [ ] **Step 4: Carry nested-group inheritance**

Add `candidate.answerTimeout int`, plus:

```go
func innerAnswerTimeout(m provider.Member) int {
    for i := len(m.Via) - 1; i >= 0; i-- {
        if m.Via[i].AnswerTimeout > 0 { return m.Via[i].AnswerTimeout }
    }
    return 0
}
```

In `planLevel`, assign `l[i].answerTimeout = innerAnswerTimeout(m)` beside
`firstToken` and `patience`.

- [ ] **Step 5: Run focused regressions**

```sh
go test -tags nogui ./internal/provider -run 'TestGroupAnswerTimeoutSaved|TestSaveGroup' -count=1
go test -tags nogui ./internal/gateway -run 'TestNestedGroupFirstToken|TestGroupFirstTokenFailsOver' -count=1
```

Expected: PASS; first-token behavior is unchanged.

- [ ] **Step 6: Commit**

```sh
git add internal/provider/group.go internal/provider/group_answer_timeout_test.go internal/gateway/fallback.go
git commit -m "provider: persist routing group answer timeout"
```

Commit body records both focused commands and their results.

### Task 2: Detect answer text and tool calls independently

**Files:**
- Create: `internal/gateway/answer_timeout_kind_test.go`
- Modify: `internal/gateway/ttft.go:168-304`

**Interfaces:**
- Produces: `answerKind(ev []byte) bool` for one complete SSE event.
- Produces: `wholeAnswer(proto provider.Protocol, body []byte) bool` for held JSON.
- Consumes: existing `timingKind` and `wholeEvents`; `firstKind` remains unchanged.

- [ ] **Step 1: Write streamed-event RED tests**

Use this table so all approved distinctions are literal fixtures:

```go
tests := []struct {
    name, event string
    want        bool
}{
    {"chat text", `data: {"choices":[{"delta":{"content":"hi"}}]}`, true},
    {"chat tool", `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"ls"}}]}}]}`, true},
    {"chat mixed reasoning and tool", `data: {"choices":[{"delta":{"reasoning_content":"hm","tool_calls":[{"index":0,"function":{"name":"ls"}}]}}]}`, true},
    {"chat reasoning", `data: {"choices":[{"delta":{"reasoning_content":"hm"}}]}`, false},
    {"chat role", `data: {"choices":[{"delta":{"role":"assistant"}}]}`, false},
    {"responses text", `event: response.output_text.delta\ndata: {"type":"response.output_text.delta","delta":"hi"}`, true},
    {"responses function", `data: {"type":"response.output_item.added","item":{"type":"function_call","name":"ls"}}`, true},
    {"responses custom tool", `data: {"type":"response.output_item.added","item":{"type":"custom_tool_call","name":"apply_patch"}}`, true},
    {"responses reasoning", `data: {"type":"response.reasoning_summary_text.delta","delta":"plan"}`, false},
    {"responses queued", `data: {"type":"response.queued"}`, false},
    {"anthropic text", `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}`, true},
    {"anthropic tool", `data: {"type":"content_block_start","content_block":{"type":"tool_use","id":"t","name":"ls","input":{}}}`, true},
    {"anthropic thinking", `data: {"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hm"}}`, false},
    {"empty text", `data: {"type":"response.output_text.delta","delta":""}`, false},
    {"keepalive", `: keep-alive`, false},
}
for _, tt := range tests {
    t.Run(tt.name, func(t *testing.T) {
        if got := answerKind([]byte(tt.event)); got != tt.want {
            t.Fatalf("answerKind() = %v, want %v for %s", got, tt.want, tt.event)
        }
    })
}
```

- [ ] **Step 2: Run and capture RED**

```sh
go test -tags nogui ./internal/gateway -run '^TestAnswerKinds$' -count=1
```

Expected: build failure because `answerKind` is undefined.

- [ ] **Step 3: Implement the stream predicate**

Add after `firstKind`:

```go
func answerKind(ev []byte) bool {
    content, text, reasoning, _ := timingKind(ev)
    return text || content && !reasoning
}
```

For mixed reasoning-plus-tool events, explicitly parse the call fields so a
call wins. Do not change the current test proving reasoning counts as first
content.

- [ ] **Step 4: Add whole-response RED/GREEN coverage**

For Chat, Responses, and Anthropic JSON, assert reasoning-only is false and
non-empty text or a client tool call is true. Implement through the existing
whole-to-stream converters, so streamed and whole replies share one predicate:

```go
tests := []struct {
    name  string
    proto provider.Protocol
    body  string
    want  bool
}{
    {"chat reasoning", provider.Chat, `{"choices":[{"message":{"role":"assistant","content":null,"reasoning_content":"hm"},"finish_reason":"stop"}]}`, false},
    {"chat text", provider.Chat, `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`, true},
    {"chat tool", provider.Chat, `{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"ls","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`, true},
    {"responses reasoning", provider.Responses, `{"status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"hm"}]}]}`, false},
    {"responses text", provider.Responses, `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`, true},
    {"anthropic thinking", provider.Anthropic, `{"role":"assistant","content":[{"type":"thinking","thinking":"hm"}],"stop_reason":"end_turn"}`, false},
    {"anthropic tool", provider.Anthropic, `{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"ls","input":{}}],"stop_reason":"tool_use"}`, true},
}
```

```go
func wholeAnswer(proto provider.Protocol, body []byte) bool {
    for _, e := range wholeEvents(proto, body) {
        data, err := json.Marshal(e.data)
        if err != nil { continue }
        ev := append([]byte("event: "+e.name+"\ndata: "), data...)
        if answerKind(ev) { return true }
    }
    return false
}
```

Do not introduce parallel protocol structs just for this feature.

- [ ] **Step 5: Run adjacent classification tests**

```sh
go test -tags nogui ./internal/gateway -run 'TestAnswerKinds|TestWholeAnswerKinds|TestFirstTokenKinds|TestStreamErrorPassing' -count=1
```

Expected: PASS, including reasoning still being a first token.

- [ ] **Step 6: Commit**

```sh
git add internal/gateway/ttft.go internal/gateway/answer_timeout_kind_test.go
git commit -m "gateway: tell answers apart from reasoning"
```

Commit body records the focused test command.

### Task 3: Time held tries and fall back without resting

**Files:**
- Create: `internal/gateway/answer_timeout_test.go`
- Modify: `internal/gateway/fallback.go:2026-2160,2246-2315,2578-2665,3076-3200`
- Modify: `internal/gateway/gateway.go:2990-3020,3430-3580`
- Modify: `internal/gateway/routing.go:400-445`

**Interfaces:**
- Produces under `holdWriter.mu`: `answerWait time.Duration`, `answered bool`, `answerTimedOut bool`.
- Produces: `failAnswerTimeout = "answer_timeout"`, `answerTimeoutErrType = "answer_timeout"`.
- Consumes: Task 1 candidate field and Task 2 predicates.

- [ ] **Step 1: Write the behavioral RED**

Create an `answerStall` upstream that emits a role frame plus
`reasoning_content:"private reasoning"`, flushes, then waits for request
cancellation. Configure ordered group `a/m`, `b/m` with `AnswerTimeout: 1`;
`b` returns `chatStreamOK`. Assert HTTP 200 contains `from b`, excludes the
reasoning, and the trace's first try has `Fail == failAnswerTimeout`, no Rest,
and error suffix `no answer text or tool call in 1s`.

- [ ] **Step 2: Run against the old runtime and capture behavioral RED**

```sh
go test -tags nogui ./internal/gateway -run '^TestGroupAnswerTimeoutFallsBack$' -count=1 -timeout=8s
```

Expected: real `FAIL`/test timeout because reasoning stops first-token timing
and the first member remains active. Preserve this output for the commit body.

- [ ] **Step 3: Add held-writer answer state**

Add:

```go
answerWait     time.Duration
answered       bool
answerTimedOut bool
```

In `Write`, inspect complete held events with `answerKind`. When it becomes
true, set `answered` and use the existing `flow` path to release the complete
held prefix. While `answerWait > 0 && !answered`, `scan` may still recognize
errors/refusals and issue keepalive comments, but cannot flow reasoning or
other upstream events.

- [ ] **Step 4: Extend `watch` with an independent timer**

Create first-token and answer timers independently. When the answer deadline
wins, lock, re-check `!answered && !passing && failure == 0`, set
`answerTimedOut`, unlock, then call `stop` and return. Writes and deadline use
the same mutex, and `stop` is never called under it.

In `settle`, use `wholeAnswer`; a completed whole reply with no answer marks
the try timed out immediately rather than sleeping the remaining duration.

- [ ] **Step 5: Attach effective duration and handle the result**

Beside effective `firstToken`:

```go
answerTimeout := cmp.Or(c.answerTimeout, g.AnswerTimeout)
if isGroup && answerTimeout > 0 && !last {
    hw.answerWait = time.Duration(answerTimeout) * time.Second
}
```

Add the constants in `routing.go`. After client-cancellation handling and
before `hw.slow`, add a branch that sets 504, error text, failure/error type,
updates trace/observer, appends to `skipped`, and continues. It must set no
Rest and call none of `restSlow`, `restAfter`, or retryable-error handling.

```go
if hw.answerTimedOut {
    call.Status = http.StatusGatewayTimeout
    call.Error = fmt.Sprintf("%s: no answer text or tool call in %ds", c.p.Name, answerTimeout)
    call.Usage.ErrType = answerTimeoutErrType
    try.Status, try.Error, try.Fail = call.Status, call.Error, failAnswerTimeout
    try.Rest = nil
    watched.attempt(call, try, c.p.ID, sent, hw, capture)
    s.trace.update(tr, func(t *Route) { t.Tries[len(t.Tries)-1] = try })
    skipped = append(skipped, c.label()+": "+call.Error)
    continue
}
```

- [ ] **Step 6: Confirm Chat GREEN**

```sh
go test -tags nogui ./internal/gateway -run '^TestGroupAnswerTimeoutFallsBack$' -count=1 -timeout=8s
```

Expected: PASS in about one second, and only the backup answer is visible.

- [ ] **Step 7: Add protocol and boundary integration tests**

Add named subtests for Responses reasoning-summary stall, Anthropic thinking
stall, text in time, tool-call start with delayed arguments, last member beyond
timeout, `firstToken` plus answer timeout, nested inner value, omitted/zero
compatibility, client cancellation before the deadline, endpoint selected
again after timeout, and whole JSON reasoning/text/tool responses. Prefer
channel state assertions over narrow wall-clock bounds.

- [ ] **Step 8: Add the deadline/event race test**

Set `holdWriter.answerWait` directly to about 20 ms, release an answer at the
boundary, finish `watch`, and assert exactly one of `answered` and
`answerTimedOut`. Run:

```sh
go test -race -tags nogui ./internal/gateway -run '^TestAnswerTimeoutRace$' -count=20
```

Expected: PASS without a race report or both states true.

- [ ] **Step 9: Run focused runtime regressions**

```sh
go test -tags nogui ./internal/gateway -run 'AnswerTimeout|FirstToken|Refusal|StreamErrorPassing|QuietKeepalive' -count=1
go test -race -tags nogui ./internal/gateway -run 'AnswerTimeout|FirstToken' -count=20
```

Expected: PASS. Record elapsed last-member evidence and any skipped tests.

- [ ] **Step 10: Commit**

```sh
git add internal/gateway/answer_timeout_test.go internal/gateway/fallback.go internal/gateway/gateway.go internal/gateway/routing.go
git commit -m "gateway: fall back when a group has no answer"
```

Commit body includes exact RED output and focused/race GREEN commands.

### Task 4: Round-trip the setting through the routing UI

**Files:**
- Create: `internal/gui/tests/group-answer-timeout.test.cjs`
- Modify: `internal/gui/assets/plugins/routing/client.js:5140-5160,5340,5990-6000,6050-6065,6180-6195,6660-6730,7460-7480`
- Modify: `internal/gui/assets/plugins/routing/i18n.js`

**Interfaces:**
- Consumes: API `Group.answerTimeout` seconds.
- Produces: every group save payload preserves `answerTimeout`.
- Produces: `No answer` editor row and `Next after {n} without an answer` card tag.

- [ ] **Step 1: Write the UI RED**

Copy the structure of `group-first-token.test.cjs`. Mock both fields:

```js
const saved = { firstToken: 0, answerTimeout: 0 };
```

Assert default Wait; 1 min posts 60; reopen shows 1 min; Wait posts zero;
custom 2.5 min posts 150; card tag appears; routing/manual-pick saves preserve
the value; all five languages work at 1100 and 420 in both engines.

- [ ] **Step 2: Run both-engine RED**

```sh
BROWSER=chromium node --test internal/gui/tests/group-answer-timeout.test.cjs
BROWSER=webkit node --test internal/gui/tests/group-answer-timeout.test.cjs
```

Expected: FAIL because the row and POST field are absent.

- [ ] **Step 3: Add draft, every save payload, tag, and editor row**

Add `answerTimeout` to the draft and every payload found by
`rg 'firstToken:' client.js`, including routing changes and manual picks. Reuse
the duration options/custom-minute validation. Use the exact hints approved in
the design: reasoning does not count; none of the member's output reaches the
agent; it does not rest; last member is always waited for.

- [ ] **Step 4: Add translations and completeness assertions**

Add these exact strings for label, tag, enabled hint, and disabled hint:

| Key | zh | zh-TW | ja | de |
| --- | --- | --- | --- | --- |
| `No answer` | `迟迟不回答` | `遲遲不回答` | `回答がないとき` | `Keine Antwort` |
| `Next after {n} without an answer` | `{n} 无回答换下一个` | `{n} 無回答換下一個` | `{n} 回答がなければ次へ` | `Nächstes nach {n} ohne Antwort` |
| `Every member is waited for, however long it reasons before answering.` | `每个成员都会一直等到真正回答，不论推理多久。` | `每個成員都會一直等到真正回答，不論推理多久。` | `回答前の推論にどれだけ時間がかかっても、すべてのメンバーを待ちます。` | `Auf jedes Mitglied wird gewartet, egal wie lange es vor der Antwort nachdenkt.` |
| enabled hint from Step 3 | `成员在 {n} 后仍没有回答文本或工具调用时，会被放弃并询问下一个。推理不算回答，且该成员的任何输出都不会到达智能体。它不会因此休息。最后一个成员始终会被等待。` | `成員在 {n} 後仍沒有回答文字或工具呼叫時，會被放棄並詢問下一個。推理不算回答，且該成員的任何輸出都不會到達智慧代理。它不會因此休息。最後一個成員始終會被等待。` | `メンバーが {n} 経っても回答テキストまたはツール呼び出しを生成しない場合、そのメンバーを中止して次を試します。推論は回答に数えず、そのメンバーの出力はエージェントに届きません。この理由では休止しません。最後のメンバーは常に待ちます。` | `Liefert ein Mitglied nach {n} weder Antworttext noch einen Werkzeugaufruf, wird es abgebrochen und das nächste versucht. Reasoning zählt nicht als Antwort und keine Ausgabe dieses Mitglieds erreicht den Agenten. Es wird deshalb nicht zurückgestellt. Auf das letzte Mitglied wird immer gewartet.` |

In the test, evaluate `I18N` and fail unless every English key exists in all
four dictionaries.

- [ ] **Step 5: Run dedicated and adjacent UI tests**

```sh
BROWSER=chromium node --test internal/gui/tests/group-answer-timeout.test.cjs
BROWSER=webkit node --test internal/gui/tests/group-answer-timeout.test.cjs
node --test internal/gui/tests/group-answer-timeout.test.cjs internal/gui/tests/group-first-token.test.cjs internal/gui/tests/group-fold.test.cjs internal/gui/tests/group-editor-align.test.cjs
```

Expected: PASS with no responsive overflow or dropped field.

- [ ] **Step 6: Commit**

```sh
git add internal/gui/assets/plugins/routing/client.js internal/gui/assets/plugins/routing/i18n.js internal/gui/tests/group-answer-timeout.test.cjs
git commit -m "routing ui: configure a group's answer timeout"
```

Commit body records both engines, locales, widths, and adjacent files.

### Task 5: Document, fully verify, and validate on dev

**Files:**
- Modify: `docs/subsystems/gateway-routing.md`
- Dev config: `/home/zhaochongzheng/.config/magpie/providers.json`
- Dev binary: `/home/zhaochongzheng/.local/bin/magpie`

**Interfaces:**
- Consumes: Tasks 1-4.
- Produces: subsystem contract plus fresh automated and dev evidence.

- [ ] **Step 1: Update subsystem documentation**

Document unchanged `firstToken`, answer-only predicate, nested inheritance,
per-try timing after upstream attempt start, full hold, no-rest fallback,
zero/default compatibility, and final-member waiting.

- [ ] **Step 2: Run formatting and diff hygiene**

```sh
gofmt -w internal/provider/group.go internal/provider/group_answer_timeout_test.go internal/gateway/ttft.go internal/gateway/answer_timeout_kind_test.go internal/gateway/fallback.go internal/gateway/gateway.go internal/gateway/routing.go internal/gateway/answer_timeout_test.go
gofmt -l internal/provider/group.go internal/provider/group_answer_timeout_test.go internal/gateway/ttft.go internal/gateway/answer_timeout_kind_test.go internal/gateway/fallback.go internal/gateway/gateway.go internal/gateway/routing.go internal/gateway/answer_timeout_test.go
git diff --check
```

Expected: the last two commands print nothing.

- [ ] **Step 3: Run full Go verification in isolated HOME**

Resolve `GOPATH`, `GOMODCACHE`, and `GOCACHE` before replacing HOME. Run the
following commands inside the documented cleanup wrapper:

```sh
verification_home=$(mktemp -d)
trap 'chmod -R u+w "$verification_home"; rm -rf "$verification_home"' EXIT
verification_gopath=$(go env GOPATH)
verification_modcache=$(go env GOMODCACHE)
verification_buildcache=$(go env GOCACHE)
export HOME="$verification_home" GOPATH="$verification_gopath" GOMODCACHE="$verification_modcache" GOCACHE="$verification_buildcache"
go vet ./... &&
GOOS=linux go vet -tags nogui ./... &&
GOOS=windows go vet ./... &&
go build &&
GOOS=linux go build -tags nogui &&
GOOS=windows go build &&
go test -tags nogui ./...
```

Expected: all exit zero. Report every skip.

- [ ] **Step 4: Run the full UI suite in both engines**

```sh
BROWSER=chromium make test-ui
BROWSER=webkit make test-ui
```

Expected: zero failures; the dedicated test covers all five locales.

- [ ] **Step 5: Commit documentation with verification record**

```sh
git add docs/subsystems/gateway-routing.md
git commit -m "docs: describe routing group answer timeout"
```

Commit body lists fresh cross-platform vet/build, full Go suite, and both UI engines.

- [ ] **Step 6: Build and stage recoverable dev deployment**

Build a Linux `nogui` binary. On dev, copy the current binary and config to
timestamped sibling backups before changing either; verify exact paths and
backup sizes with `ls -l`. Keep backups until acceptance completes.

- [ ] **Step 7: Patch three groups and restart**

Set only `sol-stable`, `opus-stable`, and `astra-stable` to
`answerTimeout: 60`, preserving membership/order. Install the binary, restart
`magpie.service`, then run `systemctl --user is-active magpie.service` and
`magpie healthcheck`. Expected: active and healthy.

- [ ] **Step 8: Exercise real endpoints and last-member behavior**

Send streamed requests to all stable groups and retain elapsed time, client
body, route trace, and service logs. A stalled first endpoint must fall back
near 60 seconds, leak no reasoning, return the next endpoint's complete answer,
record `answer_timeout` with no Rest, and remain eligible later. Use a separate
one-member test group to confirm waiting beyond 60 seconds, then cancel it;
never modify stable membership.

- [ ] **Step 9: Roll back on failed acceptance**

If health or semantics fail, restore both timestamped backups, restart, rerun
health checks, and report the failed observation plus completed rollback.

- [ ] **Step 10: Review final evidence**

```sh
git status --short --branch
git log --oneline --decorate origin/main..HEAD
git show --stat HEAD
```

Expected: clean implementation tree and only planned commits. Handoff records
behavioral RED, focused/race/full-suite, UI, dev evidence, and anything not run.
