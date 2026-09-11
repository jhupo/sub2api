# Codex Outbound Identity

## Resolution and Lifetime

`codexIdentityInput` extracts original client identity before body conversion or
account scoping. `codexAttemptIdentity` captures the effective client identity,
credential namespace and fingerprint projection once. The snapshot contains no
bearer token or inference payload. Authentication is constructed at send/dial
time; projection does not reread settings or fetch accounts.

HTTP Responses, passthrough, Chat Completions and Messages conversion, pooled WS
and dedicated WS use this projection. Independent request builders publish a
snapshot if their forwarding entry point has not already done so. A forwarding
entry point always replaces the prior attempt, including when the next account
does not use Codex protocol. Credential-source staging also checks the selected
account ID.

The selected account owns convergence policy; its actual credential account
owns the namespace and persistent device seed. No deployment-relative row ID is
introduced into an upstream identity. Existing namespace/hash formats and seed
storage are unchanged. Missing seeds are not synthesized during requests.

## Field Ownership

| Field | Source and behavior |
| --- | --- |
| UA, originator, version | One effective-version read, existing enforcement and ForceCodexCLI policy; an explicit account UA remains an administrator choice. |
| Installation identity | Existing persistent seed or configured device ID; device-mode aliases agree across flat and embedded metadata. |
| Session and thread | Original header, with body session fallback; existing API-key and credential isolation formulas are preserved. |
| Turn identity | One projection per logical request/WS turn; same-turn transport retries reuse it. |
| Prompt cache key | Existing credential scoping and default-body-session mapping; never salted with config version, token or connection ID. |
| Accept-Language | Preserved client declaration; included in WS handshake compatibility. |
| Timezone, tools and user content | Not normalized or rewritten as device attributes. No synthetic timezone header. |
| previous_response_id and call IDs | Not part of device convergence; existing continuation/tool ownership rules remain responsible. |

Authentication uses the basic UA/originator pair without adding inference-only
headers. Models and synthetic probes use the same client-identity resolver but
only send fields required by their endpoint. The model-manifest URL version and
request header version come from the same resolved identity.

## Behavior and Cost

The raw-body path decodes only client_metadata, retaining the rest of the payload
and applying scalar cache-key updates. Scope and fingerprint projection now
share that small-object pass. No new database or Redis reads are introduced.
Application of fingerprint metadata uses a local copy, so concurrent projection
cannot mutate the published snapshot.

Normal successful WS pooling remains enabled according to existing policy.
Language or credential-principal differences may require separate connections;
failed/ambiguous sockets are retired. Pool capacity and account concurrency
limits are not increased. See `WS_IDENTITY_REUSE_REVIEW.md` for ownership limits.

Body-only sessions now produce the same derivation input on normal and
passthrough paths. This corrects the normal path's former anonymous fallback and
may produce one initial upstream cache miss for those affected sessions.
Passthrough session headers derived from a prompt cache key now use the original
key, avoiding a second isolation pass over the already scoped body value.
Credential shadows now use the real credential's seed instead of a shadow-row
device. Session/full mode gets a new turn ID for a new WS turn, not for retries.
The native Responses installation fallback also uses the credential source's
device ID captured in the snapshot, including when convergence is disabled.

No database schema, billing rate, subscription limit/reset, balance settlement,
upstream-quota estimate or default convergence setting is changed. Outbound
metadata consistency is not a claim about OpenAI risk controls or account quota.

## Focused Verification

Tests cover raw/map projection parity, body-only sessions, immutable settings,
token refresh, account failover, shadow credentials, concurrent users/accounts,
cache stability, WS turn lifetime, failed-socket retirement, stale usage and
hard handshake boundaries. These are local simulations with fake credentials;
they are not live OpenAI tests or production load tests.

Local verification on 2026-09-11:

- Service identity/WS/HTTP/probe/cancellation selection: 760 top-level tests pass.
- WS relay package: 62 top-level tests pass.
- Handler cancellation/failover/usage selection: 13 top-level tests pass.
- Four concurrent-identity and socket-reuse regressions each pass 20 repetitions.
- `git diff --check` passes. No full repository release suite was run.
- Race detection was not run: this Windows environment has CGO disabled and no
  configured C compiler. No production server or live OAuth account was used.

Microbenchmarks on Windows amd64, Intel Core i5-10210U:

| Operation | Time/op | Allocated bytes/op |
| --- | ---: | ---: |
| Identity raw projection, 1 KiB input | 12.2 us | 9,175 |
| Identity raw projection, 4 MiB input | 10.6 ms | 12,611,724 |
| WS delta ownership check | 1.1 us | 368 |

These measure local processing, not upstream latency or a before/after gain.
Large raw requests still incur full-buffer splice allocations; the projection
does not deserialize the inference input into a Go object graph.
