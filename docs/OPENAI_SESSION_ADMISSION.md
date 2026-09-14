# OpenAI Session Admission

`gateway.scheduling.new_session_soft_limit_percent` defaults to `70` (environment:
`GATEWAY_SCHEDULING_NEW_SESSION_SOFT_LIMIT_PERCENT`). Set it to `0` to disable.

- New sessions try the soft concurrency limit across all eligible accounts before
  falling back to available hard capacity. Priority and subscription preference
  apply within each pass; Top-K cannot hide the overflow pool.
- The soft limit rounds upward: hard limits 1/2/3 stay 1/2/3; 10 becomes 7.
  Unlimited accounts remain unlimited. A soft threshold is not a quota, billing
  adjustment, account error or cooldown.
- Existing sticky sessions, response-ID continuations and guardian bindings
  bypass soft admission.
- Ordinary load scheduling uses the same soft-admission policy when batch load
  scheduling is enabled. Third-party OpenAI-compatible API-key accounts receive
  soft admission.

No database schema, Redis key layout, quota, pricing or prepaid billing changes
are required. The extra work is bounded candidate probing, not upstream calls.

## User-Agent

The configured canonical identity and synchronized client version remain the
source for OAuth outbound headers. This change does not guess a new OS fingerprint
or claim that a particular User-Agent prevents overload or account restrictions.
When identity enforcement is disabled, a valid client UA version is preserved;
missing, invalid or mismatched `version` headers are aligned to it. If the UA
version falls below the existing minimum, both UA version declarations and the
header are rebuilt together, preserving the OS/architecture/terminal portion.
