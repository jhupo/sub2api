# OpenAI Session Admission

`gateway.scheduling.new_session_soft_limit_percent` defaults to `70` (environment:
`GATEWAY_SCHEDULING_NEW_SESSION_SOFT_LIMIT_PERCENT`). Set it to `0` to disable.

- New sessions try the soft concurrency limit across all eligible accounts before
  falling back to available hard capacity. Priority and subscription preference
  apply within each pass; Top-K cannot hide the overflow pool.
- The soft limit rounds upward: hard limits 1/2/3 stay 1/2/3; 10 becomes 7.
  Unlimited accounts remain unlimited. Adaptive pressure tightens the hard limit
  first, and Redis atomically applies the soft percentage afterward.
- Existing sticky sessions, response-ID continuations, guardian bindings and
  active migrations bypass soft admission. A soft threshold is not a quota,
  billing adjustment, account error or cooldown.
- Under Codex adaptive scheduling, movable requests waiting for account capacity
  reconsider spare accounts every 750 ms within the existing bounded queue budget.
  The wait entry remains on the original account until the scan acquires capacity
  or follows the winning migration owner. User concurrency is unchanged.
- A capacity migration commits affinity only after success, through the existing
  distributed migration lease. It does not replay requests after output begins.
- Ordinary load scheduling uses the same soft-admission policy when batch load
  scheduling is enabled. Third-party OpenAI-compatible API-key accounts receive
  soft admission, but do not acquire Codex-specific adaptive error handling.

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
