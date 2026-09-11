Sub2API v0.2.15

## Request Preauthorization and Settlement

- Price the initial token reservation using ordinary input costs and the requested output budget or default window. Remove hypothetical cache-read and cache-write maximums that could reject long conversations despite sufficient funds for ordinary input.
- Apply the same input pricing policy to HTTP text requests, OpenAI Responses WebSocket turns, and token-metered image and audio requests. Preserve separate image and audio rates and renewable streaming output reservations.
- Settle wallet requests from actual provider usage without requiring another admission hold after the cost has already been incurred. The existing atomic wallet finalizer refunds excess reservations or debits the difference; actual usage above the remaining balance can create a negative balance.
- Subscription requests still reserve and replenish their own allowance and remain subject to subscription limits. This release does not make reservations a guarantee of zero estimation error or zero overage.
- Verify that an unchanged WebSocket turn retry reuses its reservation, a larger retry increases the cumulative target only once, and subsequent output reservations retain the same billing identity.
- No compression-specific bypass, pricing compatibility switch, database migration, historical billing rewrite, or production data mutation is included.

## Gemini Account Usage Display

- Show up to two compact quota summary rows, with expandable reset times, update time, and local usage details.
- For an explicit upstream quota group, display the valid window with the least remaining quota. Keep independent model quotas separate and show unknown values as unavailable instead of zero.
- Keep error details accessible without expanding the account table row with a long error message.

## Remaining Work

- Automatic model synchronization and account whitelist initialization after Gemini OAuth authorization are not completed in this release. Existing manual synchronization and model selection behavior remains in place.
- Streaming requests can still stop when additional wallet funds or subscription allowance are unavailable. Final billing depends on the usage reported by the upstream provider.

## Validation

- Backend service, handler, and repository package tests passed.
- Frontend quota and account usage regression tests passed: 49 tests across three files.
- Frontend production build, TypeScript checks, and full lint passed.
- Production servers were not modified or upgraded during release preparation.

## Container Image

`ghcr.io/jhupo/sub2api:0.2.15`
