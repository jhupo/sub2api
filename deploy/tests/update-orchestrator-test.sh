#!/usr/bin/env bash
set -Eeuo pipefail

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
tmp_root=$(mktemp -d "${TMPDIR:-/tmp}/sub2api-update-test.XXXXXX")
case "$tmp_root" in
  "${TMPDIR:-/tmp}"/sub2api-update-test.*) ;;
  *) printf 'unsafe temporary directory: %s\n' "$tmp_root" >&2; exit 1 ;;
esac
trap 'rm -rf -- "$tmp_root"' EXIT

mock_bin="$tmp_root/bin"
project_dir="$tmp_root/project"
mock_log="$tmp_root/docker.log"
mock_state="$tmp_root/container-version"
status_dir="$project_dir/data/update-status"
mkdir -p "$mock_bin" "$project_dir"
: > "$project_dir/docker-compose.yml"

cat > "$mock_bin/docker" <<'EOF'
#!/usr/bin/env bash
set -eu

printf 'version=%s docker %s\n' "${SUB2API_VERSION:-}" "$*" >> "$MOCK_DOCKER_LOG"

case "${1:-}" in
  ps)
    case " $* " in
      *' -aq '*)
        if [ -n "${MOCK_MISSING_CONTAINER_ONCE_MARKER:-}" ] && [ ! -f "$MOCK_MISSING_CONTAINER_ONCE_MARKER" ]; then
          : > "$MOCK_MISSING_CONTAINER_ONCE_MARKER"
        else
          printf '%s\n' mock-container
        fi
        ;;
    esac
    exit 0
    ;;
  inspect)
    case "$*" in
      *'{{.Config.Image}}'*) printf '%s\n' 'ghcr.io/jhupo/sub2api:0.1.241' ;;
      *'.State.Health'*)
        state=$(cat "$MOCK_DOCKER_STATE" 2>/dev/null || true)
        if [ "${MOCK_FAIL_TARGET_HEALTH:-0}" = 1 ] && [ "$state" = 0.1.242 ]; then
          printf '%s\n' unhealthy
        else
          printf '%s\n' healthy
        fi
        ;;
      *) printf '%s\n' mock-container ;;
    esac
    exit 0
    ;;
  run)
    if [ "${MOCK_FAIL_FINALIZER_RUN:-0}" = 1 ]; then
      exit 1
    fi
    exit 0
    ;;
  stop)
    if [ -n "${MOCK_FAIL_STOP_ONCE_MARKER:-}" ] && [ ! -f "$MOCK_FAIL_STOP_ONCE_MARKER" ]; then
      : > "$MOCK_FAIL_STOP_ONCE_MARKER"
      exit 1
    fi
    exit 0
    ;;
  start)
    exit 0
    ;;
  compose)
    shift
    if [ "${1:-}" = version ]; then
      exit 0
    fi
    while [ "$#" -gt 0 ]; do
      case "$1" in
        -f|-p|--env-file) shift 2 ;;
        *) break ;;
      esac
    done
    command=${1:-}
    shift || true
    case "$command" in
      pull)
        sleep "${MOCK_PULL_DELAY:-0}"
        exit 0
        ;;
      up)
        service="${!#}"
        if [ "${MOCK_FAIL_TARGET_SERVICE:-}" = "$service" ] && [ "${SUB2API_VERSION:-}" = 0.1.242 ]; then
          exit 1
        fi
        if [ "${MOCK_FAIL_ROLLBACK_SERVICE:-}" = "$service" ] && [ "${SUB2API_VERSION:-}" = 0.1.241 ]; then
          exit 1
        fi
        printf '%s' "${SUB2API_VERSION:-}" > "$MOCK_DOCKER_STATE"
        exit 0
        ;;
      ps)
        printf '%s\n' mock-container
        exit 0
        ;;
    esac
    ;;
esac

printf 'unexpected docker invocation: %s\n' "$*" >&2
exit 1
EOF
chmod +x "$mock_bin/docker"

cat > "$mock_bin/curl" <<'EOF'
#!/usr/bin/env bash
set -eu
if [ "${MOCK_CURL_FAIL:-0}" = 1 ]; then
  exit 1
fi
output=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o) output="${2:-}"; shift 2 ;;
    http://*|https://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
if [ -n "$output" ]; then
  if [[ "$url" = */checksums.txt ]]; then
    printf '%s\n' 'deadbeef  sub2api_0.1.242_linux_amd64.tar.gz' > "$output"
  else
    : > "$output"
  fi
fi
exit 0
EOF
chmod +x "$mock_bin/curl"

cat > "$mock_bin/tar" <<'EOF'
#!/usr/bin/env bash
set -eu
destination=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -C) destination="${2:-}"; shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$destination" ] || exit 1
printf '%s' 'new-runtime' > "$destination/sub2api"
EOF
chmod +x "$mock_bin/tar"

cat > "$mock_bin/sha256sum" <<'EOF'
#!/usr/bin/env sh
printf 'deadbeef  %s\n' "$1"
EOF
chmod +x "$mock_bin/sha256sum"

cat > "$mock_bin/mv" <<'EOF'
#!/usr/bin/env bash
set -eu

args=("$@")
if [ "${1:-}" = -f ]; then
  shift
fi
source_path="${1:-}"
if [ "${MOCK_FAIL_TERMINAL_STATUS_WRITE:-0}" = 1 ] &&
  [ -n "${MOCK_STATUS_FAIL_MARKER:-}" ] &&
  [ ! -f "$MOCK_STATUS_FAIL_MARKER" ] &&
  grep -Fq '"status":"succeeded"' "$source_path" 2>/dev/null; then
  : > "$MOCK_STATUS_FAIL_MARKER"
  sleep "${MOCK_TERMINAL_WRITE_DELAY:-3}"
  exit 1
fi
if [ "${MOCK_FAIL_RUNTIME_RESTORE:-0}" = 1 ] && [[ "$source_path" = *.rollback.* ]]; then
  exit 1
fi
exec /bin/mv "${args[@]}"
EOF
chmod +x "$mock_bin/mv"

run_orchestrator() {
  env \
    PATH="$mock_bin:$PATH" \
    MOCK_DOCKER_LOG="$mock_log" \
    MOCK_DOCKER_STATE="$mock_state" \
    SUB2API_UPDATE_DOCKER_COMMAND="$mock_bin/docker" \
    SUB2API_UPDATE_MODE=image \
    SUB2API_UPDATE_COMPOSE_FILE="$project_dir/docker-compose.yml" \
    SUB2API_UPDATE_SERVICES=sub2api \
    SUB2API_UPDATE_HEALTH_URLS=container \
    SUB2API_UPDATE_SELF_CONTAINER=sub2api-container \
    SUB2API_UPDATE_SELF_SERVICE=sub2api \
    SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
    SUB2API_UPDATE_HEALTH_TIMEOUT_SECONDS=5 \
	SUB2API_UPDATE_STATUS_DIR="$status_dir" \
    "$@"
}

run_runtime_orchestrator() {
  env \
    PATH="$mock_bin:$PATH" \
    MOCK_DOCKER_LOG="$mock_log" \
    MOCK_DOCKER_STATE="$mock_state" \
    SUB2API_UPDATE_MODE=runtime \
    SUB2API_UPDATE_RUNTIME_PATH="$project_dir/sub2api" \
    SUB2API_UPDATE_DOCKER_COMMAND="$mock_bin/docker" \
    SUB2API_UPDATE_SERVICES=worker \
    SUB2API_UPDATE_HEALTH_URLS=container \
    SUB2API_UPDATE_SELF_CONTAINER=updater \
    SUB2API_UPDATE_SELF_SERVICE= \
    SUB2API_UPDATE_HEALTH_TIMEOUT_SECONDS=5 \
    SUB2API_UPDATE_STATUS_DIR="$status_dir" \
    "$@"
}

: > "$mock_log"
scheduled_output=$(run_orchestrator bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
	--operation-id sysop-scheduled123 \
  --release-url https://github.com/jhupo/sub2api/releases/tag/v0.1.242)
printf '%s' "$scheduled_output" | grep -Fq 'update scheduled: 0.1.241 -> 0.1.242'
if printf '%s' "$scheduled_output" | grep -Fq 'update completed:'; then
  printf 'parent updater reported completion before self recreation\n' >&2
  exit 1
fi
grep -Fq -- '--entrypoint /usr/local/bin/sub2api-update' "$mock_log"
grep -Fq -- '--finalize-self --current-version 0.1.241 --target-version 0.1.242 --release-url https://github.com/jhupo/sub2api/releases/tag/v0.1.242 --operation-id sysop-scheduled123' "$mock_log"
grep -Fq -- '--volumes-from sub2api-container' "$mock_log"
grep -Fq -- "--env SUB2API_UPDATE_DOCKER_COMMAND=$mock_bin/docker" "$mock_log"
grep -Fq '"status":"pending"' "$status_dir/sysop-scheduled123.json"

: > "$mock_log"
rm -f "$mock_state"
completed_output=$(run_orchestrator bash "$repo_root/deploy/update-orchestrator.sh" \
	--finalize-self --current-version 0.1.241 --target-version 0.1.242 \
	--operation-id sysop-completed123)
printf '%s' "$completed_output" | grep -Fq 'update completed: 0.1.241 -> 0.1.242'
grep -Eq 'version=0\.1\.242 docker compose .* up --timeout 610 -d --no-deps --force-recreate sub2api' "$mock_log"
grep -Fq '"status":"succeeded"' "$status_dir/sysop-completed123.json"

: > "$mock_log"
rm -f "$mock_state"
if rollback_output=$(MOCK_FAIL_TARGET_HEALTH=1 run_orchestrator \
  env SUB2API_UPDATE_SERVICES=worker,sub2api \
  bash "$repo_root/deploy/update-orchestrator.sh" \
	--finalize-self --current-version 0.1.241 --target-version 0.1.242 \
	--operation-id sysop-rollback123 2>&1); then
  printf 'unhealthy target unexpectedly succeeded\n' >&2
  exit 1
fi
printf '%s' "$rollback_output" | grep -Fq 'self update failed; all services restored to the previous version'
grep -Eq 'version=0\.1\.242 docker compose .* up --timeout 610 -d --no-deps --force-recreate sub2api' "$mock_log"
grep -Eq 'version=0\.1\.241 docker compose .* up --timeout 610 -d --no-deps --force-recreate sub2api' "$mock_log"
grep -Eq 'version=0\.1\.241 docker compose .* up --timeout 610 -d --no-deps --force-recreate worker' "$mock_log"
grep -Fq '"status":"rolled_back"' "$status_dir/sysop-rollback123.json"

: > "$mock_log"
rm -f "$mock_state"
if incomplete_output=$(MOCK_FAIL_TARGET_HEALTH=1 MOCK_FAIL_ROLLBACK_SERVICE=sub2api run_orchestrator \
  env SUB2API_UPDATE_SERVICES=worker,sub2api \
  bash "$repo_root/deploy/update-orchestrator.sh" \
	--finalize-self --current-version 0.1.241 --target-version 0.1.242 \
	--operation-id sysop-incomplete123 2>&1); then
  printf 'incomplete rollback unexpectedly succeeded\n' >&2
  exit 1
fi
printf '%s' "$incomplete_output" | grep -Fq 'one or more services could not be restored'
grep -Eq 'version=0\.1\.241 docker compose .* up --timeout 610 -d --no-deps --force-recreate worker' "$mock_log"
grep -Fq '"status":"failed"' "$status_dir/sysop-incomplete123.json"
grep -Fq '"reason":"rollback_incomplete"' "$status_dir/sysop-incomplete123.json"

: > "$mock_log"
rm -f "$mock_state"
if rollout_failure_output=$(MOCK_FAIL_TARGET_SERVICE=worker run_orchestrator \
  env SUB2API_UPDATE_SERVICES=api,worker SUB2API_UPDATE_SELF_SERVICE= \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-rolloutfailure123 2>&1); then
  printf 'failed non-finalizer rollout unexpectedly succeeded\n' >&2
  exit 1
fi
printf '%s' "$rollout_failure_output" | grep -Fq 'rollout failed; previous version restored'
worker_restore_line=$(grep -nE 'version=0\.1\.241 docker compose .* up --timeout 610 -d --no-deps --force-recreate worker' "$mock_log" | cut -d: -f1)
api_restore_line=$(grep -nE 'version=0\.1\.241 docker compose .* up --timeout 610 -d --no-deps --force-recreate api' "$mock_log" | cut -d: -f1)
[ "$worker_restore_line" -lt "$api_restore_line" ] || {
  printf 'non-finalizer rollback did not restore services in reverse order\n' >&2
  exit 1
}
grep -Fq '"status":"rolled_back"' "$status_dir/sysop-rolloutfailure123.json"

: > "$mock_log"
rm -f "$mock_state"
if partial_restore_output=$(MOCK_FAIL_TARGET_SERVICE=worker MOCK_FAIL_ROLLBACK_SERVICE=worker run_orchestrator \
  env SUB2API_UPDATE_SERVICES=api,worker SUB2API_UPDATE_SELF_SERVICE= \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-partialrestore123 2>&1); then
  printf 'incomplete non-finalizer rollback unexpectedly succeeded\n' >&2
  exit 1
fi
printf '%s' "$partial_restore_output" | grep -Fq 'rollout failed and rollback was incomplete'
grep -Eq 'version=0\.1\.241 docker compose .* up --timeout 610 -d --no-deps --force-recreate worker' "$mock_log"
grep -Eq 'version=0\.1\.241 docker compose .* up --timeout 610 -d --no-deps --force-recreate api' "$mock_log"
grep -Fq '"status":"failed"' "$status_dir/sysop-partialrestore123.json"
grep -Fq '"reason":"rollback_incomplete"' "$status_dir/sysop-partialrestore123.json"

: > "$mock_log"
rm -f "$mock_state"
if finalizer_start_output=$(MOCK_FAIL_FINALIZER_RUN=1 run_orchestrator \
  env SUB2API_UPDATE_SERVICES=worker,sub2api \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-finalizerstart123 2>&1); then
  printf 'failed finalizer startup unexpectedly succeeded\n' >&2
  exit 1
fi
printf '%s' "$finalizer_start_output" | grep -Fq 'self restart scheduling failed; previous version restored'
grep -Eq 'version=0\.1\.242 docker compose .* up --timeout 610 -d --no-deps --force-recreate worker' "$mock_log"
grep -Eq 'version=0\.1\.241 docker compose .* up --timeout 610 -d --no-deps --force-recreate worker' "$mock_log"
grep -Fq '"status":"rolled_back"' "$status_dir/sysop-finalizerstart123.json"

if run_orchestrator bash "$repo_root/deploy/update-orchestrator.sh" \
	--current-version 0.1.241 --target-version 0.1.242 \
	--operation-id '../unsafe' >/dev/null 2>&1; then
	printf 'unsafe operation id unexpectedly accepted\n' >&2
	exit 1
fi

status_target_dir="$tmp_root/status-target"
status_link_dir="$tmp_root/status-link"
mkdir "$status_target_dir"
ln -s "$status_target_dir" "$status_link_dir"
if status_link_dir_output=$(run_orchestrator env SUB2API_UPDATE_STATUS_DIR="$status_link_dir" \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-statuslinkdir123 2>&1); then
  printf 'updater accepted a symlink status directory\n' >&2
  exit 1
fi
printf '%s' "$status_link_dir_output" | grep -Fq 'operation status file must be regular non-symlink paths'

status_file_root="$tmp_root/status-file-root"
: > "$status_file_root"
if status_file_root_output=$(run_orchestrator env SUB2API_UPDATE_STATUS_DIR="$status_file_root" \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-statusfileroot123 2>&1); then
  printf 'updater accepted a non-directory status root\n' >&2
  exit 1
fi
printf '%s' "$status_file_root_output" | grep -Fq 'operation status file must be regular non-symlink paths'

status_link_id=sysop-statuslinkfile123
status_link_target="$tmp_root/status-link-target"
printf '%s' 'do-not-touch' > "$status_link_target"
ln -s "$status_link_target" "$status_dir/$status_link_id.json"
if status_link_file_output=$(run_orchestrator bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$status_link_id" 2>&1); then
  printf 'updater accepted a symlink operation status file\n' >&2
  exit 1
fi
printf '%s' "$status_link_file_output" | grep -Fq 'operation status file must be regular non-symlink paths'
[ "$(cat "$status_link_target")" = do-not-touch ] || {
  printf 'symlink status file target was modified\n' >&2
  exit 1
}
rm -f "$status_dir/$status_link_id.json" "$status_link_target"

status_directory_id=sysop-statusdirectoryfile123
mkdir "$status_dir/$status_directory_id.json"
if status_directory_file_output=$(run_orchestrator bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$status_directory_id" 2>&1); then
  printf 'updater accepted a non-regular operation status file\n' >&2
  exit 1
fi
printf '%s' "$status_directory_file_output" | grep -Fq 'operation status file must be regular non-symlink paths'
rmdir "$status_dir/$status_directory_id.json"

if env \
  PATH="$mock_bin:$PATH" \
  MOCK_DOCKER_LOG="$mock_log" \
  MOCK_DOCKER_STATE="$mock_state" \
  SUB2API_UPDATE_MODE=image \
  SUB2API_UPDATE_COMPOSE_FILE="$project_dir/missing-compose.yml" \
  SUB2API_UPDATE_STATUS_DIR="$status_dir" \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-earlyfailure123 >/dev/null 2>&1; then
  printf 'missing Compose file unexpectedly succeeded\n' >&2
  exit 1
fi
grep -Fq '"status":"failed"' "$status_dir/sysop-earlyfailure123.json"
grep -Fq '"reason":"orchestrator_failed"' "$status_dir/sysop-earlyfailure123.json"

heartbeat_output="$tmp_root/heartbeat.out"
run_orchestrator env \
  MOCK_PULL_DELAY=5 \
  SUB2API_UPDATE_SERVICES=worker \
  SUB2API_UPDATE_STATUS_HEARTBEAT_SECONDS=1 \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-heartbeat123 >"$heartbeat_output" 2>&1 &
heartbeat_pid=$!
for _ in 1 2 3 4 5; do
  [ -f "$status_dir/sysop-heartbeat123.json" ] && break
  sleep 1
done
[ -f "$status_dir/sysop-heartbeat123.json" ] || {
  printf 'heartbeat status file was not created\n' >&2
  exit 1
}
heartbeat_before=$(stat -c %Y "$status_dir/sysop-heartbeat123.json")
sleep 3
heartbeat_after=$(stat -c %Y "$status_dir/sysop-heartbeat123.json")
[ "$heartbeat_after" -gt "$heartbeat_before" ] || {
  printf 'pending rollout lease was not renewed\n' >&2
  exit 1
}
wait "$heartbeat_pid"
grep -Fq '"status":"succeeded"' "$status_dir/sysop-heartbeat123.json"

owner_death_output="$tmp_root/owner-death.out"
env \
  PATH="$mock_bin:$PATH" \
  MOCK_DOCKER_LOG="$mock_log" \
  MOCK_DOCKER_STATE="$mock_state" \
  MOCK_PULL_DELAY=10 \
  SUB2API_UPDATE_MODE=image \
  SUB2API_UPDATE_COMPOSE_FILE="$project_dir/docker-compose.yml" \
  SUB2API_UPDATE_SERVICES=worker \
  SUB2API_UPDATE_HEALTH_URLS=container \
  SUB2API_UPDATE_SELF_CONTAINER=sub2api-container \
  SUB2API_UPDATE_SELF_SERVICE= \
  SUB2API_UPDATE_HEALTH_TIMEOUT_SECONDS=5 \
  SUB2API_UPDATE_STATUS_HEARTBEAT_SECONDS=1 \
  SUB2API_UPDATE_STATUS_DIR="$status_dir" \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-ownerdeath123 >"$owner_death_output" 2>&1 &
owner_death_pid=$!
for _ in 1 2 3 4 5; do
  [ -f "$status_dir/sysop-ownerdeath123.json" ] && break
  sleep 1
done
[ -f "$status_dir/sysop-ownerdeath123.json" ] || {
  printf 'owner-death status file was not created\n' >&2
  exit 1
}
kill -KILL "$owner_death_pid"
wait "$owner_death_pid" 2>/dev/null || true
sleep 2
owner_death_after_exit=$(stat -c %Y "$status_dir/sysop-ownerdeath123.json")
sleep 2
owner_death_later=$(stat -c %Y "$status_dir/sysop-ownerdeath123.json")
[ "$owner_death_later" -eq "$owner_death_after_exit" ] || {
  printf 'heartbeat continued after its owner exited\n' >&2
  exit 1
}
grep -Fq '"status":"pending"' "$status_dir/sysop-ownerdeath123.json"

terminal_failure_output="$tmp_root/terminal-failure.out"
terminal_failure_marker="$tmp_root/terminal-failure.marker"
run_orchestrator env \
  MOCK_FAIL_TERMINAL_STATUS_WRITE=1 \
  MOCK_STATUS_FAIL_MARKER="$terminal_failure_marker" \
  MOCK_TERMINAL_WRITE_DELAY=4 \
  SUB2API_UPDATE_SERVICES=worker \
  SUB2API_UPDATE_SELF_SERVICE= \
  SUB2API_UPDATE_STATUS_HEARTBEAT_SECONDS=1 \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-terminalfailure123 >"$terminal_failure_output" 2>&1 &
terminal_failure_pid=$!
for _ in 1 2 3 4 5; do
  [ -f "$terminal_failure_marker" ] && break
  sleep 1
done
[ -f "$terminal_failure_marker" ] || {
  printf 'terminal status write failure was not triggered\n' >&2
  exit 1
}
terminal_failure_before=$(stat -c %Y "$status_dir/sysop-terminalfailure123.json")
sleep 2
terminal_failure_after=$(stat -c %Y "$status_dir/sysop-terminalfailure123.json")
[ "$terminal_failure_after" -gt "$terminal_failure_before" ] || {
  printf 'heartbeat stopped before the terminal status was committed\n' >&2
  exit 1
}
if wait "$terminal_failure_pid"; then
  printf 'failed terminal status write unexpectedly succeeded\n' >&2
  exit 1
fi
grep -Fq '"status":"failed"' "$status_dir/sysop-terminalfailure123.json"
grep -Fq '"reason":"orchestrator_failed"' "$status_dir/sysop-terminalfailure123.json"

runtime_path="$project_dir/sub2api"
printf '%s' 'old-runtime' > "$runtime_path"
runtime_broken_backup_id=sysop-runtimebrokenbackup123
runtime_broken_backup="$runtime_path.update-backup.$runtime_broken_backup_id"
ln -s "$tmp_root/missing-runtime-backup-target" "$runtime_broken_backup"
if runtime_broken_backup_output=$(run_runtime_orchestrator \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$runtime_broken_backup_id" 2>&1); then
  printf 'runtime updater accepted a broken symlink at its generated backup path\n' >&2
  exit 1
fi
printf '%s' "$runtime_broken_backup_output" | grep -Fq 'runtime backup already exists'
[ "$(cat "$runtime_path")" = old-runtime ] || {
  printf 'rejected runtime backup path changed the runtime binary\n' >&2
  exit 1
}
[ -L "$runtime_broken_backup" ] || {
  printf 'rejected broken runtime backup symlink was removed\n' >&2
  exit 1
}
grep -Fq '"status":"failed"' "$status_dir/$runtime_broken_backup_id.json"
rm -f "$runtime_broken_backup"

stop_once_marker="$tmp_root/stop-once.marker"
if runtime_stop_output=$(MOCK_FAIL_STOP_ONCE_MARKER="$stop_once_marker" run_runtime_orchestrator \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-runtimestop123 2>&1); then
  printf 'runtime update with failed docker stop unexpectedly succeeded\n' >&2
  exit 1
fi
printf '%s' "$runtime_stop_output" | grep -Fq 'rollout failed; previous version restored'
[ "$(cat "$runtime_path")" = old-runtime ] || {
  printf 'runtime binary was not restored after docker stop failed\n' >&2
  exit 1
}
grep -Fq '"status":"rolled_back"' "$status_dir/sysop-runtimestop123.json"

printf '%s' 'old-runtime' > "$runtime_path"
missing_container_marker="$tmp_root/missing-container-once.marker"
if runtime_lookup_output=$(MOCK_MISSING_CONTAINER_ONCE_MARKER="$missing_container_marker" run_runtime_orchestrator \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-runtimelookup123 2>&1); then
  printf 'runtime update with missing container unexpectedly succeeded\n' >&2
  exit 1
fi
printf '%s' "$runtime_lookup_output" | grep -Fq 'container not found for runtime service: worker'
printf '%s' "$runtime_lookup_output" | grep -Fq 'rollout failed; previous version restored'
[ "$(cat "$runtime_path")" = old-runtime ] || {
  printf 'runtime binary was not restored after container lookup failed\n' >&2
  exit 1
}
grep -Fq '"status":"rolled_back"' "$status_dir/sysop-runtimelookup123.json"

printf '%s' 'old-runtime' > "$runtime_path"
restore_stop_marker="$tmp_root/restore-stop-once.marker"
if runtime_restore_output=$(MOCK_FAIL_STOP_ONCE_MARKER="$restore_stop_marker" MOCK_FAIL_RUNTIME_RESTORE=1 run_runtime_orchestrator \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-runtimerestore123 2>&1); then
  printf 'runtime update with failed binary restore unexpectedly succeeded\n' >&2
  exit 1
fi
printf '%s' "$runtime_restore_output" | grep -Fq 'rollback was incomplete'
[ "$(cat "$runtime_path")" = new-runtime ] || {
  printf 'failed runtime restore incorrectly reported the old binary\n' >&2
  exit 1
}
grep -Fq '"status":"failed"' "$status_dir/sysop-runtimerestore123.json"
grep -Fq '"reason":"rollback_incomplete"' "$status_dir/sysop-runtimerestore123.json"

: > "$mock_log"
printf '%s' 'old-runtime' > "$runtime_path"
runtime_self_output=$(run_runtime_orchestrator env \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_SELF_CONTAINER=sub2api-container \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-runtimeself123)
printf '%s' "$runtime_self_output" | grep -Fq 'update scheduled: 0.1.241 -> 0.1.242'
runtime_self_backup="$runtime_path.update-backup.sysop-runtimeself123"
[ "$(cat "$runtime_self_backup")" = old-runtime ] || {
  printf 'runtime self update did not preserve the previous binary for its finalizer\n' >&2
  exit 1
}
grep -Fq -- "--env SUB2API_UPDATE_DOCKER_COMMAND=$mock_bin/docker" "$mock_log"
grep -Fq -- "--env SUB2API_UPDATE_RUNTIME_BACKUP=$runtime_self_backup" "$mock_log"
grep -Fq -- '--env SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=true' "$mock_log"
grep -Fq -- '--env SUB2API_UPDATE_RUNTIME_CHANGED=true' "$mock_log"
grep -Fq -- '--finalize-self --current-version 0.1.241 --target-version 0.1.242' "$mock_log"
grep -Fq '"status":"pending"' "$status_dir/sysop-runtimeself123.json"

runtime_self_final_output=$(run_runtime_orchestrator env \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_SELF_CONTAINER=sub2api-container \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  SUB2API_UPDATE_RUNTIME_BACKUP="$runtime_self_backup" \
  SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=true \
  SUB2API_UPDATE_RUNTIME_CHANGED=true \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --finalize-self --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-runtimeself123)
printf '%s' "$runtime_self_final_output" | grep -Fq 'update completed: 0.1.241 -> 0.1.242'
grep -Fq '"status":"succeeded"' "$status_dir/sysop-runtimeself123.json"
[ ! -e "$runtime_self_backup" ] || {
  printf 'successful runtime self update left its rollback backup behind\n' >&2
  exit 1
}

printf '%s' 'old-runtime' > "$runtime_path"
runtime_self_rollback_id=sysop-runtimeselfrollback123
runtime_self_rollback_backup="$runtime_path.update-backup.$runtime_self_rollback_id"
run_runtime_orchestrator env \
  SUB2API_UPDATE_SERVICES=api,worker \
  SUB2API_UPDATE_SELF_CONTAINER=worker-container \
  SUB2API_UPDATE_SELF_SERVICE=worker \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$runtime_self_rollback_id" >/dev/null
runtime_self_stop_marker="$tmp_root/runtime-self-stop.marker"
if runtime_self_rollback_output=$(MOCK_FAIL_STOP_ONCE_MARKER="$runtime_self_stop_marker" run_runtime_orchestrator env \
  SUB2API_UPDATE_SERVICES=api,worker \
  SUB2API_UPDATE_SELF_CONTAINER=worker-container \
  SUB2API_UPDATE_SELF_SERVICE=worker \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  SUB2API_UPDATE_RUNTIME_BACKUP="$runtime_self_rollback_backup" \
  SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=true \
  SUB2API_UPDATE_RUNTIME_CHANGED=true \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --finalize-self --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$runtime_self_rollback_id" 2>&1); then
  printf 'failed runtime self rollout unexpectedly succeeded\n' >&2
  exit 1
fi
printf '%s' "$runtime_self_rollback_output" | grep -Fq 'runtime self update failed; previous version restored'
worker_restore_line=$(printf '%s\n' "$runtime_self_rollback_output" | grep -n 'restarting worker at 0.1.241' | cut -d: -f1)
api_restore_line=$(printf '%s\n' "$runtime_self_rollback_output" | grep -n 'restarting api at 0.1.241' | cut -d: -f1)
[ "$worker_restore_line" -lt "$api_restore_line" ] || {
  printf 'runtime self rollback did not restart all services in reverse order\n' >&2
  exit 1
}
[ "$(cat "$runtime_path")" = old-runtime ] || {
  printf 'runtime self finalizer did not restore the previous binary\n' >&2
  exit 1
}
[ ! -e "$runtime_self_rollback_backup" ] || {
  printf 'runtime self rollback left its backup behind\n' >&2
  exit 1
}
grep -Fq '"status":"rolled_back"' "$status_dir/$runtime_self_rollback_id.json"
grep -Fq '"reason":"target_not_ready"' "$status_dir/$runtime_self_rollback_id.json"

printf '%s' 'old-runtime' > "$runtime_path"
runtime_self_incomplete_id=sysop-runtimeselfincomplete123
runtime_self_incomplete_backup="$runtime_path.update-backup.$runtime_self_incomplete_id"
run_runtime_orchestrator env \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_SELF_CONTAINER=sub2api-container \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$runtime_self_incomplete_id" >/dev/null
runtime_self_incomplete_stop_marker="$tmp_root/runtime-self-incomplete-stop.marker"
if runtime_self_incomplete_output=$(MOCK_FAIL_STOP_ONCE_MARKER="$runtime_self_incomplete_stop_marker" MOCK_FAIL_RUNTIME_RESTORE=1 \
  run_runtime_orchestrator env \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_SELF_CONTAINER=sub2api-container \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  SUB2API_UPDATE_RUNTIME_BACKUP="$runtime_self_incomplete_backup" \
  SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=true \
  SUB2API_UPDATE_RUNTIME_CHANGED=true \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --finalize-self --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$runtime_self_incomplete_id" 2>&1); then
  printf 'incomplete runtime self rollback unexpectedly succeeded\n' >&2
  exit 1
fi
printf '%s' "$runtime_self_incomplete_output" | grep -Fq 'runtime self update failed and rollback was incomplete'
[ "$(cat "$runtime_path")" = new-runtime ] || {
  printf 'failed runtime self restore incorrectly reported the previous binary\n' >&2
  exit 1
}
[ -f "$runtime_self_incomplete_backup" ] || {
  printf 'incomplete runtime self rollback discarded its recovery backup\n' >&2
  exit 1
}
grep -Fq '"status":"failed"' "$status_dir/$runtime_self_incomplete_id.json"
grep -Fq '"reason":"rollback_incomplete"' "$status_dir/$runtime_self_incomplete_id.json"
rm -f "$runtime_self_incomplete_backup"

printf '%s' 'old-runtime' > "$runtime_path"
runtime_self_symlink_id=sysop-runtimeselfsymlink123
runtime_self_symlink_backup="$runtime_path.update-backup.$runtime_self_symlink_id"
runtime_self_symlink_target="$tmp_root/runtime-self-symlink-target"
run_runtime_orchestrator env \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_SELF_CONTAINER=sub2api-container \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$runtime_self_symlink_id" >/dev/null
mv "$runtime_self_symlink_backup" "$runtime_self_symlink_target"
ln -s "$runtime_self_symlink_target" "$runtime_self_symlink_backup"
if runtime_self_symlink_output=$(run_runtime_orchestrator env \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_SELF_CONTAINER=sub2api-container \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  SUB2API_UPDATE_RUNTIME_BACKUP="$runtime_self_symlink_backup" \
  SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=true \
  SUB2API_UPDATE_RUNTIME_CHANGED=true \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --finalize-self --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$runtime_self_symlink_id" 2>&1); then
  printf 'runtime self finalizer accepted a symlink backup on its success path\n' >&2
  exit 1
fi
printf '%s' "$runtime_self_symlink_output" | grep -Fq 'SUB2API_UPDATE_RUNTIME_BACKUP must be a regular non-symlink file'
[ "$(cat "$runtime_path")" = new-runtime ] || {
  printf 'rejected symlink backup unexpectedly changed the runtime binary\n' >&2
  exit 1
}
[ -L "$runtime_self_symlink_backup" ] || {
  printf 'rejected symlink backup was unexpectedly removed\n' >&2
  exit 1
}
grep -Fq '"status":"failed"' "$status_dir/$runtime_self_symlink_id.json"
grep -Fq '"reason":"orchestrator_failed"' "$status_dir/$runtime_self_symlink_id.json"
rm -f "$runtime_self_symlink_backup" "$runtime_self_symlink_target"

runtime_self_directory_id=sysop-runtimeselfdirectory123
runtime_self_directory_backup="$runtime_path.update-backup.$runtime_self_directory_id"
mkdir "$runtime_self_directory_backup"
if runtime_self_directory_output=$(run_runtime_orchestrator env \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_SELF_CONTAINER=sub2api-container \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  SUB2API_UPDATE_RUNTIME_BACKUP="$runtime_self_directory_backup" \
  SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=true \
  SUB2API_UPDATE_RUNTIME_CHANGED=true \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --finalize-self --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$runtime_self_directory_id" 2>&1); then
  printf 'runtime self finalizer accepted a non-regular backup on its success path\n' >&2
  exit 1
fi
printf '%s' "$runtime_self_directory_output" | grep -Fq 'SUB2API_UPDATE_RUNTIME_BACKUP must be a regular non-symlink file'
[ -d "$runtime_self_directory_backup" ] || {
  printf 'rejected non-regular backup was unexpectedly removed\n' >&2
  exit 1
}
grep -Fq '"status":"failed"' "$status_dir/$runtime_self_directory_id.json"
grep -Fq '"reason":"orchestrator_failed"' "$status_dir/$runtime_self_directory_id.json"
rmdir "$runtime_self_directory_backup"

runtime_self_false_symlink_id=sysop-runtimeselffalsesymlink123
runtime_self_false_symlink_backup="$runtime_path.update-backup.$runtime_self_false_symlink_id"
runtime_self_false_symlink_target="$tmp_root/runtime-self-false-symlink-target"
printf '%s' 'do-not-remove' > "$runtime_self_false_symlink_target"
ln -s "$runtime_self_false_symlink_target" "$runtime_self_false_symlink_backup"
if runtime_self_false_symlink_output=$(run_runtime_orchestrator env \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_SELF_CONTAINER=sub2api-container \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  SUB2API_UPDATE_RUNTIME_BACKUP="$runtime_self_false_symlink_backup" \
  SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=false \
  SUB2API_UPDATE_RUNTIME_CHANGED=true \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --finalize-self --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$runtime_self_false_symlink_id" 2>&1); then
  printf 'runtime self finalizer accepted a backup symlink when no previous binary existed\n' >&2
  exit 1
fi
printf '%s' "$runtime_self_false_symlink_output" | grep -Fq 'must be absent when SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=false'
[ "$(cat "$runtime_self_false_symlink_target")" = do-not-remove ] || {
  printf 'rejected no-previous backup symlink target was modified\n' >&2
  exit 1
}
rm -f "$runtime_self_false_symlink_backup" "$runtime_self_false_symlink_target"

runtime_self_false_file_id=sysop-runtimeselffalsefile123
runtime_self_false_file_backup="$runtime_path.update-backup.$runtime_self_false_file_id"
printf '%s' 'do-not-remove' > "$runtime_self_false_file_backup"
if runtime_self_false_file_output=$(run_runtime_orchestrator env \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_SELF_CONTAINER=sub2api-container \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  SUB2API_UPDATE_RUNTIME_BACKUP="$runtime_self_false_file_backup" \
  SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=false \
  SUB2API_UPDATE_RUNTIME_CHANGED=true \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --finalize-self --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id "$runtime_self_false_file_id" 2>&1); then
  printf 'runtime self finalizer accepted an existing backup when no previous binary existed\n' >&2
  exit 1
fi
printf '%s' "$runtime_self_false_file_output" | grep -Fq 'must be absent when SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=false'
[ "$(cat "$runtime_self_false_file_backup")" = do-not-remove ] || {
  printf 'rejected no-previous backup file was modified\n' >&2
  exit 1
}
rm -f "$runtime_self_false_file_backup"

: > "$runtime_path"
if unclean_runtime_path_output=$(env \
  PATH="$mock_bin:$PATH" \
  SUB2API_UPDATE_MODE=runtime \
  SUB2API_UPDATE_RUNTIME_PATH="$project_dir/nested/../sub2api" \
  SUB2API_UPDATE_RESTART_COMMAND=true \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 2>&1); then
  printf 'runtime updater accepted an unclean binary path\n' >&2
  exit 1
fi
printf '%s' "$unclean_runtime_path_output" | grep -Fq 'SUB2API_UPDATE_RUNTIME_PATH must be a clean absolute path'

if invalid_runtime_backup_output=$(env \
  PATH="$mock_bin:$PATH" \
  SUB2API_UPDATE_MODE=runtime \
  SUB2API_UPDATE_RUNTIME_PATH="$runtime_path" \
  SUB2API_UPDATE_RUNTIME_BACKUP="$tmp_root/unrelated-backup" \
  SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=true \
  SUB2API_UPDATE_RUNTIME_CHANGED=true \
  SUB2API_UPDATE_RESTART_COMMAND=true \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_HEALTH_URLS=http://127.0.0.1:8080/readyz \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --finalize-self --current-version 0.1.241 --target-version 0.1.242 2>&1); then
  printf 'runtime finalizer accepted a backup outside the runtime directory\n' >&2
  exit 1
fi
printf '%s' "$invalid_runtime_backup_output" | grep -Fq 'SUB2API_UPDATE_RUNTIME_BACKUP must be next to SUB2API_UPDATE_RUNTIME_PATH'

runtime_output=$(env \
  PATH="$mock_bin:$PATH" \
  SUB2API_UPDATE_MODE=runtime \
  SUB2API_UPDATE_RUNTIME_PATH="$runtime_path" \
  SUB2API_UPDATE_RUNTIME_BACKUP="$runtime_path.update-backup.12345" \
  SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=false \
  SUB2API_UPDATE_RUNTIME_CHANGED=true \
  SUB2API_UPDATE_RESTART_COMMAND=true \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_HEALTH_URLS=http://127.0.0.1:8080/readyz \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  SUB2API_UPDATE_STATUS_DIR="$status_dir" \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --finalize-self --current-version 0.1.241 --target-version 0.1.242)
printf '%s' "$runtime_output" | grep -Fq 'update completed: 0.1.241 -> 0.1.242'

if command_rejected_output=$(env \
  PATH="$mock_bin:$PATH" \
  SUB2API_UPDATE_MODE=runtime \
  SUB2API_UPDATE_RUNTIME_PATH="$runtime_path" \
  SUB2API_UPDATE_RESTART_COMMAND=true \
  SUB2API_UPDATE_SERVICES=sub2api \
  SUB2API_UPDATE_HEALTH_URLS=http://127.0.0.1:8080/readyz \
  SUB2API_UPDATE_SELF_SERVICE=sub2api \
  SUB2API_UPDATE_SELF_DELAY_SECONDS=0 \
  SUB2API_UPDATE_STATUS_DIR="$status_dir" \
  bash "$repo_root/deploy/update-orchestrator.sh" \
  --current-version 0.1.241 --target-version 0.1.242 \
  --operation-id sysop-commandrejected123 2>&1); then
  printf 'API-triggered runtime command update unexpectedly succeeded\n' >&2
  exit 1
fi
printf '%s' "$command_rejected_output" | grep -Fq 'online runtime updates with a restart command are unsupported'
grep -Fq '"status":"failed"' "$status_dir/sysop-commandrejected123.json"

printf 'update orchestrator test passed\n'
