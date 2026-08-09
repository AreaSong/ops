#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

action="${1:-}"
phase="${2:-}"
operation_dir="${3:-}"
target="${4:-}"
source_dir="${5:-}"

RUNTIME_COMPOSE="${SUB2API_OPS_RUNTIME_COMPOSE:-/opt/services/sub2api/compose.yml}"
CONTROLLED_COMPOSE="${SUB2API_OPS_CONTROLLED_COMPOSE:-/opt/ops/services/sub2api/compose.yml}"
ENV_FILE="${SUB2API_OPS_ENV_FILE:-/opt/services/sub2api/.env}"
RELEASES="${SUB2API_OPS_RELEASES:-/opt/ops/scripts/deploy/update-control/releases/sub2api.json}"
LEGACY_ADAPTER="${SUB2API_OPS_UPDATE_ADAPTER:-/opt/ops/scripts/deploy/update-control/adapters/sub2api.sh}"
BACKUP_POSTGRES="${SUB2API_OPS_BACKUP_POSTGRES:-/opt/ops/scripts/backup/backup-postgres.sh}"
BACKUP_REDIS="${SUB2API_OPS_BACKUP_REDIS:-/opt/ops/scripts/backup/backup-redis.sh}"
BACKUP_VOLUMES="${SUB2API_OPS_BACKUP_VOLUMES:-/opt/ops/scripts/backup/backup-volumes.sh}"
APP_CONTAINER="${SUB2API_OPS_APP_CONTAINER:-sub2api}"
POSTGRES_CONTAINER="${SUB2API_OPS_POSTGRES_CONTAINER:-sub2api-postgres}"
REDIS_CONTAINER="${SUB2API_OPS_REDIS_CONTAINER:-sub2api-redis}"
BASE_URL="${SUB2API_OPS_BASE_URL:-http://127.0.0.1:8080}"

fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
result() {
  local summary="$1" data="${2:-}"
  [[ -n "$data" ]] || data='{}'
  jq -cn --arg summary "$summary" --argjson data "$data" '{ok:true,summary:$summary,data:$data}'
}

[[ -d "$operation_dir" && ! -L "$operation_dir" ]] || fail "operation directory is unsafe"
[[ "$action" =~ ^(inspect|check|backup|restart|update|rollback|restore-drill)$ ]] || fail "unsupported action"

postgres_value() {
  local query="$1" user database
  user="$(docker inspect "$POSTGRES_CONTAINER" | jq -er '.[0].Config.Env[] | select(startswith("POSTGRES_USER=")) | split("=")[1]')"
  database="$(docker inspect "$POSTGRES_CONTAINER" | jq -er '.[0].Config.Env[] | select(startswith("POSTGRES_DB=")) | split("=")[1]')"
  docker exec "$POSTGRES_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$user" -d "$database" -Atc "$query"
}

runtime_identity() {
  local image image_id labels version commit projection identity_hash
  image="$(docker inspect --format '{{.Config.Image}}' "$APP_CONTAINER")"
  image_id="$(docker inspect --format '{{.Image}}' "$APP_CONTAINER")"
  labels="$(docker image inspect "$image_id")"
  version="$(jq -er '.[0].Config.Labels["org.opencontainers.image.version"]' <<<"$labels")"
  commit="$(jq -er '.[0].Config.Labels["org.opencontainers.image.revision"]' <<<"$labels")"
  projection="$(jq -cnS --arg version "$version" --arg image "$image" --arg imageId "$image_id" --arg gitCommit "$commit" \
    '{version:$version,image:$image,imageId:$imageId,gitCommit:$gitCommit}')"
  identity_hash="sha256:$(printf '%s' "$projection" | sha256sum | awk '{print $1}')"
  jq -cnS --arg currentVersion "$version" --arg currentImage "$image" --arg currentImageId "$image_id" \
    --arg runtimeIdentityHash "$identity_hash" --arg gitCommit "$commit" \
    '{currentVersion:$currentVersion,currentImage:$currentImage,currentImageId:$currentImageId,runtimeIdentityHash:$runtimeIdentityHash,gitCommit:$gitCommit}'
}

inspect_data() {
  local identity health app_state postgres_state redis_state migrations
  identity="$(runtime_identity)"
  health="$(curl -fsS "$BASE_URL/health")"
  app_state="$(docker inspect --format '{{.State.Status}}' "$APP_CONTAINER")"
  postgres_state="$(docker inspect --format '{{.State.Health.Status}}' "$POSTGRES_CONTAINER")"
  redis_state="$(docker inspect --format '{{.State.Health.Status}}' "$REDIS_CONTAINER")"
  migrations="$(postgres_value 'SELECT count(*) FROM schema_migrations;')"
  jq -cnS --argjson identity "$identity" --argjson health "$health" \
    --arg appState "$app_state" --arg postgresState "$postgres_state" --arg redisState "$redis_state" \
    --argjson migrations "$migrations" \
    '$identity + {health:$health,appState:$appState,postgresState:$postgresState,redisState:$redisState,migrations:$migrations}'
}

assert_expected_before() {
  local expected observed
  expected="$(jq -cS '.expectedBefore | {currentVersion,currentImage,currentImageId,runtimeIdentityHash}' "$operation_dir/task-contract.json")"
  observed="$(runtime_identity | jq -cS '{currentVersion,currentImage,currentImageId,runtimeIdentityHash}')"
  [[ "$observed" == "$expected" ]] || fail "EXPECTED_BEFORE_MISMATCH"
}

dependencies() {
  docker inspect "$POSTGRES_CONTAINER" "$REDIS_CONTAINER" | jq -cS '[.[] | {name:.Name,id:.Id,startedAt:.State.StartedAt,image:.Config.Image}]'
}

assert_dependencies_unchanged() {
  [[ -f "$operation_dir/dependencies.before.json" ]] || fail "dependency baseline is missing"
  [[ "$(dependencies)" == "$(jq -cS . "$operation_dir/dependencies.before.json")" ]] || fail "PostgreSQL or Redis container identity changed"
}

assert_rollback_source_is_current() {
  local directory="$1" source_target source_identity current_identity
  source_target="$(jq -er .targetId "$directory/legacy-request.json")"
  [[ "$source_target" == "$(jq -er .target "$directory/task-contract.json")" ]] ||
    fail "rollback source target mismatch"
  source_identity="$(jq -cS --arg target "$source_target" '.targets[$target].target | {
    currentVersion:.version,currentImage:.image,currentImageId:.imageId,runtimeIdentityHash:.runtimeIdentityHash
  }' "$RELEASES")"
  current_identity="$(jq -cS '.expectedBefore | {
    currentVersion,currentImage,currentImageId,runtimeIdentityHash
  }' "$operation_dir/task-contract.json")"
  [[ "$source_identity" == "$current_identity" ]] ||
    fail "rollback source is not the currently deployed release"
}

wait_health() {
  local code temporary
  temporary="$(mktemp "$operation_dir/.health.XXXXXX")"
  for _ in $(seq 1 120); do
    code="$(curl -sS -o "$temporary" -w '%{http_code}' "$BASE_URL/health" || true)"
    if [[ "$code" == 200 ]] && jq -e '.status == "ok" or .ok == true' "$temporary" >/dev/null 2>&1; then
      rm -f "$temporary"
      return
    fi
    sleep 1
  done
  rm -f "$temporary"
  fail "application did not become healthy"
}

public_smoke() {
  local temporary code
  temporary="$(mktemp "$operation_dir/.smoke.XXXXXX")"
  code="$(curl -sS -o "$temporary" -w '%{http_code}' "$BASE_URL/api/v1/settings/public")"
  [[ "$code" == 200 ]] || { rm -f "$temporary"; fail "public settings smoke returned HTTP $code"; }
  jq -e '.code == 0' "$temporary" >/dev/null || { rm -f "$temporary"; fail "public settings smoke failed"; }
  rm -f "$temporary"
}

write_legacy_request() {
  local catalog_policy identity now expires
  jq -e --arg target "$target" '.schemaVersion == 1 and
    (.targets[$target] | type == "object" and .status == "prepared")' "$RELEASES" >/dev/null ||
    fail "target is not present in prepared release catalog"
  catalog_policy="$(jq -cS --arg target "$target" '.targets[$target].expectedBefore | {autoApply,signatureRequired,rollbackAvailable,rollbackTargetVersion,rollbackTargetImage,rollbackSourceRecordSha256}' "$RELEASES")"
  identity="$(jq -cS '.expectedBefore | {currentVersion,currentImage,currentImageId,runtimeIdentityHash}' "$operation_dir/task-contract.json")"
  now="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  expires="$(date -u -d '+9 minutes' +%Y-%m-%dT%H:%M:%SZ)"
  jq -cnS --arg id "update_$(date +%s%3N)_$(jq -r .taskId "$operation_dir/task-contract.json")" \
    --arg idempotencyKey "$(jq -r .taskId "$operation_dir/task-contract.json")" --arg requestedAt "$now" \
    --arg expiresAt "$expires" --arg actorEmailHash "$(jq -r .actorHash "$operation_dir/task-contract.json")" \
    --arg targetId "$target" --argjson identity "$identity" --argjson policy "$catalog_policy" \
    '{schemaVersion:1,id:$id,idempotencyKey:$idempotencyKey,service:"sub2api",action:"apply",status:"queued",requestedAt:$requestedAt,expiresAt:$expiresAt,actorEmailHash:$actorEmailHash,targetId:$targetId,expectedBefore:($identity+$policy)}' \
    >"$operation_dir/legacy-request.json"
  chmod 0600 "$operation_dir/legacy-request.json"
}

delegate_update_phase() {
  local legacy_output
  [[ -f "$operation_dir/legacy-request.json" ]] || write_legacy_request
  legacy_output="$("$LEGACY_ADAPTER" "$phase" "$operation_dir/legacy-request.json" "$operation_dir")"
  jq -e --arg phase "$phase" '.ok == true and .phase == $phase' <<<"$legacy_output" >/dev/null || fail "legacy adapter contract failed"
  result "$(jq -r .detail <<<"$legacy_output")"
}

case "$action:$phase" in
  inspect:inspect)
    result "Sub2API 运行身份检查完成" "$(inspect_data)"
    ;;
  check:discover)
    release_file="$(mktemp "$operation_dir/.release.XXXXXX")"
    curl -fsS -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28' \
      https://api.github.com/repos/Wei-Shaw/sub2api/releases/latest -o "$release_file"
    latest="$(jq -er '.tag_name | select(test("^v[0-9]+\\.[0-9]+\\.[0-9]+([+-][0-9A-Za-z.-]+)?$"))' "$release_file")"
    published="$(jq -er '.published_at' "$release_file")"
    current="$(runtime_identity | jq -r .currentVersion)"
    prepared=false
    jq -e --arg target "$latest" '.targets[$target] | type == "object" and .status == "prepared"' "$RELEASES" >/dev/null 2>&1 && prepared=true
    rm -f "$release_file"
    result "Sub2API 最新发布检查完成" "$(jq -cn --arg currentVersion "$current" --arg latestTag "$latest" \
      --arg publishedAt "$published" --argjson prepared "$prepared" \
      '{currentVersion:$currentVersion,latestTag:$latestTag,publishedAt:$publishedAt,prepared:$prepared,updateAvailable:($latestTag != ("v"+$currentVersion))}')"
    ;;
  backup:preflight)
    assert_expected_before
    result "备份前运行身份未漂移"
    ;;
  backup:backup)
    "$BACKUP_POSTGRES" >/dev/null
    "$BACKUP_REDIS" >/dev/null
    "$BACKUP_VOLUMES" >/dev/null
    result "PostgreSQL、Redis 与卷分类备份完成"
    ;;
  backup:verify)
    assert_expected_before
    result "备份任务退出状态与业务身份检查通过"
    ;;
  restart:preflight)
    assert_expected_before
    [[ -f "$CONTROLLED_COMPOSE" && ! -L "$CONTROLLED_COMPOSE" ]] || fail "controlled Compose is missing or unsafe"
    [[ -f "$RUNTIME_COMPOSE" && ! -L "$RUNTIME_COMPOSE" ]] || fail "runtime Compose is missing or unsafe"
    cmp -s "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" || fail "controlled and runtime Compose files differ"
    dependencies >"$operation_dir/dependencies.before.json"
    result "运行身份、Compose 与依赖容器基线已记录"
    ;;
  restart:restart)
    docker compose --env-file "$ENV_FILE" -f "$RUNTIME_COMPOSE" config --quiet
    docker compose --env-file "$ENV_FILE" -f "$RUNTIME_COMPOSE" up -d --no-deps --force-recreate sub2api
    assert_dependencies_unchanged
    result "仅重建 Sub2API 应用容器"
    ;;
  restart:health)
    wait_health
    assert_dependencies_unchanged
    result "Sub2API 健康检查通过"
    ;;
  restart:smoke)
    public_smoke
    assert_dependencies_unchanged
    result "Sub2API 公共只读 smoke 通过"
    ;;
  update:preflight)
    write_legacy_request
    delegate_update_phase
    ;;
  update:backup|update:migration|update:apply|update:health|update:smoke|update:identity|update:rollback)
    delegate_update_phase
    ;;
  rollback:preflight)
    [[ -n "$source_dir" && -d "$source_dir" && ! -L "$source_dir" ]] || fail "rollback source is unsafe"
    [[ -f "$source_dir/task-contract.json" && ! -L "$source_dir/task-contract.json" ]] || fail "rollback source contract is missing"
    [[ -f "$source_dir/legacy-request.json" ]] || fail "rollback source request is missing"
    [[ "$(jq -r .service "$source_dir/task-contract.json")" == sub2api ]] || fail "rollback source service mismatch"
    [[ "$(jq -r .action "$source_dir/task-contract.json")" == update ]] || fail "rollback source action mismatch"
    assert_expected_before
    assert_rollback_source_is_current "$source_dir"
    dependencies >"$operation_dir/dependencies.before.json"
    chmod 0600 "$operation_dir/dependencies.before.json"
    result "受控更新来源、当前发布与依赖身份已核验"
    ;;
  rollback:apply)
    legacy_output="$(SUB2API_UPDATE_CONTROL_DEPENDENCY_BASELINE="$operation_dir/dependencies.before.json" \
      "$LEGACY_ADAPTER" rollback "$source_dir/legacy-request.json" "$source_dir")"
    jq -e '.ok == true and .phase == "rollback"' <<<"$legacy_output" >/dev/null || fail "legacy rollback contract failed"
    result "$(jq -r .detail <<<"$legacy_output")"
    ;;
  rollback:health)
    wait_health
    assert_dependencies_unchanged
    result "回滚后健康检查通过"
    ;;
  rollback:smoke)
    public_smoke
    assert_dependencies_unchanged
    result "回滚后公共只读 smoke 通过"
    ;;
  rollback:identity)
    expected="$(jq -cS '.expectedBefore | {currentVersion,currentImage,currentImageId,runtimeIdentityHash}' "$source_dir/task-contract.json")"
    observed="$(runtime_identity | jq -cS '{currentVersion,currentImage,currentImageId,runtimeIdentityHash}')"
    [[ "$observed" == "$expected" ]] || fail "rollback identity mismatch"
    assert_dependencies_unchanged
    result "回滚后的版本、镜像与运行身份一致"
    ;;
  restore-drill:*)
    fail "Sub2API restore drill is not enabled"
    ;;
  *)
    fail "unsupported action phase"
    ;;
esac
