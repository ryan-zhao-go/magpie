# Routing group answer timeout

**Date:** 2026-11-02

## Problem

A routing-group member can begin a streamed reply with reasoning and then
remain queued or otherwise fail to produce anything an agent can act on. The
existing `firstToken` setting does not cover this case: by design, text,
reasoning, and tool calls all count as first content, so reasoning stops its
timer. The fallback writer can then hold a reasoning-only reply for up to four
minutes, while the client sees no answer.

This was reproduced on the dev gateway with the first members of
`group/opus-stable` and `group/astra-stable`: each emitted hidden reasoning in
about 100-170 ms but no answer text or tool call within 60 seconds. The normal
fallback order should remain in place; the first member should instead give way
when it has not produced a usable answer in the configured time.

## Goals

- Add a group-level `answerTimeout` setting, in integer seconds.
- Treat answer text or a tool call as the start of a usable answer.
- Do not treat visible or hidden reasoning, response headers, empty frames,
  keepalives, or queue events as an answer.
- If a non-final try has no answer before the timeout, cancel it and continue
  through the existing fallback path.
- Keep every byte from a timed try away from the client, so the client sees one
  complete reply from the member that ultimately answers.
- Preserve current behavior for every group that does not set the new field.

## Non-goals

- Changing the meaning of `firstToken`.
- Reordering or temporarily switching group members.
- Applying a request-wide deadline.
- Returning a timeout error when only the final candidate remains.
- Resting a provider, key, account, or model because of one answer timeout.
- Changing provider `patience`, which retries members after explicit retryable
  failures and is not a deadline for an in-flight try.

## Considered approaches

### Reinterpret `firstToken`

Make reasoning stop counting as the first token. This is the smallest schema
change, but it silently changes an established contract: `firstToken` was
specifically introduced to count text, reasoning, or a tool call. Existing
groups may rely on reasoning proving that a slow model has begun work.

### Extend `patience`

Use the group's retry budget as an in-flight deadline. This conflates two
different states. `patience` begins after all members have returned retryable
errors; it does not describe a live stream that has not answered. It also has
different default and retry-round semantics.

### Add `answerTimeout` (selected)

Keep `firstToken` intact and add a separate deadline with its own answer
predicate. The configuration is explicit, defaults off, and can be composed
with `firstToken` without changing existing installations.

## Configuration contract

`provider.Group` gains:

```go
AnswerTimeout int `json:"answerTimeout,omitempty"`
```

The value is the maximum number of seconds a non-final group try may run before
it produces answer text or begins a tool call. Saving normalizes a negative
value to zero, as `firstToken` does. Zero means disabled and retains today's
behavior. Each candidate/try receives the full configured duration; time used
by an earlier candidate is not subtracted from a later one.

For nested groups, the innermost group with an explicitly positive
`answerTimeout` controls its members. If no inner group sets one, the group
originally asked for supplies its value. This matches the inheritance model of
`firstToken` without sharing its predicate.

The routing group editor exposes the field beside **Slow to start**, labels it
**No answer**, and offers Wait, 30 s, 1 min, 2 min, and a custom duration. Its
hint spells out that reasoning does not count, all output is held, the next
member is asked, and the last member is always waited for. Every group API and
editor save round-trips the field so editing an existing group cannot silently
remove it. User-visible strings receive all of the routing plugin's supported
translations.

The intended dev configuration is `answerTimeout: 60` on `sol-stable`,
`opus-stable`, and `astra-stable`. Their member order remains unchanged.

## Runtime semantics

The candidate plan carries the effective answer timeout independently from the
effective first-token timeout. A held writer receives an `answerWait` duration
only when all of these are true:

- the request is using a routing group;
- the effective `answerTimeout` is positive;
- another candidate remains; and
- the reply is on the fallback hold path, streamed or held whole.

The final candidate is deliberately not answer-timed. It continues waiting as
it does today, because Magpie's fallback contract keeps one candidate rather
than replacing the last viable route with a gateway-generated 504.

The answer timer begins when that candidate's try begins. Queueing inside
Magpie before the upstream attempt does not consume it; once the upstream try
begins, upstream queue events and all other non-answer events do consume it.
This gives each endpoint a full opportunity while bounding the state observed
in the incident: an upstream accepted the request but never produced a usable
answer.

### Answer predicate

The predicate is protocol-aware and based on decoded stream events, not on
arbitrary non-empty bytes:

- Chat Completions: a non-empty assistant `delta.content`, or the start/delta
  of a `tool_calls`/legacy `function_call` entry.
- Responses API: output text deltas, or the addition/start/delta of a function
  or custom tool call.
- Anthropic Messages: non-empty `text_delta`, or the start of a `tool_use`
  content block (including subsequent input JSON deltas).

Reasoning/thinking events never satisfy it, whether the client would see them
or Magpie marks them hidden. Protocol framing, role-only deltas, response and
message start events, empty content, pings, SSE comments, and vendor queue or
keepalive events do not satisfy it either.

Tool-call start counts without waiting for complete arguments. At that point
the agent has received the beginning of an actionable answer and switching to
another member could duplicate an external action.

For a non-streaming upstream response, Magpie already holds the response whole.
The implementation applies the same decoded predicate before release: a whole
successful response containing text or a tool call is an answer. If a whole
response completes with reasoning but no text or call, there is no reason to
wait out the remaining timer; it is an unusable answer and advances to the next
candidate immediately. If the upstream itself remains pending, the deadline
cancels it normally.

### Holding and fallback

While `answerWait` is active, the writer holds all upstream output, including
reasoning that current refusal logic might otherwise release. It may send only
Magpie's existing transport keepalives where required to keep a streaming
client connected; no upstream event or partial answer from the timed member is
sent.

When the answer predicate becomes true, the answer timer is permanently
satisfied and the held bytes flow through using the existing release behavior.
The timer does not become active again if the remainder of that answer is slow.

If the deadline wins first, under the writer mutex Magpie marks the try as
answer-timed-out, cancels its upstream context, discards its held body, and
returns control to the existing candidate loop. The next member receives a new
full timeout. Timer and event handling must use the same synchronization so an
answer event and the deadline cannot both win.

The timed try is recorded with failure type `answer_timeout` and error text:

```text
no answer text or tool call in 60s
```

It is not a retryable provider failure and does not enter provider, key,
account, or model cooldown. It should not reuse `failSlow`, because repeated
`firstToken` failures intentionally have different resting behavior.

## Interaction with existing behavior

- `firstToken` continues to mean first text, reasoning, or tool call. If both
  settings are present, either independent timer may hand the try to the next
  candidate first.
- Existing refusal holding remains active. `answerTimeout` can extend the hold
  needed for an ordinary reasoning stream, but never beyond its own deadline
  on a non-final candidate.
- `patience` still handles completed retryable failures and retry rounds. An
  answer timeout advances to the next candidate without consuming or creating
  a provider cooldown.
- Client cancellation still wins: if the request context ends, the route is
  recorded as canceled and no fallback is attempted.
- `answerTimeout: 0`, an absent field, a non-group request, or a final group
  candidate behaves exactly as current main.

## Observability

The routing trace must show the timed try as complete, with HTTP status 504,
failure kind `answer_timeout`, elapsed time, and the timeout message. Usage
already billed by the upstream may still be recorded as a handed-on failed try,
following the current fallback accounting rules.

No new client-visible error is introduced when another member answers. The
client receives only that member's status, headers, and complete stream. The
new failure is visible in Routing and logs for diagnosis.

The owning subsystem reference, `docs/subsystems/gateway-routing.md`, must be
updated with the configuration, inheritance, answer predicate, final-member
rule, and compatibility behavior in the implementation commit.

## Test strategy

Tests use local `httptest` upstreams and injected/short test durations; they do
not depend on wall-clock 60-second sleeps or external networks. The new test is
first run against the old implementation and must fail with a behavioral
assertion, not a compile error.

Coverage includes:

- Chat Completions, Responses, and Anthropic streams whose first member emits
  reasoning only, then stalls; the second member answers and is the only reply
  visible to the client.
- Answer text before the deadline keeps the first member.
- A tool call before the deadline keeps the first member, including a call
  whose arguments arrive later.
- Visible and hidden reasoning do not satisfy the answer predicate.
- Headers, role-only/empty frames, queue events, and keepalives do not satisfy
  it.
- The final member is waited for beyond the configured timeout.
- `firstToken` and `answerTimeout` operate independently when both are set.
- The innermost explicitly configured nested group supplies the timeout.
- An omitted or zero timeout preserves current behavior.
- The group API and editor preserve and save the field, validate custom input,
  show its state on the group card, and provide every supported translation.
- A timeout does not rest the member and a following request may select it
  again.
- The deadline/event race produces exactly one outcome and passes repeated
  race-enabled testing.
- A non-streaming reasoning-only response follows the same policy where the
  fallback path can safely replace it.

## Verification and rollout

Implementation verification follows the repository acceptance rules:

1. Demonstrate the new behavioral test failing on the pre-change code.
2. Run focused gateway tests in an isolated temporary `HOME`, then the relevant
   race tests repeatedly.
3. Run formatting, vet, cross-platform vet/build, the normal build, and the
   full `-tags nogui` suite as required by `docs/code-standards.md`.
   Because the routing editor changes, run its complete Node test suite in
   Chromium and WebKit and exercise every supported locale.
4. Build the candidate binary and deploy it to the dev VM using a recoverable
   backup of the current binary and configuration.
5. Add `answerTimeout: 60` to the three stable groups without changing member
   order, restart the user service, and run the healthcheck.
6. Exercise `sol-stable`, `opus-stable`, and `astra-stable`. For a deliberately
   stalled first endpoint, confirm fallback begins near 60 seconds, the client
   sees no first-endpoint reasoning, the next endpoint supplies the complete
   reply, and Routing records `answer_timeout` without cooldown.
7. Confirm a last-member case continues beyond 60 seconds rather than returning
   a generated 504.

If deployment verification fails, restore the backed-up binary and group
configuration and restart the service. No production rollout is part of this
work.
