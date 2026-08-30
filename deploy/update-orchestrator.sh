#!/usr/bin/env bash
set -Eeuo pipefail

# Update coordinator for Docker and source deployments. The application invokes
# this script only when UPDATE_STRATEGY=orchestrated. Image mode pulls a new
# image through Compose; runtime mode verifies and atomically swaps a release
# binary, then restarts services through Docker labels. A configured service
# manager command is reserved for manual runs from outside the managed unit.
# Both modes roll services one at a time and restore the previous version when
# a rollout or the detached final readiness check fails.

COMPOSE_FILE="${SUB2API_UPDATE_COMPOSE_FILE:-}"
COMPOSE_PROJECT="${SUB2API_UPDATE_PROJECT:-}"
SERVICES_RAW="${SUB2API_UPDATE_SERVICES:-sub2api}"
HEALTH_URLS_RAW="${SUB2API_UPDATE_HEALTH_URLS:-${SUB2API_UPDATE_HEALTH_URL:-}}"
HEALTH_TIMEOUT="${SUB2API_UPDATE_HEALTH_TIMEOUT_SECONDS:-120}"
DRAIN_TIMEOUT="${SUB2API_UPDATE_DRAIN_TIMEOUT_SECONDS:-610}"
if ! [[ "$DRAIN_TIMEOUT" =~ ^[0-9]+$ ]] || [ "$DRAIN_TIMEOUT" -lt 5 ]; then
  DRAIN_TIMEOUT=610
fi
SELF_FINALIZE_DELAY="${SUB2API_UPDATE_SELF_DELAY_SECONDS:-3}"
if ! [[ "$SELF_FINALIZE_DELAY" =~ ^[0-9]+$ ]]; then
  SELF_FINALIZE_DELAY=3
fi
ENV_FILE="${SUB2API_UPDATE_ENV_FILE:-}"
STATUS_DIR="${SUB2API_UPDATE_STATUS_DIR:-/app/data/update-status}"
STATUS_HEARTBEAT_INTERVAL="${SUB2API_UPDATE_STATUS_HEARTBEAT_SECONDS:-30}"
if ! [[ "$STATUS_HEARTBEAT_INTERVAL" =~ ^[0-9]+$ ]] || \
  [ "$STATUS_HEARTBEAT_INTERVAL" -lt 1 ] || [ "$STATUS_HEARTBEAT_INTERVAL" -gt 300 ]; then
  STATUS_HEARTBEAT_INTERVAL=30
fi
UPDATE_MODE="${SUB2API_UPDATE_MODE:-image}"
RUNTIME_PATH="${SUB2API_UPDATE_RUNTIME_PATH:-}"
REPOSITORY="${SUB2API_UPDATE_REPOSITORY:-jhupo/sub2api}"
RESTART_COMMAND="${SUB2API_UPDATE_RESTART_COMMAND:-}"
HELPER_IMAGE="${SUB2API_UPDATE_HELPER_IMAGE:-${SUB2API_IMAGE:-}}"
HELPER_ACTIVE="${SUB2API_UPDATE_HELPER_ACTIVE:-}"
DOCKER_COMMAND_RAW="${SUB2API_UPDATE_DOCKER_COMMAND:-docker}"
DOCKER=()
RUNTIME_BACKEND=""

CURRENT_VERSION=""
TARGET_VERSION=""
RELEASE_URL=""
OPERATION_ID=""
RUNTIME_BACKUP="${SUB2API_UPDATE_RUNTIME_BACKUP:-}"
RUNTIME_HAD_PREVIOUS="${SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS:-false}"
RUNTIME_CHANGED="${SUB2API_UPDATE_RUNTIME_CHANGED:-false}"
FINALIZE_SELF=false
SELF_UPDATE_SCHEDULED=false
STATUS_INITIALIZED=false
STATUS_FINAL=false
STATUS_HEARTBEAT_PID=""
SELF_CONTAINER="${SUB2API_UPDATE_SELF_CONTAINER:-}"
SELF_SERVICE="${SUB2API_UPDATE_SELF_SERVICE:-}"
AFFECTED_SERVICES=()
backup_dir=""

# Service names are identifiers, so whitespace around comma-separated values
# is unambiguous and can be ignored safely.
SERVICES_RAW="${SERVICES_RAW//[[:space:]]/}"

log() {
  printf '[sub2api-update] %s\n' "$*"
}

fail() {
  printf '[sub2api-update] ERROR: %s\n' "$*" >&2
  exit 1
}

is_clean_absolute_path() {
  local path="$1"
  [[ "$path" = /* ]] || return 1
  [ "$path" != / ] || return 1
  [[ "$path" != *//* ]] || return 1
  [[ "$path" != */./* && "$path" != */. ]] || return 1
  [[ "$path" != */../* && "$path" != */.. ]] || return 1
  [[ "$path" != */ ]] || return 1
}

stop_status_heartbeat() {
  [ -n "$STATUS_HEARTBEAT_PID" ] || return 0
  kill "$STATUS_HEARTBEAT_PID" 2>/dev/null || true
  wait "$STATUS_HEARTBEAT_PID" 2>/dev/null || true
  STATUS_HEARTBEAT_PID=""
}

validate_status_storage() {
  [ -n "$OPERATION_ID" ] || return 0
  [ ! -L "$STATUS_DIR" ] && [ -d "$STATUS_DIR" ] || return 1
  local path="$STATUS_DIR/$OPERATION_ID.json"
  [ ! -L "$path" ] || return 1
  [ ! -e "$path" ] || [ -f "$path" ]
}

prepare_status_storage() {
  [ -n "$OPERATION_ID" ] || return 0
  mkdir -p "$STATUS_DIR" || return 1
  validate_status_storage
}

start_status_heartbeat() {
  [ -n "$OPERATION_ID" ] || return 0
  [ -z "$STATUS_HEARTBEAT_PID" ] || return 0
  local path="$STATUS_DIR/$OPERATION_ID.json"
  validate_status_storage && [ -f "$path" ] || return 1
  local owner_pid=$$
  (
    trap 'exit 0' TERM INT
    local elapsed=0
    while kill -0 "$owner_pid" 2>/dev/null; do
      sleep 1
      kill -0 "$owner_pid" 2>/dev/null || exit 0
      elapsed=$((elapsed + 1))
      [ "$elapsed" -ge "$STATUS_HEARTBEAT_INTERVAL" ] || continue
      if ! validate_status_storage || [ ! -f "$path" ] || ! touch "$path"; then
        kill -TERM "$owner_pid" 2>/dev/null || true
        exit 1
      fi
      elapsed=0
    done
  ) &
  STATUS_HEARTBEAT_PID=$!
}

write_update_status() {
  [ -n "$OPERATION_ID" ] || return 0
  local status="$1"
  local reason="${2:-}"
  local path="$STATUS_DIR/$OPERATION_ID.json"
  local staged
  prepare_status_storage || return 1
  staged="$(mktemp "$STATUS_DIR/.${OPERATION_ID}.tmp.XXXXXX")" || return 1
  if ! (
    umask 022
    printf '{"operation_id":"%s","status":"%s","current_version":"%s","target_version":"%s","reason":"%s"}\n' \
      "$OPERATION_ID" "$status" "$CURRENT_VERSION" "$TARGET_VERSION" "$reason" > "$staged"
  ) || ! chmod 0644 "$staged" || ! validate_status_storage || ! mv -f "$staged" "$path"; then
    rm -f -- "$staged" || true
    return 1
  fi
  STATUS_INITIALIZED=true
  case "$status" in
    succeeded|rolled_back|failed)
      STATUS_FINAL=true
      stop_status_heartbeat
      ;;
  esac
}

ensure_pending_status() {
  if [ -n "$OPERATION_ID" ] && [ "$STATUS_INITIALIZED" != true ]; then
    write_update_status pending
  fi
  start_status_heartbeat
}

on_exit() {
  local exit_status=$?
  if [ "$exit_status" -ne 0 ] && [ -n "$OPERATION_ID" ] && [ "$STATUS_FINAL" != true ]; then
    write_update_status failed orchestrator_failed || true
  fi
  stop_status_heartbeat
  if [ -n "$backup_dir" ]; then
    case "$backup_dir" in
      "${TMPDIR:-/tmp}"/sub2api-update.*) rm -rf -- "$backup_dir" ;;
      *) log "refusing to remove unexpected temporary directory: $backup_dir" ;;
    esac
  fi
  return "$exit_status"
}

configure_docker() {
  read -r -a DOCKER <<< "$DOCKER_COMMAND_RAW"
  [ "${#DOCKER[@]}" -gt 0 ] || fail 'SUB2API_UPDATE_DOCKER_COMMAND is empty'
  command -v "${DOCKER[0]}" >/dev/null 2>&1 || fail "Docker command is not available: ${DOCKER[0]}"
}

docker_cli() {
  "${DOCKER[@]}" "$@"
}

require_docker_access() {
  local output detail
  if output="$(docker_cli ps 2>&1)"; then
    return 0
  fi
  detail="$(printf '%s' "$output" | tr '\n' ' ' | cut -c1-240)"
  fail "Docker CLI cannot access /var/run/docker.sock${detail:+: $detail}; grant the updater user the socket GID with group_add, enable automatic socket-group setup, or set SUB2API_UPDATE_DOCKER_COMMAND"
}

detect_self_container() {
  if [ -z "$SELF_CONTAINER" ] && [ -r /etc/hostname ] && [ "${#DOCKER[@]}" -gt 0 ]; then
    local self_id
    self_id="$(cat /etc/hostname 2>/dev/null || true)"
    if [ -n "$self_id" ]; then
      SELF_CONTAINER="$(docker_cli inspect -f '{{.Name}}' "$self_id" 2>/dev/null | sed 's#^/##' || true)"
      SELF_SERVICE="$(docker_cli inspect -f '{{index .Config.Labels "com.docker.compose.service"}}' "$self_id" 2>/dev/null || true)"
    fi
  fi
}

usage() {
  cat <<'EOF'
Usage: update-orchestrator.sh --current-version X.Y.Z --target-version X.Y.Z [--release-url URL] [--operation-id ID]

Required environment:
  SUB2API_UPDATE_COMPOSE_FILE   Compose file used by the production service

Optional environment:
  SUB2API_UPDATE_MODE            image (default) or runtime
  SUB2API_UPDATE_RUNTIME_PATH    Path visible to this process for runtime mode
  SUB2API_UPDATE_REPOSITORY      GitHub repository (default: jhupo/sub2api)
  SUB2API_UPDATE_PROJECT         Compose project name
  SUB2API_UPDATE_SERVICES        Comma-separated app services, in rollout order
  SUB2API_UPDATE_HEALTH_URLS     Comma-separated health URLs, one per service
  SUB2API_UPDATE_ENV_FILE        Env file used by Compose
  SUB2API_UPDATE_STATUS_DIR      Shared rollout status directory
  SUB2API_UPDATE_STATUS_HEARTBEAT_SECONDS Pending-status lease heartbeat, 1-300 (default: 30)
  SUB2API_UPDATE_DOCKER_COMMAND  Docker command with optional arguments
                                  (default: docker; e.g. sudo -n docker)
  SUB2API_UPDATE_RESTART_COMMAND Command used by manual runtime mode outside Docker;
                                  use {service} as a per-service placeholder
  SUB2API_UPDATE_HELPER_IMAGE    Root helper image for image mode when the env
                                  file is not readable by the application user
  SUB2API_UPDATE_HEALTH_TIMEOUT_SECONDS (default: 120)
  SUB2API_UPDATE_DRAIN_TIMEOUT_SECONDS Health drain/stop grace period (default: 610)
  SUB2API_UPDATE_VERSION_ENV     Variable name used by the image tag (default: SUB2API_VERSION)
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --current-version) CURRENT_VERSION="${2:-}"; shift 2 ;;
    --target-version) TARGET_VERSION="${2:-}"; shift 2 ;;
    --release-url) RELEASE_URL="${2:-}"; shift 2 ;;
    --operation-id) OPERATION_ID="${2:-}"; shift 2 ;;
    --finalize-self) FINALIZE_SELF=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown argument: $1" ;;
  esac
done

[ -n "$CURRENT_VERSION" ] || fail '--current-version is required'
[ -n "$TARGET_VERSION" ] || fail '--target-version is required'
if [[ ! "$CURRENT_VERSION" =~ ^[0-9]+(\.[0-9]+){0,2}$ ]]; then
  fail "invalid current version: $CURRENT_VERSION"
fi
if [[ ! "$TARGET_VERSION" =~ ^[0-9]+(\.[0-9]+){0,2}$ ]]; then
  fail "invalid target version: $TARGET_VERSION"
fi
if [ -n "$OPERATION_ID" ]; then
  if [ "${#OPERATION_ID}" -gt 128 ] || [[ ! "$OPERATION_ID" =~ ^sysop-[A-Za-z0-9-]+$ ]]; then
    fail 'invalid operation id'
  fi
  is_clean_absolute_path "$STATUS_DIR" || fail 'SUB2API_UPDATE_STATUS_DIR must be a clean absolute path'
  prepare_status_storage || fail 'SUB2API_UPDATE_STATUS_DIR and operation status file must be regular non-symlink paths'
fi
trap on_exit EXIT
case "$UPDATE_MODE" in
  image|runtime) ;;
  *) fail "invalid SUB2API_UPDATE_MODE: $UPDATE_MODE (expected image or runtime)" ;;
esac
if [ "$UPDATE_MODE" = image ]; then
  [ -n "$COMPOSE_FILE" ] || fail 'SUB2API_UPDATE_COMPOSE_FILE is not configured for image mode'
  [ -f "$COMPOSE_FILE" ] || fail "compose file not found: $COMPOSE_FILE"
fi
if [ "$UPDATE_MODE" = runtime ]; then
  [ -n "$RUNTIME_PATH" ] || fail 'SUB2API_UPDATE_RUNTIME_PATH is required in runtime mode'
  is_clean_absolute_path "$RUNTIME_PATH" || fail 'SUB2API_UPDATE_RUNTIME_PATH must be a clean absolute path'
  case "$RUNTIME_HAD_PREVIOUS" in true|false) ;; *) fail 'SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS must be true or false' ;; esac
  case "$RUNTIME_CHANGED" in true|false) ;; *) fail 'SUB2API_UPDATE_RUNTIME_CHANGED must be true or false' ;; esac
  if [ -n "$RUNTIME_BACKUP" ]; then
    is_clean_absolute_path "$RUNTIME_BACKUP" || fail 'SUB2API_UPDATE_RUNTIME_BACKUP must be a clean absolute path'
    case "$RUNTIME_BACKUP" in
      "$RUNTIME_PATH.update-backup."*) ;;
      *) fail 'SUB2API_UPDATE_RUNTIME_BACKUP must be next to SUB2API_UPDATE_RUNTIME_PATH' ;;
    esac
    runtime_backup_suffix="${RUNTIME_BACKUP#"$RUNTIME_PATH.update-backup."}"
    if [[ ! "$runtime_backup_suffix" =~ ^([0-9]+|sysop-[A-Za-z0-9-]+)$ ]]; then
      fail 'SUB2API_UPDATE_RUNTIME_BACKUP has an invalid suffix'
    fi
    if [ -n "$OPERATION_ID" ] && [ "$runtime_backup_suffix" != "$OPERATION_ID" ]; then
      fail 'SUB2API_UPDATE_RUNTIME_BACKUP does not match the operation id'
    fi
    if [ "$RUNTIME_HAD_PREVIOUS" = true ]; then
      if [ -L "$RUNTIME_BACKUP" ] || [ ! -f "$RUNTIME_BACKUP" ]; then
        fail 'SUB2API_UPDATE_RUNTIME_BACKUP must be a regular non-symlink file'
      fi
    elif [ -L "$RUNTIME_BACKUP" ] || [ -e "$RUNTIME_BACKUP" ]; then
      fail 'SUB2API_UPDATE_RUNTIME_BACKUP must be absent when SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=false'
    fi
  fi
  if [ "$RUNTIME_CHANGED" = true ] && [ "$RUNTIME_HAD_PREVIOUS" = true ] && [ -z "$RUNTIME_BACKUP" ]; then
    fail 'SUB2API_UPDATE_RUNTIME_BACKUP is required when restoring a previous runtime binary'
  fi
  if [ "$FINALIZE_SELF" = true ] && [ "$RUNTIME_CHANGED" != true ]; then
    fail 'SUB2API_UPDATE_RUNTIME_CHANGED=true is required for runtime finalization'
  fi
  if [ -n "$RESTART_COMMAND" ]; then
    RUNTIME_BACKEND=command
  elif [ -S /var/run/docker.sock ] || [[ -v SUB2API_UPDATE_DOCKER_COMMAND ]]; then
    configure_docker
    require_docker_access
    detect_self_container
    RUNTIME_BACKEND=docker
  else
    fail 'runtime mode requires Docker socket access or SUB2API_UPDATE_RESTART_COMMAND'
  fi
fi

if [ "$UPDATE_MODE" = image ]; then
  configure_docker
  if docker_cli compose version >/dev/null 2>&1; then
    COMPOSE=("${DOCKER[@]}" compose)
  elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE=(docker-compose)
  else
    fail 'docker compose is not available'
  fi
  require_docker_access
  detect_self_container

  compose_args=(-f "$COMPOSE_FILE")
  if [ -n "$COMPOSE_PROJECT" ]; then
    compose_args+=(-p "$COMPOSE_PROJECT")
  fi
  if [ -n "$ENV_FILE" ]; then
    [ -f "$ENV_FILE" ] || fail "env file not found: $ENV_FILE"
    # Secret env files are intentionally root-only. Re-run image updates in a
    # short-lived root helper instead of weakening the host file permissions.
    if [ ! -r "$ENV_FILE" ] && [ "$HELPER_ACTIVE" != 1 ]; then
      [ -n "$HELPER_IMAGE" ] || fail "env file is not readable; set SUB2API_UPDATE_HELPER_IMAGE for image mode"
      if [ -n "$OPERATION_ID" ] && [ -z "$SELF_CONTAINER" ]; then
        fail 'self container is required to share rollout status with the root helper'
      fi
      ensure_pending_status
      log "env file is root-only; delegating image update to $HELPER_IMAGE"
      helper_env=(
        --env SUB2API_UPDATE_HELPER_ACTIVE=1
        --env "SUB2API_UPDATE_STATUS_DIR=$STATUS_DIR"
        --env "SUB2API_UPDATE_SELF_CONTAINER=$SELF_CONTAINER"
        --env "SUB2API_UPDATE_SELF_SERVICE=$SELF_SERVICE"
      )
      for name in SUB2API_UPDATE_MODE SUB2API_UPDATE_COMPOSE_FILE SUB2API_UPDATE_PROJECT \
        SUB2API_UPDATE_SERVICES SUB2API_UPDATE_HEALTH_URLS SUB2API_UPDATE_HEALTH_TIMEOUT_SECONDS \
        SUB2API_UPDATE_DRAIN_TIMEOUT_SECONDS \
        SUB2API_UPDATE_ENV_FILE SUB2API_UPDATE_VERSION_ENV SUB2API_UPDATE_REPOSITORY \
        SUB2API_UPDATE_STATUS_HEARTBEAT_SECONDS \
        SUB2API_UPDATE_HELPER_IMAGE SUB2API_UPDATE_DOCKER_COMMAND \
        SUB2API_UPDATE_AUTO_DOCKER_GROUP; do
        if [[ -v "$name" ]]; then
          helper_env+=(--env "$name=${!name}")
        fi
      done
      helper_mounts=()
      if [ -n "$SELF_CONTAINER" ]; then
        helper_mounts+=(--volumes-from "$SELF_CONTAINER")
      else
        helper_mounts+=(
          -v /var/run/docker.sock:/var/run/docker.sock
          -v "$COMPOSE_FILE:$COMPOSE_FILE:ro"
          -v "$ENV_FILE:$ENV_FILE:ro"
        )
      fi
      helper_args=(
        --current-version "$CURRENT_VERSION"
        --target-version "$TARGET_VERSION"
        --release-url "$RELEASE_URL"
      )
      if [ -n "$OPERATION_ID" ]; then
        helper_args+=(--operation-id "$OPERATION_ID")
      fi
      helper="sub2api-update-root-${TARGET_VERSION//./-}-$$"
      docker_cli run --rm -d --name "$helper" --user 0:0 --network host \
        "${helper_mounts[@]}" \
        "${helper_env[@]}" \
        --entrypoint /usr/local/bin/sub2api-update "$HELPER_IMAGE" \
        "${helper_args[@]}" >/dev/null
      log "update scheduled in detached root helper: $CURRENT_VERSION -> $TARGET_VERSION"
      exit 0
    fi
    compose_args+=(--env-file "$ENV_FILE")
  fi
fi

IFS=',' read -r -a SERVICES <<< "$SERVICES_RAW"
[ "${#SERVICES[@]}" -gt 0 ] || fail 'no services configured'
ORIGINAL_SERVICES=("${SERVICES[@]}")

# A helper started by the request-serving systemd unit remains in that unit's
# cgroup and is killed by systemctl restart before it can verify readiness or
# write terminal status. Keep command mode available for an operator invoking
# this script from an external shell, but reject it for API-triggered rollouts.
if [ "$UPDATE_MODE" = runtime ] && [ "$RUNTIME_BACKEND" = command ] && [ -n "$OPERATION_ID" ]; then
  fail 'online runtime updates with a restart command are unsupported; use the default binary strategy or Docker runtime mode'
fi

# When the updater runs inside one of the services it is about to recreate,
# move that service to the end. The final restart is delegated to a detached
# helper container so the updater process can return its HTTP response first.
if [ -n "$SELF_SERVICE" ]; then
  reordered=()
  self_seen=false
  for service in "${SERVICES[@]}"; do
    service="$(printf '%s' "$service" | xargs)"
    if [ "$service" = "$SELF_SERVICE" ]; then
      self_seen=true
      continue
    fi
    reordered+=("$service")
  done
  if [ "$self_seen" = true ]; then
    reordered+=("$SELF_SERVICE")
    SERVICES=("${reordered[@]}")
  fi
fi

IFS=',' read -r -a HEALTH_URLS <<< "$HEALTH_URLS_RAW"
[ -n "$HEALTH_URLS_RAW" ] || fail 'SUB2API_UPDATE_HEALTH_URLS is required for post-rollout verification'
VERSION_ENV="${SUB2API_UPDATE_VERSION_ENV:-SUB2API_VERSION}"
if [[ ! "$VERSION_ENV" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
  fail "invalid version variable: $VERSION_ENV"
fi

ensure_pending_status

backup_dir="$(mktemp -d "${TMPDIR:-/tmp}/sub2api-update.XXXXXX")"

compose() {
  "${COMPOSE[@]}" "${compose_args[@]}" "$@"
}

compose_with_version() {
  local version="$1"
  shift
  local had_previous=false
  local previous_value=""
  if [[ -v "$VERSION_ENV" ]]; then
    had_previous=true
    previous_value="${!VERSION_ENV}"
  fi
  export "$VERSION_ENV=$version"
  local status=0
  if compose "$@"; then
    status=0
  else
    status=$?
  fi
  if [ "$had_previous" = true ]; then
    export "$VERSION_ENV=$previous_value"
  else
    unset "$VERSION_ENV"
  fi
  return "$status"
}

health_url_for() {
  local index="$1"
  local configured=""
  if [ "${#HEALTH_URLS[@]}" -eq 0 ] || [ -z "${HEALTH_URLS[0]:-}" ]; then
    return 0
  fi
  if [ "$index" -lt "${#HEALTH_URLS[@]}" ]; then
    configured="${HEALTH_URLS[$index]}"
  elif [ "${#HEALTH_URLS[@]}" -eq 1 ]; then
    # Backward-compatible single URL configuration.
    configured="${HEALTH_URLS[0]}"
  fi
  if [ "$configured" = container ] || [ "$configured" = - ]; then
    return 0
  fi
  if [ -n "$configured" ]; then
    printf '%s' "$configured"
  fi
}

health_url_for_service() {
  local service="$1"
  local index=0
  for original in "${ORIGINAL_SERVICES[@]}"; do
    original="$(printf '%s' "$original" | xargs)"
    if [ "$original" = "$service" ]; then
      health_url_for "$index"
      return 0
    fi
    index=$((index + 1))
  done
}

wait_for_health() {
  local url="$1"
  [ -n "$url" ] || return 0
  command -v curl >/dev/null 2>&1 || fail 'curl is required when a health URL is configured'
  local deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
  until curl --fail --silent --show-error --max-time 5 "$url" >/dev/null; do
    if [ "$(date +%s)" -ge "$deadline" ]; then
      return 1
    fi
    sleep 2
  done
}

runtime_container_for_service() {
  local service="$1"
  local container=""
  if [ "$RUNTIME_BACKEND" != docker ]; then
    return 1
  fi
  if [ -n "$COMPOSE_PROJECT" ]; then
    container="$(docker_cli ps -aq \
      --filter "label=com.docker.compose.project=$COMPOSE_PROJECT" \
      --filter "label=com.docker.compose.service=$service")" || return 2
  else
    # A single-container/source deployment can use the service name directly.
    container="$(docker_cli ps -aq --filter "name=^/${service}$")" || return 2
  fi
  container="$(printf '%s' "$container" | head -n 1)"
  [ -n "$container" ] || return 1
  printf '%s' "$container"
}

wait_for_container_health() {
  local service="$1"
  [ "${#DOCKER[@]}" -gt 0 ] || fail 'docker is required for container health checks'
  local deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
  local container=""
  local state=""
  until [ -n "$container" ] && [ "$state" = healthy ]; do
    if [ "$UPDATE_MODE" = runtime ]; then
      container="$(runtime_container_for_service "$service" 2>/dev/null || true)"
    else
      container="$(compose ps -q "$service" 2>/dev/null | tail -n 1)"
    fi
    if [ -n "$container" ]; then
      state="$(docker_cli inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$container" 2>/dev/null || true)"
    fi
    if [ "$state" = unhealthy ] || [ "$state" = exited ] || [ "$state" = dead ]; then
      return 1
    fi
    if [ "$(date +%s)" -ge "$deadline" ]; then
      return 1
    fi
    [ "$state" = healthy ] || sleep 2
  done
}

wait_for_service() {
  local service="$1"
  local url
  url="$(health_url_for_service "$service")"
  if [ -n "$url" ]; then
    wait_for_health "$url"
  elif [ "$UPDATE_MODE" = runtime ] && [ "$RUNTIME_BACKEND" = command ]; then
    fail "health URL is required for runtime service $service when using a restart command"
  else
    wait_for_container_health "$service"
  fi
}

graceful_restart_container() {
  local container="$1"
  local timeout="$DRAIN_TIMEOUT"
  if ! [[ "$timeout" =~ ^[0-9]+$ ]] || [ "$timeout" -lt 5 ]; then
    timeout=610
  fi
  # SIGTERM makes the Go server close its listener first, so the load balancer
  # sends new requests to another replica while existing streams drain. Keep
  # this hard deadline above SERVER_SHUTDOWN_TIMEOUT_SECONDS.
  docker_cli stop --time "$timeout" "$container" >/dev/null || return 1
  docker_cli start "$container" >/dev/null || return 1
}

download_runtime_binary() {
  command -v curl >/dev/null 2>&1 || fail 'curl is required for runtime updates'
  command -v tar >/dev/null 2>&1 || fail 'tar is required for runtime updates'
  command -v sha256sum >/dev/null 2>&1 || fail 'sha256sum is required for runtime updates'

  local os arch archive base_url archive_path checksum_path expected actual extracted
  case "$(uname -s)" in
    Linux) os=linux ;;
    Darwin) os=darwin ;;
    *) fail "unsupported update operating system: $(uname -s)" ;;
  esac
  case "$(uname -m)" in
    x86_64|amd64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) fail "unsupported update architecture: $(uname -m)" ;;
  esac

  archive="sub2api_${TARGET_VERSION}_${os}_${arch}.tar.gz"
  base_url="https://github.com/${REPOSITORY}/releases/download/v${TARGET_VERSION}"
  archive_path="$backup_dir/$archive"
  checksum_path="$backup_dir/checksums.txt"

  log "downloading runtime asset $archive"
  curl --fail --silent --show-error --location --retry 3 --connect-timeout 15 --max-time 900 \
    "$base_url/$archive" -o "$archive_path"
  curl --fail --silent --show-error --location --retry 3 --connect-timeout 15 --max-time 60 \
    "$base_url/checksums.txt" -o "$checksum_path"

  expected="$(awk -v file="$archive" '$2 == file { print $1; exit }' "$checksum_path")"
  [ -n "$expected" ] || fail "checksum entry not found for $archive"
  actual="$(sha256sum "$archive_path" | awk '{print $1}')"
  [ "$actual" = "$expected" ] || fail "checksum mismatch for $archive"

  tar -xzf "$archive_path" -C "$backup_dir"
  extracted="$backup_dir/sub2api"
  [ -f "$extracted" ] || fail "release archive did not contain sub2api"
  chmod 0755 "$extracted"

  local runtime_dir backup_staged staged
  runtime_dir="$(dirname "$RUNTIME_PATH")"
  mkdir -p "$runtime_dir"
  RUNTIME_BACKUP="$RUNTIME_PATH.update-backup.${OPERATION_ID:-$$}"
  [ ! -e "$RUNTIME_BACKUP" ] && [ ! -L "$RUNTIME_BACKUP" ] || fail "runtime backup already exists: $RUNTIME_BACKUP"
  RUNTIME_HAD_PREVIOUS=false
  RUNTIME_CHANGED=false
  staged="$(mktemp "$RUNTIME_PATH.tmp.XXXXXX")"
  if ! cp "$extracted" "$staged" || ! chmod 0755 "$staged"; then
    rm -f "$staged" || true
    return 1
  fi
  if [ -f "$RUNTIME_PATH" ]; then
    backup_staged="$(mktemp "$RUNTIME_BACKUP.tmp.XXXXXX")"
    if ! cp "$RUNTIME_PATH" "$backup_staged" ||
      ! chmod 0755 "$backup_staged" ||
      ! mv -f "$backup_staged" "$RUNTIME_BACKUP"; then
      rm -f "$backup_staged" || true
      rm -f "$staged" || true
      return 1
    fi
    RUNTIME_HAD_PREVIOUS=true
  fi
  if ! mv -f "$staged" "$RUNTIME_PATH"; then
    rm -f "$staged" || true
    cleanup_runtime_backup || true
    return 1
  fi
  RUNTIME_CHANGED=true
}

restore_runtime() {
  [ "$RUNTIME_CHANGED" = true ] || return 0
  if [ "$RUNTIME_HAD_PREVIOUS" = true ]; then
    [ ! -L "$RUNTIME_BACKUP" ] && [ -f "$RUNTIME_BACKUP" ] || return 1
    local staged
    staged="$(mktemp "$RUNTIME_PATH.rollback.XXXXXX")" || return 1
    if ! cp "$RUNTIME_BACKUP" "$staged"; then
      rm -f "$staged" || true
      return 1
    fi
    if ! chmod 0755 "$staged"; then
      rm -f "$staged" || true
      return 1
    fi
    if ! mv -f "$staged" "$RUNTIME_PATH"; then
      rm -f "$staged" || true
      return 1
    fi
  else
    rm -f "$RUNTIME_PATH" || return 1
  fi
  RUNTIME_CHANGED=false
}

cleanup_runtime_backup() {
  [ -n "$RUNTIME_BACKUP" ] || return 0
  rm -f "$RUNTIME_BACKUP"
}

rollback() {
  log "rolling back to $CURRENT_VERSION"
  local rollback_failed=false
  if [ "$UPDATE_MODE" = runtime ]; then
    if ! restore_runtime; then
      log 'runtime binary could not be restored'
      rollback_failed=true
    fi
  fi
  local index service
  for ((index=${#AFFECTED_SERVICES[@]} - 1; index >= 0; index--)); do
    service="${AFFECTED_SERVICES[$index]}"
    log "restarting $service at $CURRENT_VERSION"
    if [ "$UPDATE_MODE" = runtime ]; then
      if ! restart_service "$service"; then
        log "$service could not be restarted at $CURRENT_VERSION"
        rollback_failed=true
        continue
      fi
    else
      if ! compose_with_version "$CURRENT_VERSION" up --timeout "$DRAIN_TIMEOUT" -d --no-deps --force-recreate "$service"; then
        log "$service could not be recreated at $CURRENT_VERSION"
        rollback_failed=true
        continue
      fi
    fi
    if ! wait_for_service "$service"; then
      log "$service did not become ready at $CURRENT_VERSION"
      rollback_failed=true
    fi
  done
  if [ "$rollback_failed" != true ] && [ "$UPDATE_MODE" = runtime ]; then
    if ! cleanup_runtime_backup; then
      log 'restored runtime backup could not be removed'
    fi
  fi
  [ "$rollback_failed" != true ]
}

fail_after_rollback() {
  local failure="$1"
  if rollback; then
    write_update_status rolled_back rollout_failed
    fail "$failure; previous version restored"
  fi
  write_update_status failed rollback_incomplete || true
  fail "$failure and rollback was incomplete"
}

schedule_self_restart() {
  if [ "$UPDATE_MODE" = runtime ] && [ "$RUNTIME_BACKEND" = command ]; then
    local -a command_helper_args
    command_helper_args=(
      --finalize-self
      --current-version "$CURRENT_VERSION"
      --target-version "$TARGET_VERSION"
      --release-url "$RELEASE_URL"
    )
    if [ -n "$OPERATION_ID" ]; then
      command_helper_args+=(--operation-id "$OPERATION_ID")
    fi
    log "scheduling detached restart command for $SELF_SERVICE"
    nohup env \
      SUB2API_UPDATE_MODE=runtime \
      "SUB2API_UPDATE_RUNTIME_PATH=$RUNTIME_PATH" \
      "SUB2API_UPDATE_RUNTIME_BACKUP=$RUNTIME_BACKUP" \
      "SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=$RUNTIME_HAD_PREVIOUS" \
      "SUB2API_UPDATE_RUNTIME_CHANGED=$RUNTIME_CHANGED" \
      "SUB2API_UPDATE_SERVICES=$SERVICES_RAW" \
      "SUB2API_UPDATE_HEALTH_URLS=$HEALTH_URLS_RAW" \
      "SUB2API_UPDATE_HEALTH_TIMEOUT_SECONDS=$HEALTH_TIMEOUT" \
      "SUB2API_UPDATE_DRAIN_TIMEOUT_SECONDS=$DRAIN_TIMEOUT" \
      "SUB2API_UPDATE_SELF_DELAY_SECONDS=$SELF_FINALIZE_DELAY" \
      "SUB2API_UPDATE_RESTART_COMMAND=$RESTART_COMMAND" \
      "SUB2API_UPDATE_SELF_SERVICE=$SELF_SERVICE" \
      "SUB2API_UPDATE_STATUS_DIR=$STATUS_DIR" \
      "SUB2API_UPDATE_STATUS_HEARTBEAT_SECONDS=$STATUS_HEARTBEAT_INTERVAL" \
      "$0" "${command_helper_args[@]}" >/dev/null 2>&1 </dev/null &
    SELF_UPDATE_SCHEDULED=true
    return 0
  fi
  if [ -z "$SELF_CONTAINER" ]; then
    log 'self container could not be identified for final restart'
    return 1
  fi
  if [ "${#DOCKER[@]}" -eq 0 ]; then
    log 'docker is required for the final service restart'
    return 1
  fi
  local image helper
  image="$(docker_cli inspect -f '{{.Config.Image}}' "$SELF_CONTAINER" 2>/dev/null || true)"
  if [ -z "$image" ]; then
    log "could not determine image for $SELF_CONTAINER"
    return 1
  fi
  helper="sub2api-update-self-${TARGET_VERSION//./-}-$$"
  if [ "$UPDATE_MODE" = runtime ]; then
    log "scheduling detached restart for $SELF_CONTAINER"
    local -a runtime_helper_args
    runtime_helper_args=(
      --finalize-self
      --current-version "$CURRENT_VERSION"
      --target-version "$TARGET_VERSION"
      --release-url "$RELEASE_URL"
    )
    if [ -n "$OPERATION_ID" ]; then
      runtime_helper_args+=(--operation-id "$OPERATION_ID")
    fi
    if ! docker_cli run --rm -d --name "$helper" \
      --user 0:0 --network host \
      --volumes-from "$SELF_CONTAINER" \
      --env SUB2API_UPDATE_MODE=runtime \
      --env "SUB2API_UPDATE_RUNTIME_PATH=$RUNTIME_PATH" \
      --env "SUB2API_UPDATE_RUNTIME_BACKUP=$RUNTIME_BACKUP" \
      --env "SUB2API_UPDATE_RUNTIME_HAD_PREVIOUS=$RUNTIME_HAD_PREVIOUS" \
      --env "SUB2API_UPDATE_RUNTIME_CHANGED=$RUNTIME_CHANGED" \
      --env "SUB2API_UPDATE_PROJECT=$COMPOSE_PROJECT" \
      --env "SUB2API_UPDATE_SERVICES=$SERVICES_RAW" \
      --env "SUB2API_UPDATE_HEALTH_URLS=$HEALTH_URLS_RAW" \
      --env "SUB2API_UPDATE_HEALTH_TIMEOUT_SECONDS=$HEALTH_TIMEOUT" \
      --env "SUB2API_UPDATE_DRAIN_TIMEOUT_SECONDS=$DRAIN_TIMEOUT" \
      --env "SUB2API_UPDATE_SELF_DELAY_SECONDS=$SELF_FINALIZE_DELAY" \
      --env "SUB2API_UPDATE_STATUS_DIR=$STATUS_DIR" \
      --env "SUB2API_UPDATE_STATUS_HEARTBEAT_SECONDS=$STATUS_HEARTBEAT_INTERVAL" \
      --env "SUB2API_UPDATE_SELF_CONTAINER=$SELF_CONTAINER" \
      --env "SUB2API_UPDATE_SELF_SERVICE=$SELF_SERVICE" \
      --env "SUB2API_UPDATE_DOCKER_COMMAND=$DOCKER_COMMAND_RAW" \
      --entrypoint /usr/local/bin/sub2api-update "$image" \
      "${runtime_helper_args[@]}" >/dev/null; then
      log 'detached runtime finalizer could not be started'
      return 1
    fi
    SELF_UPDATE_SCHEDULED=true
    return 0
  fi

  local -a helper_env helper_mounts
  helper_env=(
    --env SUB2API_UPDATE_HELPER_ACTIVE=1
    --env SUB2API_UPDATE_MODE=image
    --env "SUB2API_UPDATE_COMPOSE_FILE=$COMPOSE_FILE"
    --env "SUB2API_UPDATE_PROJECT=$COMPOSE_PROJECT"
    --env "SUB2API_UPDATE_SERVICES=$SERVICES_RAW"
    --env "SUB2API_UPDATE_HEALTH_URLS=$HEALTH_URLS_RAW"
    --env "SUB2API_UPDATE_HEALTH_TIMEOUT_SECONDS=$HEALTH_TIMEOUT"
    --env "SUB2API_UPDATE_DRAIN_TIMEOUT_SECONDS=$DRAIN_TIMEOUT"
    --env "SUB2API_UPDATE_SELF_DELAY_SECONDS=$SELF_FINALIZE_DELAY"
    --env "SUB2API_UPDATE_VERSION_ENV=$VERSION_ENV"
    --env "SUB2API_UPDATE_REPOSITORY=$REPOSITORY"
    --env "SUB2API_UPDATE_STATUS_DIR=$STATUS_DIR"
    --env "SUB2API_UPDATE_STATUS_HEARTBEAT_SECONDS=$STATUS_HEARTBEAT_INTERVAL"
    --env "SUB2API_UPDATE_SELF_CONTAINER=$SELF_CONTAINER"
    --env "SUB2API_UPDATE_SELF_SERVICE=$SELF_SERVICE"
    --env "SUB2API_UPDATE_DOCKER_COMMAND=$DOCKER_COMMAND_RAW"
  )
  helper_mounts=(
    --volumes-from "$SELF_CONTAINER"
  )
  if [ -n "$ENV_FILE" ]; then
    helper_env+=(--env "SUB2API_UPDATE_ENV_FILE=$ENV_FILE")
  fi
  local -a helper_args
  helper_args=(
    --finalize-self
    --current-version "$CURRENT_VERSION"
    --target-version "$TARGET_VERSION"
    --release-url "$RELEASE_URL"
  )
  if [ -n "$OPERATION_ID" ]; then
    helper_args+=(--operation-id "$OPERATION_ID")
  fi

  log "scheduling detached Compose recreation for $SELF_CONTAINER"
  if ! docker_cli run --rm -d --name "$helper" \
    --user 0:0 --network host \
    "${helper_mounts[@]}" \
    "${helper_env[@]}" \
    --entrypoint /usr/local/bin/sub2api-update "$image" \
    "${helper_args[@]}" >/dev/null; then
    log 'detached Compose finalizer could not be started'
    return 1
  fi
  SELF_UPDATE_SCHEDULED=true
}

restart_service() {
  local service="$1"
  if [ "$UPDATE_MODE" = runtime ]; then
    if [ "$RUNTIME_BACKEND" = docker ]; then
      local container
      local lookup_status=0
      if container="$(runtime_container_for_service "$service")"; then
        :
      else
        lookup_status=$?
        if [ "$lookup_status" -eq 2 ]; then
          log "Docker CLI lost access while locating runtime service $service; check Docker access"
        else
          log "container not found for runtime service: $service"
        fi
        return 1
      fi
      log "restarting container $container ($service)"
      graceful_restart_container "$container"
    else
      local command="${RESTART_COMMAND//\{service\}/$service}"
      log "running restart command for $service"
      sh -c "$command"
    fi
  else
    compose_with_version "$TARGET_VERSION" up --timeout "$DRAIN_TIMEOUT" -d --no-deps --force-recreate "$service"
  fi
}

finalize_self_image() {
  [ "$UPDATE_MODE" = image ] || fail '--finalize-self is only supported in image mode'
  [ -n "$SELF_SERVICE" ] || fail 'self service is required for final recreation'
  sleep "$SELF_FINALIZE_DELAY"
  log "recreating $SELF_SERVICE at $TARGET_VERSION"
  if compose_with_version "$TARGET_VERSION" up --timeout "$DRAIN_TIMEOUT" -d --no-deps --force-recreate "$SELF_SERVICE" &&
    wait_for_service "$SELF_SERVICE"; then
    write_update_status succeeded
    log "update completed: $CURRENT_VERSION -> $TARGET_VERSION"
    return 0
  fi

  log 'self health check failed; restoring all services to the previous version'
  local rollback_failed=false
  local index service
  for ((index=${#SERVICES[@]}-1; index>=0; index--)); do
    service="$(printf '%s' "${SERVICES[$index]}" | xargs)"
    [ -n "$service" ] || continue
    log "recreating $service at $CURRENT_VERSION"
    if ! compose_with_version "$CURRENT_VERSION" up --timeout "$DRAIN_TIMEOUT" -d --no-deps --force-recreate "$service"; then
      log "failed to recreate $service at $CURRENT_VERSION"
      rollback_failed=true
      continue
    fi
    if ! wait_for_service "$service"; then
      log "service $service did not become ready after rollback"
      rollback_failed=true
    fi
  done
  if [ "$rollback_failed" = true ]; then
    write_update_status failed rollback_incomplete || true
    fail 'self update failed and one or more services could not be restored'
  fi
  write_update_status rolled_back target_not_ready
  fail 'self update failed; all services restored to the previous version'
}

finalize_self_runtime() {
  [ "$UPDATE_MODE" = runtime ] || fail '--finalize-self runtime mode mismatch'
  [ -n "$SELF_SERVICE" ] || fail 'self service is required for final restart'
  sleep "$SELF_FINALIZE_DELAY"
  log "restarting $SELF_SERVICE at $TARGET_VERSION"
  if restart_service "$SELF_SERVICE" && wait_for_service "$SELF_SERVICE"; then
    write_update_status succeeded
    if ! cleanup_runtime_backup; then
      log 'runtime backup could not be removed after successful update'
    fi
    log "update completed: $CURRENT_VERSION -> $TARGET_VERSION"
    return 0
  fi

  log 'runtime self health check failed; restoring the previous binary'
  local rollback_failed=false
  if ! restore_runtime; then
    log 'runtime binary could not be restored'
    rollback_failed=true
  else
    local index service
    for ((index=${#SERVICES[@]}-1; index>=0; index--)); do
      service="$(printf '%s' "${SERVICES[$index]}" | xargs)"
      [ -n "$service" ] || continue
      log "restarting $service at $CURRENT_VERSION"
      if ! restart_service "$service"; then
        log "$service could not be restarted at $CURRENT_VERSION"
        rollback_failed=true
        continue
      fi
      if ! wait_for_service "$service"; then
        log "$service did not become ready at $CURRENT_VERSION"
        rollback_failed=true
      fi
    done
  fi
  if [ "$rollback_failed" = true ]; then
    write_update_status failed rollback_incomplete || true
    fail 'runtime self update failed and rollback was incomplete'
  fi
  if ! cleanup_runtime_backup; then
    log 'restored runtime backup could not be removed'
  fi
  write_update_status rolled_back target_not_ready
  fail 'runtime self update failed; previous version restored'
}

if [ "$FINALIZE_SELF" = true ]; then
  if [ "$UPDATE_MODE" = image ]; then
    finalize_self_image
  else
    finalize_self_runtime
  fi
  exit 0
fi

if [ "$UPDATE_MODE" = runtime ]; then
  download_runtime_binary
else
  log "pulling $TARGET_VERSION from $RELEASE_URL"
  if ! compose_with_version "$TARGET_VERSION" pull "${SERVICES[@]}"; then
    fail 'image pull failed; no services were changed'
  fi
fi

for service in "${SERVICES[@]}"; do
  service="$(printf '%s' "$service" | xargs)"
  [ -n "$service" ] || continue
  log "rolling $service to $TARGET_VERSION"
  if [ "$UPDATE_MODE" = runtime ] && [ "$RUNTIME_BACKEND" = command ] && [ -z "$SELF_SERVICE" ] && [ "${#SERVICES[@]}" -eq 1 ]; then
    SELF_SERVICE="$service"
  fi
  if [ -n "$SELF_SERVICE" ] && [ "$service" = "$SELF_SERVICE" ]; then
    if ! schedule_self_restart; then
      log 'self restart scheduling failed; starting rollback'
      fail_after_rollback 'self restart scheduling failed'
    fi
    continue
  fi
  AFFECTED_SERVICES+=("$service")
  if ! restart_service "$service"; then
    log 'rollout command failed; starting rollback'
    fail_after_rollback 'rollout failed'
  fi
  if ! wait_for_service "$service"; then
    log "health check failed for $service; starting rollback"
    fail_after_rollback 'health check failed'
  fi
done

if [ "$SELF_UPDATE_SCHEDULED" = true ]; then
  log "update scheduled: $CURRENT_VERSION -> $TARGET_VERSION"
else
  write_update_status succeeded
  if [ "$UPDATE_MODE" = runtime ] && ! cleanup_runtime_backup; then
    log 'runtime backup could not be removed after successful update'
  fi
  log "update completed: $CURRENT_VERSION -> $TARGET_VERSION"
fi
