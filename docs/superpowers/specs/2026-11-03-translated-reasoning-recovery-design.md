# Translated reasoning recovery and non-stream keepalive safety

**Date:** 2026-11-03

## Problem

Two compatibility gaps can turn a recoverable routing-group attempt into a
client-visible protocol error.

First, an Anthropic client request translated to the Responses API can carry a
provider-local reasoning item from an earlier turn. When the selected provider
cannot resolve that item, it returns an error such as:

```text
Item with id 'rs_...' not found.
```

The Responses passthrough path already removes unreadable bare reasoning and
retries. The translated path only recognizes encrypted-content and
decrypt/verify errors, so this equivalent error bypasses its carried-reasoning
recovery.

Second, a held group attempt sends Magpie's SSE keepalive after three seconds
without checking whether the original client requested streaming. A
non-streaming retry that fails after that point can therefore become HTTP 200
`text/event-stream`, hiding the upstream status and producing a malformed
response at the client.

## Goals

- Recover translated Anthropic-to-Responses requests when a provider cannot
  resolve a carried reasoning item.
- Retry the same candidate at most once after removing only the implicated
  carried reasoning.
- Preserve the existing fallback path when recovery is impossible or the retry
  fails.
- Never emit SSE keepalives for a client request with `stream: false`.
- Preserve streaming keepalives, endpoint order, session affinity, answer
  timeout semantics, and the fork's release/update workflow.

## Non-goals

- Reordering or temporarily disabling `astra-stable` members.
- Treating every upstream 400 as a reasoning compatibility error.
- Adding an unbounded or multi-round retry loop.
- Changing Responses passthrough behavior beyond shared matching needed for
  consistency.
- Changing which events count as a real answer for `answerTimeout`.
- Changing the final-candidate exemption from `answerTimeout`.

## Considered approaches

### Extend translated carried-reasoning recovery (selected)

Recognize `Item with id 'rs_*' not found` as an unreadable carried-reasoning
error in the translated path. Reuse the existing refusal bookkeeping to omit
the matching carry and retry the same candidate once. This keeps the fix local
and follows the behavior already established by the Responses passthrough
path.

### Immediately fall back to the next candidate

This avoids retry logic but throws away a healthy endpoint even when removing
one provider-local item would make the request valid. It also makes behavior
depend on whether the client request was translated or passed through.

### Build a unified reasoning recovery layer

A common recovery layer could consolidate passthrough and translated handling,
but it would require a wider refactor of mature request paths. That scope is
not justified for this incident fix.

## Runtime design

### Translated reasoning recovery

The translated group path will treat an unreadable Responses reasoning-item
error as a carried-reasoning refusal when all existing retry guards hold:

- the client protocol is not Responses;
- the response has not already been passed to the client;
- this attempt has not already resealed/retried; and
- the error matches either the existing foreign-reasoning family or the
  existing unreadable-item family.

The refusal handler must be able to associate the error with reasoning carried
by the translated request. If it cannot identify removable carried reasoning,
the request is not retried merely because the error text matched.

When association succeeds, Magpie records that carry as refused for the
conversation/provider context, rebuilds the request without it, and retries
the same candidate once. A successful retry is returned normally. A second
failure, or a first failure with no removable carry, returns to the existing
candidate/fallback logic. The existing retry guard prevents a loop.

This recovery does not change endpoint health, cooldowns, ordering, or session
affinity. It is request compatibility handling within one candidate.

### Keepalive gating

The held-response keepalive path will require the original client request to
be streaming before it writes SSE headers or bytes. Waiting for an answer,
buffering reasoning, or exceeding the three-second quiet interval is not by
itself permission to change the response protocol.

For `stream: false`, Magpie continues holding the attempt silently. When the
attempt completes, its actual JSON response and HTTP status remain available
to the retry/fallback machinery and, if ultimately returned, to the client.

For `stream: true`, current keepalive timing and framing remain unchanged.
Keepalives still do not count as answer text and do not satisfy
`answerTimeout`.

## Error handling and fallback

Reasoning recovery is bounded to one same-candidate retry. If the cleaned
request still fails, normal routing classification decides whether another
candidate is tried or the error is returned. No special fallback order is
introduced.

The non-stream keepalive fix restores the original upstream status instead of
manufacturing an HTTP 200 SSE response. It does not make an error retryable by
itself; existing routing rules continue to decide that.

## Test strategy

Tests use local `httptest` upstreams and short injected timing where needed.
Each regression test must first fail against the current implementation with a
behavioral assertion.

- A translated Anthropic-to-Responses request carrying an `rs_*` item receives
  a Responses SSE error saying the item was not found, removes the implicated
  carry, retries the same provider, and succeeds.
- If no carried reasoning can be associated with the unreadable item, Magpie
  does not retry blindly.
- If the cleaned retry also fails, only one recovery retry occurs and existing
  fallback/error behavior proceeds.
- A non-final group attempt with `answerTimeout`, `stream: false`, and a delay
  longer than the keepalive threshold never receives SSE framing; the client
  sees the eventual JSON response and real status from the selected outcome.
- The equivalent `stream: true` path continues emitting its existing
  keepalive.
- Existing answer-timeout, reasoning-refusal, bare-reasoning, fallback, and
  stream-error tests remain green.

## Documentation and delivery

The gateway routing subsystem documentation will state that transport
keepalives are streaming-client-only and that translated Responses requests
can discard unreadable carried reasoning once before normal fallback.

Implementation will be committed on `feature/answer-timeout` and pushed to
`fork/feature/answer-timeout`. The existing fork release workflow remains the
only publication mechanism; no workflow, tag scheme, endpoint order, or dev
auto-update configuration is changed by this fix.
