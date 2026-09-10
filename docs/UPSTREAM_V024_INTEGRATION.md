# Upstream v0.2.4 Integration

Base: local `330afe1ae` (v0.2.10). Upstream: `5de5e2be` (v0.2.4).
Release target: v0.2.11. This integration does not change our update source,
payment source/group separation, or production deployment.

## Scope

| Area | Upstream commits | Local integration |
| --- | --- | --- |
| OAuth 429 | `5fea83dc` | Window reset headers only prove exhaustion when the corresponding usage reaches 100%; retain request-scoped retry policy. |
| Runtime recovery | `6d339ec9` reviewed, not copied | Require observed persistence followed by a newer recovery snapshot before releasing a local block; stale snapshots cannot revoke new protection. |
| Redis | `99eba19f` | go-redis 9.22.0, current sorted-set API; no Redis data conversion. |
| Ops logs | `163eb7ff` | Independent system-log retention and optional access-log persistence; warnings/errors remain indexed. |
| Chat streaming | `be4ab92b` | Cancel before closing upstream bodies, propagate client cancellation, retain observed usage and pricing metadata. |
| WS cancellation | `a10ff125` reviewed, not copied | Stop the upstream turn promptly, discard its pooled connection, preserve observed usage through Forward and handler layers; no detached 900-second drain or replay. |
| Image 2.5 | `7ccc8a6f` | Flare/Sunburst catalog, allowlist/mapping, Images and Responses tool routing, pricing, usage normalization and image-aware reservations. |
| Proxies | `38cfd7e2`, `e835f798`, `859de4c8`, `7137fae2`, `efc6e4a8`, `188e3a9f` | Directed backup relation, partial updates, repeated expiry fallback retaining the original proxy, valid JSON dates. |
| Channel cache | `1ee929e4` | Redis invalidations, reconnect invalidation, generation-checked snapshot publication, shutdown cleanup. |
| Other providers | `4e5d67df`, `b6ee9f0a`, `c8deeb0b` | Claude one-token probe, Antigravity OAuth plan persistence, Grok raw Chat option filtering without modifying tool schemas/results. |
| Administration | `941a0487`, `7f0f579b`, `df64b5f3`, `6c8ad0bd` | Remove assigned inactive groups, keep failed refresh selections, viewport-aware account menu, registration visibility. |

## Image Billing Contract

- Supported additions: `gpt-image-2.5-flare`, `gpt-image-2.5-sunburst`.
- OAuth image tool model and Responses driver remain distinct. The driver is
  configurable through `SUB2API_IMAGES_MAIN_MODEL`, defaulting to `gpt-5.6-sol`.
  This default does not guarantee that every upstream account has driver access.
- Final billing uses the existing group/channel pricing resolver and multipliers.
  Per-image pricing stays per-image; token pricing receives separate image input
  and output quantities. Explicit model pricing wins over catalog fallbacks.
- Images responses without output details report image-only output tokens.
  Explicit details, including zero image tokens, take precedence. This rule
  does not apply to text/mixed Responses usage.
- Token reservations are estimates, not provider usage or an official maximum.
  The local budget uses image dimensions (default 2048x2048), 16-pixel tiles plus
  256 tokens, multiplied by output count. Locally known upload dimensions are
  used; remote images are not fetched for estimation. Actual usage settles the
  reservation. Quality-dependent provider metering can differ from this budget.
- Encoded image output never drives text-window top-ups. Mixed Responses retain
  their text window and the site's configured image-output rate.
- Wallet and subscription reservations share the estimator. No synthetic usage
  is written to compensate for missing upstream usage on a canceled stream.

Official references:

- https://developers.openai.com/api/docs/guides/image-generation
- https://developers.openai.com/api/reference/resources/images/methods/generate
- https://developers.openai.com/api/docs/pricing

## Deployment and Verification Boundaries

- No destructive data migration is added. Migration 149 already defines a
  non-unique backup-proxy index; the Ent relation is corrected to match it.
- No server operations or live paid model requests are part of this change.
- Development verification used focused tests. The release pass additionally
  runs full unit/frontend tests, lint, production builds and CI integration tests.
- PostgreSQL integration tests and Go race detection require a suitable local
  database/container runtime and C compiler respectively; ordinary unit tests
  are not substitutes for those checks or production-load validation.

## Intentionally Not Copied

- Subscription-to-group visibility coupling conflicts with our independent
  funding-source and group-permission architecture.
- Upstream H2 ping tightening and the unconnected LongStream profile do not
  justify changing the existing 15s/15s transport policy in this batch.
- MiniMax expansion, weekly quota-cost estimates, optional Grok media admin,
  sponsor metadata and upstream release-version files remain out of scope.

## Focused Verification

- Go service, repository, handler/admin, startup wiring, configuration and model
  tests passed for the integrated areas. Scheduling/slot and reservation tests
  were included during development, before the separate release pass.
- Frontend typecheck and changed-file ESLint passed; 8 targeted Vitest files,
  117 tests passed.
- Concurrent runtime-block isolation: 30 callers across 50 accounts; stale
  snapshot rejection, new-block protection and database timestamp precision.
- Channel cache: invalidated in-flight builds (success and error), Redis
  publication, reconnect invalidation and idempotent subscription shutdown.
- WS tests exercise the public Forward path before/after output, observed
  usage preservation, closed pooled connections and no HTTP replay.
- Runtime snapshot microbenchmark on the local i5-10210U with GOMAXPROCS=4:
  approximately 182 ns/op, 0 B/op, 0 allocations/op. This is not an end-to-end
  latency or production-capacity measurement.
