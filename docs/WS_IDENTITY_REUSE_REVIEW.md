# WS Identity Reuse Review

## Implemented

The pool's handshake compatibility key now includes User-Agent, originator and
version independently of fingerprint convergence mode. Identity changes apply
to new work. Incompatible idle connections can be replaced using the existing
pool capacity policy; active leases and pinned continuations are not evicted.

An explicitly forced, owned continuation may retain the original connection's
handshake identity. Its existing session and capability checks remain intact.
Per-turn metadata, authorization refresh and soft routing hints do not become
hard connection-identity keys. Stored handshake compatibility uses the actual
headers passed to the dialer, after the per-dial credential factory.

## Verification

- Identity changes in off/device/session/full modes retire an incompatible idle
  connection even when the pool's capacity is one.
- Active requests are not interrupted. New identity requests wait for capacity.
- Pinned continuations survive an identity update; a mismatched thread remains
  rejected. Prewarm target identity follows the same comparison.
- Two different API-key sessions using one OAuth account in device mode can
  reuse a connection after normal terminal events and receive their own response
  IDs and usage. This is a local protocol simulation, not a live OpenAI test.

## Confirmed Late-Frame Regression

A temporary end-to-end test exercised `OpenAIGatewayService.Forward` using the
existing capture dialer and `newWSReuseBoundaryFixture`. With connection reuse
enabled (`store_disabled_conn_mode=off`), the upstream event sequence was:

1. A separate `response.failed` message for `resp_a` ends request A.
2. A second, independently buffered `response.failed` message still references
   `resp_a`.
3. `response.completed` for `resp_b` belongs to request B.

Request A used API key 11/session-a; B used API key 12/session-b. The assertion
that B's result ID was `resp_b` failed: the actual ID was `resp_a`. The successful
normal-boundary test uses the same fixture without the extra terminal message.
The diagnostic is now a permanent regression in
`openai_ws_reuse_boundary_test.go`. A failed terminal retires the socket before
the next user can acquire it.

This proves a defensive boundary gap when an upstream emits an extra independent
frame after terminating a response. It does not prove this sequence occurs in
production or that fingerprint convergence causes upstream overload. Strict
new-connection policy avoids this particular cross-session reuse scenario; it
is not a general proof that all connection continuation paths are immune.

## Response Ownership

Both pooled connections and dedicated passthrough sockets use
`openAIWSResponseBoundary`. Each response.create starts one turn. Response IDs
are checked before downstream delivery, model observation, usage accounting and
account-health hooks. Event IDs are not response IDs; response.id and response_id
are the correlation fields.

Failed, canceled, incomplete and ambiguous sockets are retired. Releasing an
unfinished pooled turn also retires the socket. Normal completed responses may
reuse their socket. Previously completed response IDs cannot be adopted by a
later turn, including when a duplicate is a separate buffered WS message.
On dedicated sockets, a bare error prohibits further turns but may still be
followed by response.failed for the current response's authoritative usage.
The history is bounded at 4096 responses by retiring the connection, not by
forgetting older IDs.

An ID-less delta is accepted within an established response. At a reused
socket's turn boundary, uncorrelated frames are rejected, not silently dropped.
ID-less errors on reused sockets are likewise treated as ambiguous instead of
changing the new request's account/model health. Legitimate connection-level
session acknowledgements on dedicated passthrough are handled separately.

This cannot identify an arbitrarily misordered ID-less delta arriving after a
new response.created. Such frames carry no verifiable origin; the implementation
relies on ordered, sequential Responses events within a successfully completed
socket. Failed or canceled sockets are never reused to make that assumption.
No timer-based drain is used as an ownership guarantee.

## Identity Boundaries

Accept-Language, credential principal, target URL and proxy are hard reuse
boundaries in addition to capabilities and configured device/session identity.
An owned continuation may retain its original UA/version handshake, but cannot
bypass these hard boundaries. Credential refresh does not itself change the
stable OAuth principal. The actual credential source of shadow accounts is used.
When no stable OAuth principal is available (including API-key connections), an
irreversible authorization digest prevents reusing a connection after credential
replacement on the same local account row. The digest is not sent upstream.

Closing an unsafe connection can require client reconnection for an existing
previous_response_id chain. It does not authorize replay after visible output,
and it does not rewrite prompt cache keys, restore upstream quota or guarantee
cache hits after account switching or compaction.
