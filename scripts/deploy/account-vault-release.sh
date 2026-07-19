#!/usr/bin/env bash
set -Eeuo pipefail

umask 077
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=account-vault-release-state.sh
# shellcheck disable=SC1091
source "${ACCOUNT_VAULT_RELEASE_STATE_HELPER:-$SCRIPT_DIR/account-vault-release-state.sh}"
CONTROLLED_COMPOSE="${ACCOUNT_VAULT_CONTROLLED_COMPOSE:-/opt/ops/services/account-vault/compose.yml}"
RUNTIME_COMPOSE="${ACCOUNT_VAULT_RUNTIME_COMPOSE:-/opt/services/account-vault/compose.yml}"
ENV_FILE="${ACCOUNT_VAULT_ENV_FILE:-/etc/account-vault/account-vault.env}"
STATE_DIR="${ACCOUNT_VAULT_RELEASE_STATE_DIR:-/var/lib/ops/account-vault-release}"
BACKUP_SET_ROOT="${ACCOUNT_VAULT_BACKUP_SET_ROOT:-/var/backups/ops}"
BACKUP_MANIFEST_TOOL="${ACCOUNT_VAULT_BACKUP_MANIFEST_TOOL:-/opt/ops/scripts/backup/backup_manifest.py}"
R2_VERIFY_STATE="${ACCOUNT_VAULT_R2_VERIFY_STATE:-/var/lib/ops/backup-set-r2-verify/state}"
RELEASE_VALIDATOR="${ACCOUNT_VAULT_RELEASE_VALIDATOR:-/opt/ops/scripts/deploy/account-vault-release-validate.py}"
ATTESTATION_VERIFIER="${ACCOUNT_VAULT_ATTESTATION_VERIFIER:-/opt/ops/scripts/deploy/account-vault-attestation-verify.sh}"
# shellcheck disable=SC2034 # Consumed by the sourced release-state library.
METRIC_OUT="${ACCOUNT_VAULT_RELEASE_METRIC_OUT:-/var/lib/node_exporter/textfile_collector/account-vault-release.prom}"
ROLE_PERMISSION_HELPER="${ACCOUNT_VAULT_ROLE_PERMISSION_HELPER:-/opt/ops/scripts/deploy/account-vault-role-permissions.sh}"
LOCK_FILE="${ACCOUNT_VAULT_RELEASE_LOCK_FILE:-/run/lock/account-vault-release.lock}"
MAX_BACKUP_AGE_MINUTES="${ACCOUNT_VAULT_MAX_BACKUP_AGE_MINUTES:-1800}"
MAX_R2_AGE_SECONDS="${ACCOUNT_VAULT_MAX_R2_AGE_SECONDS:-129600}"
RELEASE_WINDOW_ENFORCED="${ACCOUNT_VAULT_RELEASE_WINDOW_ENFORCED:-true}"

ACTION=""
ACTIVE_IMAGE=""
CHANGE_ID=""
RELEASE_EVIDENCE=""
APPROVED_GIT_SHA=""
APPROVED_IMAGE_ID=""
APPROVED_ATTESTATION_RECEIPT=""
APPROVED_BACKUP_MANIFEST=""
APPROVED_ACTION=""
ROLE_GRANTS_APPROVED=false
ROLE_GRANTS_CHANGE_ID=""
OUTSIDE_WINDOW_APPROVED=false
WEB_RECREATED=false
ROLLBACK_REF=""
COMPOSE_BACKUP=""
COMPOSE_EXISTED=false
COMPOSE_INSTALLED=false
OPERATION_COMMITTED=false
# shellcheck disable=SC2034 # Consumed by the sourced release-state library.
STARTED_AT="$(date +%s)"
log() {
  printf '==> %s\n' "$*"
}
fail() {
  printf 'ERROR: %s\n' "$*" >&2
  return 1
}
usage() {
  cat >&2 <<'EOF'
Usage:
  account-vault-release.sh deploy IMAGE_DIGEST --evidence FILE --approve-migration --approve-role-grants --role-grants-change-id ID --change-id ID [--approve-outside-window]
  account-vault-release.sh rollback --approve-rollback --change-id ID [--approve-outside-window]
  account-vault-release.sh verify

IMAGE_DIGEST must be ghcr.io/areasong/sorryiossearch@sha256:<64 lowercase hex>.
EOF
  exit 2
}
is_release_digest() {
  [[ "$1" =~ ^ghcr\.io/areasong/sorryiossearch@sha256:[0-9a-f]{64}$ ]]
}
is_rollback_ref() {
  is_release_digest "$1" || [[ "$1" =~ ^sha256:[0-9a-f]{64}$ ]]
}

parse_common_flags() {
  while [ "$#" -gt 0 ]; do
    case "$1" in
      --change-id)
        [ "$#" -ge 2 ] || usage
        CHANGE_ID="$2"
        shift 2
        ;;
      --evidence)
        [ "$#" -ge 2 ] || usage
        RELEASE_EVIDENCE="$2"
        shift 2
        ;;
      --approve-migration)
        APPROVED_ACTION=deploy
        shift
        ;;
      --approve-role-grants)
        ROLE_GRANTS_APPROVED=true
        shift
        ;;
      --role-grants-change-id)
        [ "$#" -ge 2 ] || usage
        ROLE_GRANTS_CHANGE_ID="$2"
        shift 2
        ;;
      --approve-rollback)
        APPROVED_ACTION=rollback
        shift
        ;;
      --approve-outside-window)
        OUTSIDE_WINDOW_APPROVED=true
        shift
        ;;
      *) usage ;;
    esac
  done
}

parse_args() {
  ACTION="${1:-}"
  case "$ACTION" in
    deploy)
      [ "$#" -ge 2 ] || usage
      ACTIVE_IMAGE="$2"
      shift 2
      parse_common_flags "$@"
      is_release_digest "$ACTIVE_IMAGE" || fail "release image must be an immutable approved GHCR digest"
      ;;
    rollback)
      shift
      parse_common_flags "$@"
      ;;
    verify)
      [ "$#" -eq 1 ] || usage
      ;;
    *) usage ;;
  esac
}

require_commands() {
  local command_name
  for command_name in awk cat chmod cmp cp curl date dirname docker find flock grep gzip id install ln mv python3 rm sha256sum sleep stat tr; do
    command -v "$command_name" >/dev/null 2>&1 || fail "required command is missing: $command_name"
  done
}

require_root() {
  [ "$(id -u)" -eq 0 ] || fail "account-vault release operations must run as root"
}

require_change_approval() {
  [ "$APPROVED_ACTION" = "$ACTION" ] || fail "the explicit approval flag for ${ACTION} is required"
  [[ "$CHANGE_ID" =~ ^[A-Za-z0-9._-]{3,80}$ ]] || fail "a safe --change-id value is required"
  if [ "$ACTION" = deploy ]; then
    [ "$ROLE_GRANTS_APPROVED" = true ] || fail "--approve-role-grants is required for database permission writes"
    [[ "$ROLE_GRANTS_CHANGE_ID" =~ ^[A-Za-z0-9._-]{3,80}$ ]] || \
      fail "a separate --role-grants-change-id value is required"
  fi
}

require_release_attestation() {
  [ -x "$ATTESTATION_VERIFIER" ] || fail "attestation verifier is missing: $ATTESTATION_VERIFIER"
  install -d -m 0700 "$STATE_DIR"
  APPROVED_ATTESTATION_RECEIPT="$STATE_DIR/pending-attestation-${CHANGE_ID}.json"
  "$ATTESTATION_VERIFIER" "$ACTIVE_IMAGE" "$APPROVED_GIT_SHA" "$APPROVED_ATTESTATION_RECEIPT" || \
    fail "GitHub build provenance verification failed"
  require_secure_file "$APPROVED_ATTESTATION_RECEIPT" "attestation verification receipt"
}

require_release_window() {
  local local_time
  [ "$RELEASE_WINDOW_ENFORCED" = true ] || return 0
  local_time="$(TZ=Asia/Shanghai date +%H%M)"
  if [ "$local_time" -lt 2200 ] || [ "$local_time" -ge 2300 ]; then
    [ "$OUTSIDE_WINDOW_APPROVED" = true ] || fail "outside the regular 22:00-23:00 Asia/Shanghai release window"
  fi
}

require_secret_file() {
  local mode owner
  [ -f "$ENV_FILE" ] || fail "release environment file is missing: $ENV_FILE"
  mode="$(stat -c '%a' "$ENV_FILE")"
  owner="$(stat -c '%U:%G' "$ENV_FILE")"
  [ "$mode" = 600 ] || fail "release environment file must use mode 0600"
  [ "$owner" = root:root ] || fail "release environment file must be owned by root:root"
}

require_secure_file() {
  local path="$1"
  local description="$2"
  local mode owner
  [ -f "$path" ] || fail "$description is missing: $path"
  mode="$(stat -c '%a' "$path")"
  owner="$(stat -c '%U:%G' "$path")"
  [ "$mode" = 600 ] || fail "$description must use mode 0600"
  [ "$owner" = root:root ] || fail "$description must be owned by root:root"
}

validate_release_environment() {
  [ -r "$RELEASE_VALIDATOR" ] || fail "release validator is missing: $RELEASE_VALIDATOR"
  python3 "$RELEASE_VALIDATOR" environment "$ENV_FILE"
}

require_release_evidence() {
  [ -n "$RELEASE_EVIDENCE" ] || fail "--evidence is required for deploy"
  require_secure_file "$RELEASE_EVIDENCE" "published release evidence"
  [ -r "$RELEASE_VALIDATOR" ] || fail "release validator is missing: $RELEASE_VALIDATOR"
  IFS=$'\t' read -r APPROVED_GIT_SHA APPROVED_IMAGE_ID < <(
    python3 "$RELEASE_VALIDATOR" evidence "$RELEASE_EVIDENCE" "$ACTIVE_IMAGE"
  ) || fail "published release evidence validation failed"
  [ -n "$APPROVED_GIT_SHA" ] && [ -n "$APPROVED_IMAGE_ID" ] || \
    fail "published release evidence validation failed"
}

acquire_lock() {
  install -d -m 0755 "$(dirname "$LOCK_FILE")"
  exec 9>"$LOCK_FILE"
  flock -n 9 || fail "another Account Vault release operation is running"
}

compose_file() {
  local compose_path="$1"
  shift
  ACCOUNT_VAULT_IMAGE="$ACTIVE_IMAGE" docker compose \
    --env-file "$ENV_FILE" -f "$compose_path" "$@"
}

ensure_image_override() {
  local override="$STATE_DIR/image-override.yml"
  install -d -m 0700 "$STATE_DIR"
  cat >"${override}.tmp" <<EOF
services:
  web:
    image: ${ACTIVE_IMAGE}
EOF
  chmod 0600 "${override}.tmp"
  mv "${override}.tmp" "$override"
  printf '%s\n' "$override"
}

ensure_legacy_health_override() {
  local override="$STATE_DIR/legacy-health-override.yml"
  install -d -m 0700 "$STATE_DIR"
  cat >"${override}.tmp" <<'EOF'
services:
  web:
    healthcheck:
      test: ["CMD", "node", "-e", "fetch('http://127.0.0.1:3001/health').then(r => process.exit(r.ok ? 0 : 1)).catch(() => process.exit(1))"]
EOF
  chmod 0600 "${override}.tmp"
  mv "${override}.tmp" "$override"
  printf '%s\n' "$override"
}

compose_runtime() {
  local image_override legacy_override
  image_override="$(ensure_image_override)"
  if [[ "$ACTIVE_IMAGE" =~ ^sha256: ]]; then
    legacy_override="$(ensure_legacy_health_override)"
    ACCOUNT_VAULT_IMAGE="$ACTIVE_IMAGE" docker compose --env-file "$ENV_FILE" \
      -f "$RUNTIME_COMPOSE" -f "$image_override" -f "$legacy_override" "$@"
  else
    ACCOUNT_VAULT_IMAGE="$ACTIVE_IMAGE" docker compose --env-file "$ENV_FILE" \
      -f "$RUNTIME_COMPOSE" -f "$image_override" "$@"
  fi
}

require_fresh_backups() {
  local pointer manifest_relative manifest manifest_sha backup_relative backup
  local r2_manifest r2_sha r2_timestamp now key value
  pointer="$BACKUP_SET_ROOT/manifests/latest-manifest.txt"
  [ -r "$pointer" ] || fail "latest complete backup-set pointer is missing"
  manifest_relative="$(tr -d '\r\n' <"$pointer")"
  [[ "$manifest_relative" =~ ^manifests/backup-set-[0-9]{8}-[0-9]{6}\.json$ ]] || \
    fail "latest complete backup-set pointer is invalid"
  manifest="$BACKUP_SET_ROOT/$manifest_relative"
  [ -r "$BACKUP_MANIFEST_TOOL" ] || fail "backup manifest verifier is missing"
  python3 "$BACKUP_MANIFEST_TOOL" verify \
    --backup-root "$BACKUP_SET_ROOT" \
    --manifest "$manifest" \
    --role postgres-account-vault >/dev/null || fail "latest Account Vault backup-set artifact failed verification"
  backup_relative="$(python3 "$BACKUP_MANIFEST_TOOL" artifact-field \
    --manifest "$manifest" --role postgres-account-vault --field path)"
  backup="$BACKUP_SET_ROOT/$backup_relative"
  [ -s "$backup" ] || fail "latest Account Vault PostgreSQL backup is empty"
  gzip -t "$backup" || fail "latest Account Vault PostgreSQL backup failed gzip verification"
  [ "$(find "$backup" -mmin "-${MAX_BACKUP_AGE_MINUTES}" -print)" = "$backup" ] || \
    fail "latest manifest-selected Account Vault backup is stale"

  require_secure_file "$R2_VERIFY_STATE" "R2 verification state"
  r2_manifest=""
  r2_sha=""
  r2_timestamp=""
  while IFS='=' read -r key value; do
    case "$key" in
      manifest_relative) r2_manifest="$value" ;;
      manifest_sha256) r2_sha="$value" ;;
      verified_at) r2_timestamp="$value" ;;
    esac
  done <"$R2_VERIFY_STATE"
  manifest_sha="$(sha256sum "$manifest" | awk '{print $1}')"
  [ "$r2_manifest" = "$manifest_relative" ] || fail "R2 verification does not cover the selected local backup set"
  [ "$r2_sha" = "$manifest_sha" ] || fail "R2 verification manifest hash does not match the selected local backup set"
  [[ "$r2_timestamp" =~ ^[0-9]+$ ]] || fail "R2 verification timestamp is invalid"
  now="$(date +%s)"
  [ "$r2_timestamp" -le "$((now + 300))" ] || fail "R2 verification timestamp is unexpectedly in the future"
  [ "$((now - r2_timestamp))" -le "$MAX_R2_AGE_SECONDS" ] || fail "R2 complete-set verification is stale"
  # shellcheck disable=SC2034 # Consumed by the sourced release-state library.
  APPROVED_BACKUP_MANIFEST="$manifest_relative@$manifest_sha"
  log "Fresh manifest-selected local backup and matching R2 verification confirmed."
}

running_image_ref() {
  local configured image_id
  configured="$(docker inspect --format '{{.Config.Image}}' account-vault-web-1 2>/dev/null || true)"
  if is_release_digest "$configured"; then
    printf '%s\n' "$configured"
    return
  fi
  image_id="$(docker inspect --format '{{.Image}}' account-vault-web-1 2>/dev/null || true)"
  is_rollback_ref "$image_id" || fail "cannot determine a content-addressed rollback image"
  printf '%s\n' "$image_id"
}

prepare_compose() {
  local source="${1:-$CONTROLLED_COMPOSE}" backup_dir timestamp
  [ -f "$source" ] || fail "compose source is missing: $source"
  ACCOUNT_VAULT_IMAGE="$ACTIVE_IMAGE" docker compose --env-file "$ENV_FILE" -f "$source" config --quiet

  timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
  backup_dir="${ACCOUNT_VAULT_RELEASE_BACKUP_DIR:-/var/backups/ops/manual}/account-vault-release-${timestamp}-${CHANGE_ID}"
  install -d -m 0700 "$backup_dir"
  COMPOSE_BACKUP=""
  COMPOSE_EXISTED=false
  if [ -f "$RUNTIME_COMPOSE" ]; then
    cp -a "$RUNTIME_COMPOSE" "$backup_dir/compose.yml.before"
    COMPOSE_BACKUP="$backup_dir/compose.yml.before"
    COMPOSE_EXISTED=true
  fi
  install -d -m 0755 "$(dirname "$RUNTIME_COMPOSE")"
  COMPOSE_INSTALLED=true
  install -m 0644 "$source" "$RUNTIME_COMPOSE"
  cmp -s "$source" "$RUNTIME_COMPOSE" || fail "runtime compose differs after installation"
  log "Runtime compose synchronized; rollback snapshot: $backup_dir"
}

restore_runtime_compose() {
  [ "$COMPOSE_INSTALLED" = true ] || return 0
  if [ "$COMPOSE_EXISTED" = true ] && [ -r "$COMPOSE_BACKUP" ]; then
    install -m 0644 "$COMPOSE_BACKUP" "$RUNTIME_COMPOSE"
  else
    rm -f "$RUNTIME_COMPOSE"
  fi
  COMPOSE_INSTALLED=false
}

ensure_image_available() {
  local verify_evidence="${1:-true}" repo_digests image_id image_user revision
  if is_release_digest "$ACTIVE_IMAGE"; then
    docker pull "$ACTIVE_IMAGE" >/dev/null
    repo_digests="$(docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$ACTIVE_IMAGE")"
    grep -Fxq "$ACTIVE_IMAGE" <<<"$repo_digests" || fail "pulled image does not expose the requested RepoDigest"
    image_user="$(docker image inspect --format '{{.Config.User}}' "$ACTIVE_IMAGE")"
    [ "$image_user" = node ] || fail "approved release image must declare the node runtime user"
    if [ "$verify_evidence" = true ]; then
      image_id="$(docker image inspect --format '{{.Id}}' "$ACTIVE_IMAGE")"
      [ "$image_id" = "$APPROVED_IMAGE_ID" ] || fail "pulled image ID does not match published release evidence"
      revision="$(docker image inspect --format '{{index .Config.Labels \"org.opencontainers.image.revision\"}}' "$ACTIVE_IMAGE")"
      [ "$revision" = "$APPROVED_GIT_SHA" ] || fail "image revision does not match published release evidence"
    fi
  else
    docker image inspect "$ACTIVE_IMAGE" >/dev/null
  fi
}

wait_for_web_health() {
  local container_id status attempt
  container_id="$(compose_runtime ps -q web)"
  [ -n "$container_id" ] || fail "Account Vault web container was not created"
  for ((attempt = 1; attempt <= 60; attempt++)); do
    status="$(docker inspect --format '{{.State.Health.Status}}' "$container_id")"
    [ "$status" = healthy ] && return 0
    if [ "$status" = unhealthy ]; then
      fail "Account Vault web container became unhealthy"
      return 1
    fi
    sleep 2
  done
  fail "Account Vault web health check timed out"
}

verify_endpoints() {
  local local_health_path=ready
  [[ "$ACTIVE_IMAGE" =~ ^sha256: ]] && local_health_path=health
  curl --fail --silent --show-error --max-time 10 "http://127.0.0.1:8392/${local_health_path}" >/dev/null
  curl --fail --silent --show-error --max-time 10 "http://127.0.0.1:8392/api/auth/status" >/dev/null
  curl --fail --silent --show-error --max-time 15 https://sorryiossearch.areasong.top/health >/dev/null
}

restore_previous_web() {
  local should_recreate="$WEB_RECREATED"
  [ "$COMPOSE_INSTALLED" = true ] || [ "$WEB_RECREATED" = true ] || return 0
  is_rollback_ref "$ROLLBACK_REF" || return 0
  log "Release failed; restoring the previous Compose snapshot and web image."
  restore_runtime_compose || return 1
  [ "$should_recreate" = true ] || return 0
  ACTIVE_IMAGE="$ROLLBACK_REF"
  ensure_image_available false || return 1
  compose_runtime up -d --no-deps --force-recreate web || return 1
  wait_for_web_health || return 1
  verify_endpoints || return 1
}

cleanup_pending_attestation() {
  local pending="${APPROVED_ATTESTATION_RECEIPT:-}"
  [ -n "$pending" ] || return 0
  case "$pending" in
    "$STATE_DIR"/pending-attestation-*.json) rm -f -- "$pending" ;;
  esac
  APPROVED_ATTESTATION_RECEIPT=""
}

cleanup_failed_operation() {
  local failed_image="${ACTIVE_IMAGE:-unknown}"
  cleanup_pending_attestation || true
  if [ "$OPERATION_COMMITTED" = false ]; then
    restore_previous_web || true
  fi
  publish_metric 0 "${ACTION:-unknown}" "$failed_image" || true
}

on_error() {
  local status=$? failed_image="${ACTIVE_IMAGE:-unknown}"
  trap - ERR HUP INT TERM
  cleanup_failed_operation
  printf 'ERROR: Account Vault %s failed; inspect the release log and retained rollback snapshot.\n' "${ACTION:-operation}" >&2
  exit "$status"
}

on_signal() {
  local signal_name="$1"
  local status="$2"
  trap - ERR HUP INT TERM
  cleanup_failed_operation
  printf 'ERROR: Account Vault %s interrupted by %s; rollback was attempted.\n' \
    "${ACTION:-operation}" "$signal_name" >&2
  exit "$status"
}

deploy_release() {
  require_change_approval
  require_release_window
  validate_release_environment
  require_release_evidence
  require_release_attestation
  require_fresh_backups
  ROLLBACK_REF="$(running_image_ref)"
  ensure_image_available
  prepare_compose "$CONTROLLED_COMPOSE"

  [ -x "$ROLE_PERMISSION_HELPER" ] || fail "role-permission helper is missing: $ROLE_PERMISSION_HELPER"
  log "Applying separately approved runtime-role grants and default privileges."
  "$ROLE_PERMISSION_HELPER" apply "$ENV_FILE"
  log "Applying explicit Prisma migrations with the database management role."
  compose_file "$RUNTIME_COMPOSE" --profile tools run --rm --no-deps migrate
  "$ROLE_PERMISSION_HELPER" apply "$ENV_FILE"
  log "Recreating only the Account Vault web service."
  WEB_RECREATED=true
  compose_runtime up -d --no-deps --force-recreate web
  wait_for_web_health
  verify_endpoints
  record_success "$ROLLBACK_REF"
  OPERATION_COMMITTED=true
}

rollback_release() {
  local old_current
  require_change_approval
  require_release_window
  [ -r "$STATE_DIR/current/previous-image" ] || fail "no previous image is recorded"
  ACTIVE_IMAGE="$(<"$STATE_DIR/current/previous-image")"
  is_rollback_ref "$ACTIVE_IMAGE" || fail "recorded previous image is not content-addressed"
  old_current="$(running_image_ref)"
  ROLLBACK_REF="$old_current"
  ensure_image_available false
  if [ -r "$STATE_DIR/current/previous-compose.yml" ]; then
    prepare_compose "$STATE_DIR/current/previous-compose.yml"
  else
    prepare_compose "$CONTROLLED_COMPOSE"
  fi
  WEB_RECREATED=true
  compose_runtime up -d --no-deps --force-recreate web
  wait_for_web_health
  verify_endpoints
  record_success "$old_current"
  OPERATION_COMMITTED=true
}

verify_current() {
  ACTIVE_IMAGE="$(running_image_ref)"
  wait_for_web_health
  verify_endpoints
  printf 'Account Vault is healthy on %s\n' "$ACTIVE_IMAGE"
}

main() {
  parse_args "$@"
  require_commands
  require_root
  require_secret_file
  acquire_lock
  trap on_error ERR
  trap 'on_signal HUP 129' HUP
  trap 'on_signal INT 130' INT
  trap 'on_signal TERM 143' TERM
  case "$ACTION" in
    deploy) deploy_release ;;
    rollback) rollback_release ;;
    verify) verify_current ;;
  esac
  trap - ERR HUP INT TERM
  log "Account Vault $ACTION completed successfully."
}

main "$@"
