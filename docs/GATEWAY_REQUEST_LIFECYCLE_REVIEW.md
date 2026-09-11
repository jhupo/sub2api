# Gateway Request Lifecycle Review

## Ownership

- Account admission returns the terminally checked account snapshot. Forwarding,
  identity projection and billing use that returned account, not the earlier
  selection pointer.
- Before enqueuing usage, handlers capture public/requested model, channel usage
  fields, pricing time, endpoints and client metadata. Worker callbacks must not
  retain a pooled Gin context. An AST regression checks the gateway callbacks.
- Chat and Messages bridges derive their transport session before the final
  Codex fingerprint projection. Session/full convergence remains authoritative
  for header and body replicas. Existing account/key isolation is unchanged.

## WebSocket Turns

Each new business turn waits for the previous turn's usage task to finish,
refreshes the API key and its user/group permissions from the repository, and
checks billing eligibility and channel model restrictions. Transport retries of
that same admitted turn do not consume RPM again. A failed usage task prevents
the next turn from being forwarded. Waiting does not retain turn concurrency
slots; the current user concurrency limit is reapplied when slots are acquired.

Key revocation, expired/quota-exhausted keys, lost group/IP permissions and
exhausted monetary windows reject the next turn. Group, platform or payment
identity changes require a reconnect. Repository refresh includes allowed groups
so exclusive/public-whitelist users are not falsely rejected.
Local permission, billing, settlement and channel-model denials are request-scoped;
they must not lower the health score of a shared upstream account.

WS ingress now owns funding per business turn in pooled, passthrough and HTTP
bridge modes. Each response.create is authorized after payload reconstruction
and policy, before upstream sending. An internal ws-turn UUID identifies both
the hold and settlement; it is not sent upstream or derived from client IDs.
Transport retries extend the same hold without refunding/reacquiring it. The
handler transfers ownership to the mandatory usage task before returning; queue
overflow falls back to synchronous settlement. The next turn waits for it.

Wallet authorization follows the existing switch. Subscription reservations
remain mandatory. Subsequent turns reload the subscription and maintain its
day/week/month windows through the existing subscription service. Input may not
be added through unmetered Realtime/session events on a preauthorized Responses
connection; supported input belongs to response.create.

Semantic text/tool/reasoning deltas extend the output window. Terminal measured
output also covers invisible reasoning. Repeated done/completed content and
response.created envelopes are not charged as additional deltas. Terminal or
partial measured usage settles at the normal pricing/rate; an unmeasured
interruption releases its hold instead of charging the estimate. Passthrough
keeps unfinished-turn usage separate from already-settled connection totals.

Continuation input includes the previous response's measured input/output
bound, scoped to the API key and group. Only numeric bounds are stored in Redis;
the connection has a bounded local map. If a previous_response_id has no trusted
bound, a preauthorized request must reconnect with full input and no anchor.
This can occur for anchors created before upgrading, expired cache entries or
a different API key. No zero-cost or guessed-input fallback is used. This is
gateway billing metadata, not OpenAI's prompt cache, and does not alter cache keys.

Authentication cache schema 24 lives under the fork-owned jhupo:apikey:auth:
namespace. A valid payment identity is required even when the schema number
matches. Official v0.2.1 schema 23 cannot silently become wallet-funded traffic.
Old Redis entries are left to expire; quota and balance namespaces are unchanged.

An uncorrelated bare error on a reused upstream socket is not attributed to the
active user/model. If the active response ID is already established, the socket
is retired and only its matching `response.failed` can supply terminal usage.
The drain has one five-second deadline, preserved across reads and repeated
errors. Stale/mismatched response IDs, malformed frames and ambiguous turn
boundaries fail closed. This is an error-drain deadline, not a first-token limit.

For an unowned error before a response starts, pooled ingress can retry the exact
request once on a fresh socket within the existing retry budget. It does not use
the unowned error to delete an anchor. Strict store=false turns require enabled
anchor recovery and replay context; tool-output continuations fail closed. A
fresh socket's own `previous_response_not_found` may then invoke existing
one-time context rebuilding. If the fresh request succeeds, its original anchor
and cache key remain unchanged. No already-visible turn is replayed.

## Recovery and Charging

| Durable state | Recovery |
| --- | --- |
| Prepared or authorized hold, no actual settlement | Release/refund the hold; do not manufacture usage from the estimate. |
| Wallet finalization pending | Resume the persisted actual amount and fingerprint. |
| Subscription finalizing | Capture the persisted actual amount and fingerprint. |
| Terminal record | Existing idempotent repository transitions remain authoritative. |

Releasing an unmeasured hold logs `billing.authorized_hold_released_without_usage`.
It does not charge API-key quotas or upstream account usage from a guess. This is
not an exactly-once durable usage outbox: process death before `repo.Apply` can
still lose unpersisted usage and platform revenue. Recovery favors not charging
an unproven estimate. Once actual settlement is persisted, existing recovery
continues it. Database schema, prices and subscription reset windows are unchanged.

## Scheduling Simulation

`TestOpenAISchedulerSimulation40Users6Accounts5Slots` exercises production
selection, admission, bounded pool waiting and sticky migration using synchronized
in-memory repository/cache doubles, with both scheduler modes enabled/disabled.

- Forty users contend for six OAuth accounts with a hard limit of five each.
- A saturated phase must reach 30 occupied slots and at least 10 queued requests.
- Seeded faults disable two accounts, rate-limit two accounts and inject
  request-scoped overloaded failures. Independent session pressure changes
  subsequent atomic admission limits; it never raises the configured ceiling.
- All-account unavailability rejects requests. Restored accounts accept requests.
- Successful migration publishes the next sticky account; sequential follow-ups
  remain there with spare capacity.
- A migration's post-selection queue remains on its claimed target, as in the
  real handler. The simulation must not reenter pool spillover after selection
  has already coordinated that target; target ownership is asserted before work.
- Forty blocked requests are canceled together; all wait entries and acquired
  slots must be released, including duplicate cleanup calls.

This is not live OpenAI traffic, a throughput benchmark, or a full HTTP/Redis/
PostgreSQL production load test. Separate repository tests exercise real admission
Lua scripts against local miniredis, and handler/relay tests use local WebSockets.

## Validation — 2026-09-11

- Affected-chain regression: 867 top-level tests passed across handler (120),
  service (691), WS relay (39) and repository (17), using `go test -tags unit`
  with name filters. No whole-project suite was run.
- Supplemental scheduling configuration and billing repository checks: 32
  top-level tests passed (2 configuration, 30 repository).
- The corrected 40-user simulation passed ten repetitions in both scheduler
  modes (20 scenarios). Four WS recovery/ownership tests also passed ten repeats.
- A final run with explicit disabled/rate-limited-account assertions passed both
  modes. Per scenario: 160 main successes, 22 injected overloaded retries, 40
  all-unavailable rejections, 40 canceled waiters and 40 sticky follow-ups.
  Total peak concurrency was 30; every account peaked at exactly 5. Slot
  acquisitions/releases were 252/252, with no remaining slots or wait entries.
- A prior repetition exposed an invalid simulation path: the driver reentered
  pool spillover after claiming a migration target. The driver now matches the
  production handler's same-target wait; the production migration guard was not
  relaxed to make the test pass.
- Formatting and `git diff --check` passed. Windows with CGO disabled did not
  run the race detector. PostgreSQL-backed integration tests and live upstream
  traffic were not run; passing simulations do not guarantee upstream cache hits.

Generated test evidence is retained under ignored `backend/output/` as
`lifecycle-review-final-tests.jsonl`, `lifecycle-review-billing-config-tests.jsonl`,
`lifecycle-review-stability-corrected-tests.jsonl` and
`lifecycle-review-final-simulation-tests.jsonl`. Earlier failed-run logs are also
retained there for comparison. No server, database schema, commit or tag was changed
by this lifecycle-fix task.
