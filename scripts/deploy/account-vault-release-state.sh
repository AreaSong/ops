#!/usr/bin/env bash
# shellcheck disable=SC2153 # This library consumes globals from account-vault-release.sh.

write_state_value() {
  local destination="$1"
  local value="$2"
  printf '%s\n' "$value" >"$destination"
  chmod 0600 "$destination"
}

publish_metric() {
  local success="$1"
  local action="$2"
  local image_ref="$3"
  local now temporary escaped_ref
  now="$(date +%s)"
  temporary="${METRIC_OUT}.tmp"
  escaped_ref="${image_ref//\\/\\\\}"
  escaped_ref="${escaped_ref//\"/\\\"}"
  install -d -m 0755 "$(dirname "$METRIC_OUT")"
  {
    echo '# HELP account_vault_release_last_attempt_timestamp Unix timestamp of the latest release action.'
    echo '# TYPE account_vault_release_last_attempt_timestamp gauge'
    printf 'account_vault_release_last_attempt_timestamp{action="%s"} %s\n' "$action" "$now"
    echo '# HELP account_vault_release_last_success Whether the latest release action succeeded.'
    echo '# TYPE account_vault_release_last_success gauge'
    printf 'account_vault_release_last_success{action="%s"} %s\n' "$action" "$success"
    echo '# HELP account_vault_release_duration_seconds Duration of the latest release action.'
    echo '# TYPE account_vault_release_duration_seconds gauge'
    printf 'account_vault_release_duration_seconds{action="%s"} %s\n' "$action" "$((now - STARTED_AT))"
    echo '# HELP account_vault_release_info Current release image reference.'
    echo '# TYPE account_vault_release_info gauge'
    printf 'account_vault_release_info{action="%s",image_ref="%s"} 1\n' "$action" "$escaped_ref"
  } >"$temporary"
  chmod 0644 "$temporary"
  mv "$temporary" "$METRIC_OUT"
}

content_digest() {
  local image_ref="$1"
  local digest="${image_ref##*sha256:}"
  [[ "$digest" =~ ^[0-9a-f]{64}$ ]] || return 1
  printf '%s\n' "$digest"
}

archive_path() {
  local kind="$1"
  local image_ref="$2"
  printf '%s/archive/%s/%s.json\n' "$STATE_DIR" "$kind" "$(content_digest "$image_ref")"
}

archive_approved_artifacts() {
  local path
  if [ -n "$RELEASE_EVIDENCE" ]; then
    path="$(archive_path evidence "$ACTIVE_IMAGE")"
    install -m 0600 "$RELEASE_EVIDENCE" "$path"
  fi
  if [ -n "$APPROVED_ATTESTATION_RECEIPT" ]; then
    path="$(archive_path attestation "$ACTIVE_IMAGE")"
    install -m 0600 "$APPROVED_ATTESTATION_RECEIPT" "$path"
    rm -f "$APPROVED_ATTESTATION_RECEIPT"
  fi
}

copy_archived_artifact() {
  local kind="$1"
  local image_ref="$2"
  local destination="$3"
  local source
  source="$(archive_path "$kind" "$image_ref")"
  if [ -r "$source" ]; then
    install -m 0600 "$source" "$destination"
  fi
}

carry_forward_state() {
  local filename="$1"
  local destination="$2"
  if [ -r "$STATE_DIR/current/$filename" ]; then
    install -m 0600 "$STATE_DIR/current/$filename" "$destination"
  fi
}

record_success() {
  local previous_ref="$1"
  local generation="${STARTED_AT}-$$-${ACTION}-${CHANGE_ID}"
  local pending="$STATE_DIR/generations/.pending-${generation}"
  local final="$STATE_DIR/generations/${generation}"
  local next_link="$STATE_DIR/.current-${generation}"

  install -d -m 0700 "$STATE_DIR/generations" "$STATE_DIR/archive/evidence" "$STATE_DIR/archive/attestation"
  archive_approved_artifacts
  install -d -m 0700 "$pending"
  install -m 0600 "$RUNTIME_COMPOSE" "$pending/current-compose.yml"
  if [ -n "$COMPOSE_BACKUP" ] && [ -r "$COMPOSE_BACKUP" ]; then
    install -m 0600 "$COMPOSE_BACKUP" "$pending/previous-compose.yml"
  fi
  write_state_value "$pending/current-image" "$ACTIVE_IMAGE"
  write_state_value "$pending/previous-image" "$previous_ref"
  write_state_value "$pending/last-change-id" "$CHANGE_ID"
  if [ -n "$ROLE_GRANTS_CHANGE_ID" ]; then
    write_state_value "$pending/last-role-grants-change-id" "$ROLE_GRANTS_CHANGE_ID"
  else
    carry_forward_state last-role-grants-change-id "$pending/last-role-grants-change-id"
  fi
  if [ -n "$APPROVED_BACKUP_MANIFEST" ]; then
    write_state_value "$pending/backup-manifest" "$APPROVED_BACKUP_MANIFEST"
  else
    carry_forward_state backup-manifest "$pending/backup-manifest"
  fi
  copy_archived_artifact evidence "$ACTIVE_IMAGE" "$pending/current-evidence.json"
  copy_archived_artifact evidence "$previous_ref" "$pending/previous-evidence.json"
  copy_archived_artifact attestation "$ACTIVE_IMAGE" "$pending/current-attestation.json"
  copy_archived_artifact attestation "$previous_ref" "$pending/previous-attestation.json"

  publish_metric 1 "$ACTION" "$ACTIVE_IMAGE"
  mv "$pending" "$final"
  ln -s "generations/${generation}" "$next_link"
  python3 -c 'import os, sys; os.replace(sys.argv[1], sys.argv[2])' "$next_link" "$STATE_DIR/current"
}
