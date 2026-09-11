# Gemini OAuth quota and model discovery

## Scope

Gemini accounts with `oauth_type=antigravity` use the authenticated Code Assist
model catalog and upstream quota windows. AI Studio/API-key and GCP Code Assist
request-count policies remain separate.

## Quota

- Resolve the token through the account's token provider before querying.
- Query `retrieveUserQuotaSummary` and preserve groups and buckets, including
  upstream window names, remaining fractions, and reset timestamps.
- Only an unsupported summary endpoint (404/501) permits `fetchAvailableModels`
  fallback. Authentication, 429, and malformed responses do not imply no quota.
- Do not infer five-hour/weekly windows from a model quota or local request counts.
- Account usage UI and channel monitoring consume the same window data.
- Gemini Antigravity accounts no longer use the estimated 1500 RPD / 120 RPM
  policy as a scheduling gate. Existing upstream error/cooldown handling remains.
- These queries do not perform inference or settle customer usage.

## Models

- Stop on the first successful catalog; do not union daily/sandbox/prod catalogs.
- Preserve upstream IDs and variants for both authorized sync and an unmapped
  client catalog. Exclude existing internal placeholder IDs.
- Keep administrator whitelist/alias configuration. Exact upstream IDs take
  precedence; an unavailable explicitly requested variant cannot downgrade.
- Explicit synchronization reports refresh errors instead of claiming a cached
  directory is newly synchronized. Ordinary reads may still use stale cache
  alongside an error.

## Billing and rollout

Model discovery does not supply a verified price card. Subscription balances,
group multipliers and historical usage rows are not rewritten.

Gemini Antigravity text dispatch now checks the settlement price candidates and
calculator before forwarding, independent of the preauthorization switch. The
selected price must be explicitly configured or identified in the price catalog;
a family-name guess alone is insufficient. Missing pricing returns an error and
does not send an inference request. Native Gemini token-image models use token
pricing by default, including input, thinking and image output. Explicit
operator image/group pricing remains authoritative.

## Official API pricing policy

- Reference: https://ai.google.dev/gemini-api/docs/pricing and
  https://platform.claude.com/docs/en/about-claude/pricing (2026-09-11).
- Use non-promotional Standard API list prices. Gemini 3.6 Flash uses USD
  1.50 input / 7.50 output / 0.15 cached input per million tokens, even when a
  downloaded catalog contains the temporary half-price offer. Operator override
  files and group/channel price cards still take precedence.
- Known Gemini thinking levels share the public base-model price. They change
  output usage, not the unit rate. Exact operator overrides remain available.
- Thinking is included in output tokens. Cached tokens are removed from normal
  input before pricing. Gemini 2.5 Flash audio uses USD 1.00 input / 0.10 cache
  read per million tokens; upstream modality counts partition existing totals.
- Gemini 3.1 Flash Image uses USD 0.50 input / 3.00 text-and-thinking output /
  60.00 image output per million tokens. Gemini 3 Pro Image uses 2 / 12 / 120.
  Per-image examples are rounded equivalents, not an additional charge.
- Preauthorization includes image output and conservative audio input estimates;
  estimates never become reported usage or the final charge. Settlement uses
  provider-reported usage and applies the customer/group multiplier once.
- Existing long-context, peak, explicit per-image and custom pricing settings
  remain in force. No new cache-storage or search charge is inferred from a
  cache hit or tool declaration; these require their own authoritative usage.

Before rollout, synchronize each account's authorized models, inspect existing
whitelist targets, and configure explicit channel prices for internal IDs such as
`gemini-pro-agent` that have no independent entry. Do not derive their prices from
display names. Public API list prices and subscription quota consumption are not
interchangeable, and unknown GPT-OSS IDs must not inherit a generic GPT price.

Reference implementation inspected: CPA-Manager-Plus commit
`e19d8267a52ca146c43bdb86d185bb7baed7ff38`, especially
`apps/web/src/utils/quota/providerRequests.ts` and `builders.ts`.

No database migration is required. No production credentials or upstream
inference calls are used by the regression tests.
