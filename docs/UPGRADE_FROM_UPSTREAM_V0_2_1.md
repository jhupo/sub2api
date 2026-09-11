# Upgrade From Official v0.2.1

This is a local-code deployment plan, not evidence of a production rehearsal.
Use a tested release containing the WS funding and auth-cache fixes; an older
published tag does not include uncommitted working-tree changes.

## Three Independent Versions

- Release VERSION/tag selects the executable and image. It does not prove that
  a database schema or a cached JSON object is compatible.
- API-key auth snapshot version 24 invalidates incompatible authentication
  snapshots. Its Redis namespace is jhupo:apikey:auth:. Cache misses reload the
  database. No FLUSHDB/FLUSHALL is required or appropriate.
- Database migration filenames and checksums determine pending schema changes.
  Do not infer migrations from a release string or edit migration history to
  bypass validation. The known upstream checksum exception is narrowly scoped.

## Preserve the Existing Deployment

Keep the existing Compose project, PostgreSQL and Redis services, networks,
volume/bind paths, database name/user, REDIS_DB, credentials, application data,
JWT_SECRET and TOTP_ENCRYPTION_KEY. Do not replace the entire Compose file with
our template: its PostgreSQL/Redis versions and pool sizes may differ.

Change only the application image to ghcr.io/jhupo/sub2api at an explicit tested
tag (preferably its recorded digest). Retain UPDATE_STRATEGY=binary unless the
operator intentionally configures a different update mechanism. A later image
recreation replaces an in-container online binary update with the image binary.

Changing only image is enough to reuse the same data services, but it is NOT
a zero-write database operation: application startup normally applies pending
migrations. The subscription funding migration backfills existing plans, keys
and subscription usage and also changes constraints. It cannot be treated as
an automatically reversible, add-only change.

## Rehearse Before Production

Restore a recent PostgreSQL backup into an isolated database with isolated Redis.
Never run old and new application writers against the same production database.
Verify migrated user/key/account counts, wallet totals, key funding sources and
subscription ownership, expiry, plan versions and all usage windows. Include
expired/disabled keys, legacy subscription keys and users with multiple plans.

Test login, API key authentication, package assignment/redemption, wallet and
subscription billing, HTTP streaming, sequential WS turns, model switching,
insufficient allowance, retries, cancellation and reconnection. Check official
OpenAI usage versus user rate-adjusted billing; a hold is not actual spending.
Initially keep optional Codex overdraft probing disabled unless intentionally
required. Do not overwrite an existing operator setting through migration.

## Production Sequence

The following commands are examples for a Compose service named sub2api and
must be adapted to the existing Compose file/project. They are not executed by
this task.

1. Record the running binary version and image digest; preserve Compose, .env,
   application configuration and secrets securely. Prepare a tested rollback.
2. Pull only the selected application image. Drain public traffic and active
   HTTP/WS requests, then stop only the old application writer. A long stream
   may need more than the illustrative 120-second stop grace period.
3. Take and verify a consistent PostgreSQL backup after writes stop. Preserve
   Redis persistence as appropriate, without flushing or replacing its volume.
4. Apply migrations once using the new image against the same database. Any
   unexpected checksum or migration error is a stop condition, not a reason to
   manually delete migration records.
5. Check migrations, recreate only the application, then verify health and
   critical payment paths before restoring traffic.

```sh
docker compose pull sub2api
docker compose stop -t 120 sub2api
# Take and verify the deployment-specific database backup here.
docker compose run --rm --no-deps --entrypoint /app/sub2api sub2api -migrate-only
docker compose run --rm --no-deps --entrypoint /app/sub2api sub2api -check-migrations
docker compose up -d --no-deps --force-recreate sub2api
docker compose logs --since 10m sub2api
```

The explicit entrypoint avoids application entrypoint permission maintenance
for migration-only jobs. These commands do not recreate PostgreSQL or Redis.
Do not use compose down -v, system prune --volumes, or a Redis flush.

## Rollback Boundary

Before schema changes, restoring the old image/configuration is straightforward.
After the funding migration, switching only the old image is not a supported
rollback: old code may not satisfy the new plan/payment constraints. Restore
the verified pre-upgrade database into the planned rollback deployment, keeping
all writers stopped until data and cache state are coherent. Once new payments
have been accepted, blindly restoring that backup would lose them; reconcile
post-upgrade transactions or roll forward instead.

Keeping Redis data volumes is not a guarantee that every cached entry remains
valid after a database restore. Plan targeted cache invalidation/reconciliation
for rollback separately; never solve it with an unreviewed global flush.
