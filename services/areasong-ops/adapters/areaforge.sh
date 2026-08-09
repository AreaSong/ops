#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

action="${1:-}"
phase="${2:-}"
operation_dir="${3:-}"
target="${4:-}"
source_dir="${5:-}"

UPDATER="${AREAFORGE_OPS_UPDATER:-/opt/areaforge/ops/github-release-updater/areaforge-updater.sh}"
UPDATER_CONFIG="${AREAFORGE_OPS_UPDATER_CONFIG:-/etc/areaforge/updater.env}"
SMOKE="${AREAFORGE_OPS_SMOKE:-/opt/areaforge/ops/update-agent/areaforge-release-readonly-smoke.sh}"
CONTROLLED_COMPOSE="${AREAFORGE_OPS_CONTROLLED_COMPOSE:-/opt/ops/services/areaforge/compose.yml}"
RUNTIME_COMPOSE="${AREAFORGE_OPS_RUNTIME_COMPOSE:-/opt/areaforge/docker-compose.prod.yml}"
ENV_FILE="${AREAFORGE_OPS_ENV_FILE:-/opt/areaforge/.env.production}"
BACKUP_POSTGRES="${AREAFORGE_OPS_BACKUP_POSTGRES:-/opt/ops/scripts/backup/backup-postgres.sh}"
BACKUP_VOLUMES="${AREAFORGE_OPS_BACKUP_VOLUMES:-/opt/ops/scripts/backup/backup-volumes.sh}"
RESTORE_DRILL="${AREAFORGE_OPS_RESTORE_DRILL:-/opt/ops/scripts/backup/restore-areaforge-isolated.sh}"
LATEST_MANIFEST="${AREAFORGE_OPS_LATEST_MANIFEST:-/var/backups/ops/manifests/latest-manifest.txt}"
RESTORE_METRICS="${AREAFORGE_OPS_RESTORE_METRICS:-/var/lib/node_exporter/textfile_collector/areaforge-restore-drill.prom}"
APP_CONTAINER="${AREAFORGE_OPS_APP_CONTAINER:-areaforge-web}"
POSTGRES_CONTAINER="${AREAFORGE_OPS_POSTGRES_CONTAINER:-areaforge-postgres}"
BASE_URL="${AREAFORGE_OPS_BASE_URL:-http://127.0.0.1:3020}"

fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
result() {
  local summary="$1" data="${2:-}"
  [[ -n "$data" ]] || data='{}'
  jq -cn --arg summary "$summary" --argjson data "$data" '{ok:true,summary:$summary,data:$data}'
}
sha256_text() { printf '%s' "$1" | sha256sum | awk '{print "sha256:"$1}'; }
epoch_ms() {
  local value
  value="$(date +%s%3N 2>/dev/null || true)"
  if [[ "$value" =~ ^[0-9]+$ ]]; then printf '%s\n' "$value"; else printf '%s000\n' "$(date +%s)"; fi
}

[[ -d "$operation_dir" && ! -L "$operation_dir" ]] || fail "operation directory is unsafe"
[[ "$action" =~ ^(inspect|check|backup|restart|update|rollback|restore-drill)$ ]] || fail "unsupported action"

require_file() {
  [[ -f "$1" && ! -L "$1" ]] || fail "required regular file is missing: $1"
}

updater_expected_before() {
  AREAFORGE_UPDATER_NO_MAIN=1 AREAFORGE_UPDATER_CONFIG="$UPDATER_CONFIG" \
    bash -c '
      script="$1"
      shift
      set --
      source "$script"
      load_config
      observed_before_json
    ' areasong-ops "$UPDATER"
}

inspect_data() {
  local before health current_image current_image_id app_state postgres_state
  before="$(updater_expected_before | jq -ceS .)"
  health="$(curl -fsS "$BASE_URL/api/health")"
  current_image="$(docker inspect --format '{{.Config.Image}}' "$APP_CONTAINER")"
  current_image_id="$(docker inspect --format '{{.Image}}' "$APP_CONTAINER")"
  app_state="$(docker inspect --format '{{.State.Status}}' "$APP_CONTAINER")"
  postgres_state="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$POSTGRES_CONTAINER")"

  jq -e --arg version "$(jq -r .currentVersion <<<"$before")" \
    --arg image "$(jq -r .currentImage <<<"$before")" \
    --arg currentImage "$current_image" \
    '.ok == true and .service == "AreaForge" and .version == $version and
     .runtimeIdentity.status == "verified" and $image == $currentImage' <<<"$health" >/dev/null ||
    fail "AreaForge env, Docker and runtime identity disagree"

  jq -cnS --argjson before "$before" --arg currentImageId "$current_image_id" \
    --arg runtimeIdentityHash "$(jq -er .runtimeIdentity.identityHash <<<"$health")" \
    --arg gitCommit "$(jq -er .runtimeIdentity.gitCommit <<<"$health")" \
    --arg appState "$app_state" --arg postgresState "$postgres_state" \
    --argjson health "$(jq -c '{ok,service,version,runtimeIdentityStatus:.runtimeIdentity.status}' <<<"$health")" \
    '$before + {currentImageId:$currentImageId,runtimeIdentityHash:$runtimeIdentityHash,
      gitCommit:$gitCommit,appState:$appState,postgresState:$postgresState,health:$health}'
}

contract_before() {
  jq -cS '.expectedBefore | {
    currentVersion,currentImage,currentImageId,runtimeIdentityHash,autoApply,signatureRequired,
    rollbackAvailable,rollbackTargetVersion,rollbackTargetImage,rollbackSourceRecordSha256
  }' "$operation_dir/task-contract.json"
}

assert_expected_before() {
  local expected observed
  expected="$(contract_before)"
  observed="$(inspect_data | jq -cS '{
    currentVersion,currentImage,currentImageId,runtimeIdentityHash,autoApply,signatureRequired,
    rollbackAvailable,rollbackTargetVersion,rollbackTargetImage,rollbackSourceRecordSha256
  }')"
  [[ "$observed" == "$expected" ]] || fail "EXPECTED_BEFORE_MISMATCH"
}

dependency_identity() {
  docker inspect "$POSTGRES_CONTAINER" | jq -ceS '.[0] | {
    name:.Name,id:.Id,startedAt:.State.StartedAt,image:.Config.Image
  }'
}

record_dependency() {
  dependency_identity >"$operation_dir/postgres.before.json"
  chmod 0600 "$operation_dir/postgres.before.json"
}

assert_dependency_unchanged_from() {
  local directory="$1"
  [[ -f "$directory/postgres.before.json" ]] || fail "PostgreSQL dependency baseline is missing"
  [[ "$(dependency_identity)" == "$(jq -ceS . "$directory/postgres.before.json")" ]] ||
    fail "AreaForge PostgreSQL container identity changed"
}

verify_compose_pair() {
  require_file "$CONTROLLED_COMPOSE"
  require_file "$RUNTIME_COMPOSE"
  cmp -s "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" || fail "controlled and runtime Compose files differ"
}

verify_release() {
  local output="$1" selected_tag="${2:-}"
  local args=(check --config "$UPDATER_CONFIG" --identity-json "$output")
  if [[ -n "$selected_tag" ]]; then
    [[ "$selected_tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "release tag is invalid"
    args+=(--tag "$selected_tag")
  fi
  "$UPDATER" "${args[@]}" >/dev/null
  chmod 0600 "$output"
  jq -e 'keys == ["manifestSha256","manifestVersion","releaseId","webImageDigest"] and
    (.releaseId | type == "number" and . > 0) and
    (.manifestSha256 | test("^sha256:[a-f0-9]{64}$")) and
    (.manifestVersion | test("^[0-9]+\\.[0-9]+\\.[0-9]+$")) and
    (.webImageDigest | test("^ghcr\\.io/[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+@sha256:[a-f0-9]{64}$"))' \
    "$output" >/dev/null || fail "verified target identity is invalid"
}

write_guard() {
  local target_identity="$1" expected params now expires expected_projection expected_hash
  local semantic_projection semantic_hash request_projection request_hash task_id actor_hash
  expected="$(jq -cS '.expectedBefore | {
    autoApply,currentImage,currentVersion,rollbackAvailable,rollbackSourceRecordSha256,
    rollbackTargetImage,rollbackTargetVersion,signatureRequired
  }' "$operation_dir/task-contract.json")"
  task_id="$(jq -er .taskId "$operation_dir/task-contract.json")"
  actor_hash="$(jq -er .actorHash "$operation_dir/task-contract.json")"
  params="$(jq -cnS --arg tag "$target" '{autoApply:null,tag:$tag}')"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  expires="$(date -u -d '+9 minutes' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+9M +%Y-%m-%dT%H:%M:%SZ)"
  expected_projection="$(jq -cnS --argjson expectedBefore "$expected" \
    '{domain:"areaforge.update-request.expected-before.v2",expectedBefore:$expectedBefore}')"
  expected_hash="$(sha256_text "$expected_projection")"
  semantic_projection="$(jq -cnS --argjson params "$params" --argjson targetIdentity "$(jq -cS . "$target_identity")" \
    --argjson expectedBefore "$expected" \
    '{domain:"areaforge.update-request.semantic.v2",action:"apply",params:$params,target:$targetIdentity,expectedBefore:$expectedBefore}')"
  semantic_hash="$(sha256_text "$semantic_projection")"
  request_projection="$(jq -cnS --arg id "update_$(epoch_ms)_$task_id" --arg idempotencyKey "$task_id" \
    --arg requestedAt "$now" --arg expiresAt "$expires" --arg actorEmailHash "$actor_hash" \
    --argjson params "$params" --argjson targetIdentity "$(jq -cS . "$target_identity")" \
    --argjson expectedBefore "$expected" --arg expectedBeforeHash "$expected_hash" --arg semanticHash "$semantic_hash" \
    '{domain:"areaforge.update-request.v2",schemaVersion:2,id:$id,idempotencyKey:$idempotencyKey,
      action:"apply",status:"queued",requestedAt:$requestedAt,expiresAt:$expiresAt,
      actorEmailHash:$actorEmailHash,params:$params,target:$targetIdentity,expectedBefore:$expectedBefore,
      expectedBeforeHash:$expectedBeforeHash,semanticHash:$semanticHash}')"
  request_hash="$(sha256_text "$request_projection")"
  jq -cnS --argjson request "$request_projection" --arg requestHash "$request_hash" \
    '$request | del(.domain) + {requestHash:$requestHash}' >"$operation_dir/areaforge-request-v2.json"
  chmod 0600 "$operation_dir/areaforge-request-v2.json"
}

run_updater_apply() {
  local error_file rc reason last_line
  error_file="$(mktemp "$operation_dir/.updater-stderr.XXXXXX")"
  set +e
  "$UPDATER" apply --config "$UPDATER_CONFIG" --tag "$target" --yes \
    --request-guard "$operation_dir/areaforge-request-v2.json" >/dev/null 2>"$error_file"
  rc=$?
  set -e
  if [[ "$rc" -eq 0 ]]; then
    jq -cnS '{exitCode:0,status:"applied",reasonCode:"NONE"}' >"$operation_dir/apply-outcome.json"
    chmod 0600 "$operation_dir/apply-outcome.json"
    rm -f "$error_file"
    return
  fi
  reason="$(grep -Eo 'reasonCode=[A-Z0-9_]+' "$error_file" | tail -n 1 | cut -d= -f2 || true)"
  reason="${reason:-UPDATER_FAILED}"
  jq -cnS --argjson exitCode "$rc" --arg reasonCode "$reason" \
    '{exitCode:$exitCode,status:"failed",reasonCode:$reasonCode}' >"$operation_dir/apply-outcome.json"
  chmod 0600 "$operation_dir/apply-outcome.json"
  last_line="$(tail -n 1 "$error_file")"
  rm -f "$error_file"
  fail "AreaForge updater failed (exit=$rc reason=$reason): $last_line"
}

updater_latest_record() {
  AREAFORGE_UPDATER_NO_MAIN=1 AREAFORGE_UPDATER_CONFIG="$UPDATER_CONFIG" \
    bash -c '
      script="$1"
      shift
      set --
      source "$script"
      load_config
      latest_update_record
    ' areasong-ops "$UPDATER"
}

record_value() { awk -F': ' -v key="$2" '$1 == key {print $2; exit}' "$1"; }

write_update_summary() {
  local record status release_tag previous_version previous_image target_version target_image migration
  record="$(updater_latest_record)"
  require_file "$record"
  status="$(record_value "$record" status)"
  release_tag="$(record_value "$record" releaseTag)"
  previous_version="$(record_value "$record" previousAppVersion)"
  previous_image="$(record_value "$record" previousImage)"
  target_version="$(record_value "$record" targetVersion)"
  target_image="$(record_value "$record" targetWebImageDigest)"
  migration="$(record_value "$record" migrationApplied)"
  [[ "$status" == applied && "$release_tag" == "$target" ]] || fail "latest updater record does not match the applied target"
  [[ "$previous_version" == "$(jq -r .expectedBefore.currentVersion "$operation_dir/task-contract.json")" ]] ||
    fail "updater record previous version mismatch"
  [[ "$previous_image" == "$(jq -r .expectedBefore.currentImage "$operation_dir/task-contract.json")" ]] ||
    fail "updater record previous image mismatch"
  [[ "$target_version" == "$(jq -r .manifestVersion "$operation_dir/target-identity.apply.json")" ]] ||
    fail "updater record target version mismatch"
  [[ "$target_image" == "$(jq -r .webImageDigest "$operation_dir/target-identity.apply.json")" ]] ||
    fail "updater record target image mismatch"
  [[ "$migration" == true || "$migration" == false ]] || fail "updater record migration flag is invalid"
  jq -cnS --arg recordSha256 "sha256:$(sha256sum "$record" | awk '{print $1}')" \
    --arg releaseTag "$release_tag" --arg previousVersion "$previous_version" --arg previousImage "$previous_image" \
    --arg targetVersion "$target_version" --arg targetImage "$target_image" --argjson migrationApplied "$migration" \
    '{recordSha256:$recordSha256,releaseTag:$releaseTag,previousVersion:$previousVersion,
      previousImage:$previousImage,targetVersion:$targetVersion,targetImage:$targetImage,migrationApplied:$migrationApplied}' \
    >"$operation_dir/update-result.json"
  chmod 0600 "$operation_dir/update-result.json"
}

wait_health() {
  local expected_version="$1" body
  for _ in $(seq 1 60); do
    body="$(curl -fsS "$BASE_URL/api/health" 2>/dev/null || true)"
    if jq -e --arg version "$expected_version" \
      '.ok == true and .version == $version and .runtimeIdentity.status == "verified"' <<<"$body" >/dev/null 2>&1; then
      return
    fi
    sleep 2
  done
  fail "AreaForge did not become healthy with the expected version"
}

run_smoke() {
  local expected_version="$1"
  AREAFORGE_SMOKE_EXPECTED_VERSION="$expected_version" "$SMOKE" --config "$UPDATER_CONFIG" >/dev/null
}

recreate_web() {
  AREAFORGE_UPDATER_NO_MAIN=1 AREAFORGE_UPDATER_CONFIG="$UPDATER_CONFIG" \
    bash -c '
      script="$1"
      shift
      set --
      source "$script"
      load_config
      updater_admission_barrier
      locked_state_path="$AREAFORGE_PRODUCTION_STATE_LOCK_FILE"
      acquire_production_state_lock
      updater_release_admission_queue_control
      load_config
      [[ "$AREAFORGE_PRODUCTION_STATE_LOCK_FILE" == "$locked_state_path" ]] ||
        die "production-state lock path changed while acquiring lock"
      require_production_state_lock
      load_runtime_env
      compose config --quiet
      compose up -d --no-deps --force-recreate web
    ' areasong-ops "$UPDATER" >/dev/null
}

restore_application() {
  local directory="$1" old_version old_image
  require_file "$directory/controlled-compose.before.yml"
  require_file "$directory/runtime-compose.before.yml"
  old_version="$(jq -er .expectedBefore.currentVersion "$directory/task-contract.json")"
  old_image="$(jq -er .expectedBefore.currentImage "$directory/task-contract.json")"
  [[ "$old_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]] || fail "rollback version is invalid"
  [[ "$old_image" =~ ^ghcr\.io/[A-Za-z0-9._/-]+:[A-Za-z0-9._-]+@sha256:[a-f0-9]{64}$ ]] || fail "rollback image is invalid"

  AREAFORGE_UPDATER_NO_MAIN=1 AREAFORGE_UPDATER_CONFIG="$UPDATER_CONFIG" \
    bash -c '
      script="$1"; controlled_before="$2"; runtime_before="$3"; controlled="$4"
      runtime="$5"; env_file="$6"; old_version="$7"; old_image="$8"
      shift 8
      set --
      source "$script"
      load_config
      [[ "$AREAFORGE_COMPOSE_FILE" == "$runtime" ]] || die "configured runtime Compose path mismatch"
      [[ "$AREAFORGE_ENV_FILE" == "$env_file" ]] || die "configured env path mismatch"
      updater_admission_barrier
      locked_state_path="$AREAFORGE_PRODUCTION_STATE_LOCK_FILE"
      acquire_production_state_lock
      updater_release_admission_queue_control
      load_config
      [[ "$AREAFORGE_PRODUCTION_STATE_LOCK_FILE" == "$locked_state_path" ]] ||
        die "production-state lock path changed while acquiring lock"
      require_production_state_lock
      install -m 0644 "$controlled_before" "$controlled"
      install -m 0644 "$runtime_before" "$runtime"
      cmp -s "$controlled" "$runtime" || die "rollback Compose copies differ"
      env_set AREAFORGE_IMAGE "$old_image"
      env_set APP_VERSION "$old_version"
      load_runtime_env
      compose config --quiet
      compose up -d --no-deps --force-recreate web
    ' areasong-ops "$UPDATER" "$directory/controlled-compose.before.yml" \
      "$directory/runtime-compose.before.yml" "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" "$ENV_FILE" \
      "$old_version" "$old_image" >/dev/null
}

assert_identity_from() {
  local directory="$1" expected observed
  expected="$(jq -cS '.expectedBefore | {currentVersion,currentImage,currentImageId,runtimeIdentityHash}' \
    "$directory/task-contract.json")"
  observed="$(inspect_data | jq -cS '{currentVersion,currentImage,currentImageId,runtimeIdentityHash}')"
  [[ "$observed" == "$expected" ]] || fail "rollback identity mismatch"
}

assert_rollback_source_is_current() {
  local directory="$1" source_version source_image source_tag current_version current_image
  source_version="$(jq -er .targetVersion "$directory/update-result.json")"
  source_image="$(jq -er .targetImage "$directory/update-result.json")"
  source_tag="$(jq -er .releaseTag "$directory/update-result.json")"
  current_version="$(jq -er .expectedBefore.currentVersion "$operation_dir/task-contract.json")"
  current_image="$(jq -er .expectedBefore.currentImage "$operation_dir/task-contract.json")"
  [[ "$source_tag" == "$(jq -er .target "$directory/task-contract.json")" ]] ||
    fail "rollback source release tag mismatch"
  [[ "$source_version" == "$current_version" && "$source_image" == "$current_image" ]] ||
    fail "rollback source is not the currently deployed release"
}

perform_full_rollback() {
  local directory="$1" old_version current expected
  old_version="$(jq -er .expectedBefore.currentVersion "$directory/task-contract.json")"
  current="$(inspect_data | jq -cS '{currentVersion,currentImage,currentImageId,runtimeIdentityHash}')"
  expected="$(jq -cS '.expectedBefore | {currentVersion,currentImage,currentImageId,runtimeIdentityHash}' \
    "$directory/task-contract.json")"
  if [[ "$current" != "$expected" ]]; then
    restore_application "$directory"
  fi
  wait_health "$old_version"
  run_smoke "$old_version"
  assert_identity_from "$directory"
  assert_dependency_unchanged_from "$directory"
}

latest_manifest_relative() {
  local value
  require_file "$LATEST_MANIFEST"
  value="$(tr -d '\r\n' <"$LATEST_MANIFEST")"
  [[ "$value" =~ ^manifests/backup-set-[0-9]{8}-[0-9]{6}\.json$ ]] || fail "latest backup manifest pointer is invalid"
  printf '%s\n' "$value"
}

case "$action:$phase" in
  inspect:inspect)
    result "AreaForge 运行身份检查完成" "$(inspect_data)"
    ;;
  check:discover)
    verify_release "$operation_dir/target-identity.json"
    current="$(inspect_data | jq -r .currentVersion)"
    latest="$(jq -r .manifestVersion "$operation_dir/target-identity.json")"
    result "AreaForge 签名发布检查完成" "$(jq -cn --arg currentVersion "$current" --arg latestTag "v$latest" \
      --argjson target "$(jq -c . "$operation_dir/target-identity.json")" \
      '{currentVersion:$currentVersion,latestTag:$latestTag,manifestVersion:$target.manifestVersion,
        prepared:true,updateAvailable:($currentVersion != $target.manifestVersion),webImageDigest:$target.webImageDigest}')"
    ;;
  backup:preflight)
    assert_expected_before
    result "备份前运行身份未漂移"
    ;;
  backup:backup)
    "$BACKUP_POSTGRES" >/dev/null
    "$BACKUP_VOLUMES" >/dev/null
    result "PostgreSQL 与卷分类备份完成"
    ;;
  backup:verify)
    assert_expected_before
    result "备份任务退出状态与业务身份检查通过"
    ;;
  restart:preflight)
    assert_expected_before
    verify_compose_pair
    record_dependency
    result "运行身份、Compose 与 PostgreSQL 基线已记录"
    ;;
  restart:restart)
    recreate_web
    assert_dependency_unchanged_from "$operation_dir"
    result "仅重建 AreaForge Web 容器"
    ;;
  restart:health)
    wait_health "$(jq -r .expectedBefore.currentVersion "$operation_dir/task-contract.json")"
    assert_dependency_unchanged_from "$operation_dir"
    result "AreaForge 健康检查通过"
    ;;
  restart:smoke)
    run_smoke "$(jq -r .expectedBefore.currentVersion "$operation_dir/task-contract.json")"
    assert_dependency_unchanged_from "$operation_dir"
    result "AreaForge 认证只读 smoke 通过"
    ;;
  update:preflight)
    for command_name in bash curl docker jq sha256sum awk flock; do command -v "$command_name" >/dev/null || fail "missing command: $command_name"; done
    for path in "$UPDATER" "$UPDATER_CONFIG" "$SMOKE" "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" \
      "$ENV_FILE" "$BACKUP_POSTGRES" "$BACKUP_VOLUMES"; do require_file "$path"; done
    assert_expected_before
    verify_compose_pair
    install -m 0600 "$CONTROLLED_COMPOSE" "$operation_dir/controlled-compose.before.yml"
    install -m 0600 "$RUNTIME_COMPOSE" "$operation_dir/runtime-compose.before.yml"
    record_dependency
    verify_release "$operation_dir/target-identity.preflight.json" "$target"
    [[ "v$(jq -r .manifestVersion "$operation_dir/target-identity.preflight.json")" == "$target" ]] || fail "target tag and manifest version disagree"
    result "expected-before、签名发布、Compose 与依赖身份已核验"
    ;;
  update:backup)
    "$BACKUP_POSTGRES" >/dev/null
    "$BACKUP_VOLUMES" >/dev/null
    assert_expected_before
    result "宿主机 PostgreSQL 与卷 fresh backup 完成"
    ;;
  update:apply)
    assert_expected_before
    assert_dependency_unchanged_from "$operation_dir"
    verify_release "$operation_dir/target-identity.apply.json" "$target"
    cmp -s "$operation_dir/target-identity.preflight.json" "$operation_dir/target-identity.apply.json" ||
      fail "verified release identity changed after preflight"
    write_guard "$operation_dir/target-identity.apply.json"
    run_updater_apply
    write_update_summary
    assert_dependency_unchanged_from "$operation_dir"
    result "事务 updater 完成并保存非敏感发布摘要"
    ;;
  update:health)
    wait_health "$(jq -r .manifestVersion "$operation_dir/target-identity.apply.json")"
    assert_dependency_unchanged_from "$operation_dir"
    result "更新后健康检查通过"
    ;;
  update:smoke)
    run_smoke "$(jq -r .manifestVersion "$operation_dir/target-identity.apply.json")"
    assert_dependency_unchanged_from "$operation_dir"
    result "更新后认证只读 smoke 通过"
    ;;
  update:identity)
    expected_image="$(jq -r .webImageDigest "$operation_dir/target-identity.apply.json")"
    expected_image_id="${expected_image##*@}"
    expected_version="$(jq -r .manifestVersion "$operation_dir/target-identity.apply.json")"
    identity="$(inspect_data)"
    image_id="$(jq -r .currentImageId <<<"$identity")"
    labels="$(docker image inspect "$image_id")"
    jq -e --arg image "$expected_image" --arg imageId "$expected_image_id" --arg version "$expected_version" \
      --arg labelVersion "$(jq -er '.[0].Config.Labels["org.opencontainers.image.version"]' <<<"$labels")" \
      --arg labelCommit "$(jq -er '.[0].Config.Labels["org.opencontainers.image.revision"]' <<<"$labels")" \
      '.currentImage == $image and .currentImageId == $imageId and .currentVersion == $version and
       .currentVersion == $labelVersion and .gitCommit == $labelCommit' \
      <<<"$identity" >/dev/null || fail "target image, OCI labels and runtime identity disagree"
    assert_dependency_unchanged_from "$operation_dir"
    result "版本、镜像摘要、OCI 标签与运行身份一致"
    ;;
  update:rollback)
    if [[ -f "$operation_dir/apply-outcome.json" ]] &&
       [[ "$(jq -r .reasonCode "$operation_dir/apply-outcome.json")" =~ (MIGRATION_STATE_UNCERTAIN|RECOVERY_UNCERTAIN) ]]; then
      fail "updater reported migration or rollback recovery uncertainty"
    fi
    perform_full_rollback "$operation_dir"
    result "更新前应用身份已恢复；未执行数据库恢复"
    ;;
  rollback:preflight)
    [[ -n "$source_dir" && -d "$source_dir" && ! -L "$source_dir" ]] || fail "rollback source is unsafe"
    require_file "$source_dir/task-contract.json"
    require_file "$source_dir/update-result.json"
    require_file "$source_dir/controlled-compose.before.yml"
    require_file "$source_dir/runtime-compose.before.yml"
    [[ "$(jq -r .service "$source_dir/task-contract.json")" == areaforge ]] || fail "rollback source service mismatch"
    [[ "$(jq -r .action "$source_dir/task-contract.json")" == update ]] || fail "rollback source action mismatch"
    assert_expected_before
    assert_rollback_source_is_current "$source_dir"
    record_dependency
    result "受控更新来源、当前身份与回滚产物已核验"
    ;;
  rollback:apply)
    restore_application "$source_dir"
    result "更新前 Web 与 Compose 已恢复；数据库 schema 保持现状"
    ;;
  rollback:health)
    wait_health "$(jq -r .expectedBefore.currentVersion "$source_dir/task-contract.json")"
    result "回滚后健康检查通过"
    ;;
  rollback:smoke)
    run_smoke "$(jq -r .expectedBefore.currentVersion "$source_dir/task-contract.json")"
    result "回滚后认证只读 smoke 通过"
    ;;
  rollback:identity)
    assert_identity_from "$source_dir"
    assert_dependency_unchanged_from "$operation_dir"
    result "回滚后的版本、镜像与运行身份一致" "$(jq -c '{databaseRestored:false,migrationApplied}' "$source_dir/update-result.json")"
    ;;
  restore-drill:preflight)
    assert_expected_before
    manifest="$(latest_manifest_relative)"
    printf '%s\n' "$manifest" >"$operation_dir/restore-manifest.txt"
    date +%s >"$operation_dir/restore-started.epoch"
    chmod 0600 "$operation_dir/restore-manifest.txt" "$operation_dir/restore-started.epoch"
    result "最新完整备份集与生产身份已核验" "$(jq -cn --arg manifest "$manifest" '{manifest:$manifest}')"
    ;;
  restore-drill:drill)
    "$RESTORE_DRILL" --source local --manifest "$(tr -d '\r\n' <"$operation_dir/restore-manifest.txt")" \
      --compare-production >"$operation_dir/restore-drill.log"
    chmod 0600 "$operation_dir/restore-drill.log"
    result "AreaForge 隔离恢复演练完成"
    ;;
  restore-drill:verify)
    require_file "$RESTORE_METRICS"
    started="$(tr -d '\r\n' <"$operation_dir/restore-started.epoch")"
    completed="$(awk '/^areaforge_restore_drill_last_success_timestamp\{source="local"\}/ {print $2}' "$RESTORE_METRICS")"
    [[ "$started" =~ ^[0-9]+$ && "$completed" =~ ^[0-9]+$ && "$completed" -ge "$started" ]] ||
      fail "restore drill success metric was not refreshed"
    grep -Fxq 'AreaForge isolated restore drill completed successfully' "$operation_dir/restore-drill.log" ||
      fail "restore drill completion marker is missing"
    assert_expected_before
    result "隔离恢复结果、成功指标与生产未漂移检查通过"
    ;;
  *)
    fail "unsupported action phase"
    ;;
esac
