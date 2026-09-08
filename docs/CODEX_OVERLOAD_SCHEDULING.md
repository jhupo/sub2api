# Codex Overload Scheduling

## Scope and Controls

The gateway's Codex adaptive scheduling switch is off by default. It applies to
OpenAI OAuth, Setup Token, and official `https://api.openai.com` API-key accounts.
Custom API-key relay endpoints retain their existing scheduling policy.

An explicit upstream capacity-shed error is the overload signal. Slow first
output, quota exhaustion, unsupported models, and authentication errors are not
interchangeable with overload. The old normal/high-effort first-output deadline
settings and their abort logic have been removed. The existing idle-stream
timeout and post-timeout account action settings remain separate.

Native compaction and legacy compact requests use the same account admission
limit. Legacy compact model mappings are resolved before reading model pressure.
Slow compaction does not by itself cause adaptive failover.

## Admission and Retries

- Account concurrency remains the configured upper limit. Redis atomically reads
  independent-session pressure and acquires a slot in the existing account slot
  namespace, shared across models and HTTP/WS traffic.
- Two or more independent failing sessions reduce the limit to
  `max(1, ceil(configured_concurrency / session_pressure))`. Pressure expires after
  90 seconds; a successful session clears its own signal. Repeated failures of
  one session do not count as multiple independent sessions.
- A minimum slot is not permission to bypass authentication, configured quota
  pauses, group membership, endpoint capability, or other admission gates.
- Queued acquisition re-reads current account policy. A cache-affine account gets
  up to 750 ms of short waiting before spare capacity is considered. Remaining
  queue plans are bounded to at most 3 seconds.
- Eligible OpenAI requests have at most three actual upstream execution attempts
  across transport retries and account switching, even with adaptive scheduling
  disabled. Each new client WS turn gets a fresh budget; an internal reconnection
  of the same turn does not. The optional `generate=false` WS prewarm shares its
  execution attempt rather than creating a separate model-generation budget.
- Explicit overload permits at most one same-account retry, subject to the account
  retry setting and remaining request budget. Retry delay honors `Retry-After`
  with a 500 ms floor and up to 250 ms jitter. Request cancellation stops waiting.

## Session Affinity

Temporary overflow to a spare account does not replace an existing durable
session binding. Failover migration uses a Redis lease with a unique version:

1. Claim a shared target for the session and model.
2. Concurrent requests follow that target while preserving response-ID ownership.
3. Refresh the 90-second lease during active execution.
4. Commit the durable binding only after a successful target response, using an
   atomic source/target/version check.

Late source completions, failed targets, stale commits, and concurrent requests
for another model cannot overwrite a newer committed target. Requests carrying
`previous_response_id` do not rebind ordinary session affinity. A failed migration
without success leaves the original durable binding intact; its lease expires.
Migration-store failure is surfaced rather than silently selecting random owners.

This reduces avoidable cache churn; it cannot transfer provider prompt caches
between OAuth accounts or guarantee cache hits after compaction.

## Quota and Billing

OAuth usage is provider-observed data, not an estimate from local billing:

- Capture HTTP quota headers when the response arrives, including failed attempts.
- Read WS quota headers only on a new handshake, never as a fresh observation when
  a pooled connection is reused or a long-running turn finishes.
- Coalesce busy account observations while retaining the final pending snapshot.
  PostgreSQL publication atomically rejects older observation timestamps.
- Ordinary account edits preserve the locked database quota observations instead
  of accepting an older form snapshot. Account duplication drops those observations.
- Manual/automatic usage refresh uses GET `/backend-api/wham/usage`, not a synthetic
  model request. Concurrent refresh callers share a bounded query; canceling one
  caller does not cancel the others. Failed refresh preserves stale timestamps.
- Quota-reset refresh does not join a query that started before reset completion.
- The separately configured overdraft detector still sends bounded real probes.
  Those probes share account slots and do not bypass unrelated credential blocks.

Failure diagnostics distinguish reported upstream usage from unknown usage. They
do not fabricate zero usage, alter OAuth percentages, or create additional customer
charges. Existing preauthorization and final settlement remain responsible for
customer billing. Failed or repeated provider executions may still consume OAuth
quota; this design does not promise free retries or a complete provider-cost ledger.

## Streaming Commit Boundary

Native Responses waits for upstream response headers before writing SSE heartbeat
bytes so `x-codex-turn-state` is not lost. A heartbeat that commits those headers
also commits that attempt: later failures are delivered to the client, not replayed
on another account. Heartbeats and protocol metadata do not count as visible TTFT.
HTTP requests forwarded through upstream WS obey the same rule.

Turn-state provenance is keyed by the exact token hash and client session, so
out-of-order completions do not overwrite another token's account origin.

There is an unavoidable boundary: before upstream headers arrive, native Responses
cannot both send an early HTTP heartbeat and preserve an unknown response header.
A proxy's pre-header deadline therefore remains an infrastructure concern. Legacy
body-signal compact keeps its existing pre-header heartbeat behavior.

## Data and Validation

No database schema migration or data deletion is required. Observation markers are
additive account `extra` fields; migration leases are expiring Redis keys. Existing
balances, subscriptions, usage records, and concurrency keys are not reset.

Targeted tests cover concurrent multi-user admission, cross-model slot sharing,
pressure changes while queued, migration CAS, stale quota publication, shared quota
queries, SSE/WS header commitment, retry budgets, and preauthorization. Repository
concurrency and migration tests can use an isolated local Redis through
`SUB2API_TEST_REDIS_ADDR=127.0.0.1:<port>`; they do not flush the database. PostgreSQL
observation ordering is checked with SQL expectations, not a live PostgreSQL test.

Run relevant suites with `-race` on a supported Go platform. Use `-parallel 1` when
including existing tests that change Gin's process-global mode; concurrency inside
individual scheduling tests remains enabled. Full release testing is still required
before a production tag.
