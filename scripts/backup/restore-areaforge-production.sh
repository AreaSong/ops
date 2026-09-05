#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

action="${1:-}"
phase="${2:-}"
operation_dir="${3:-}"
target="${4:-}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BACKUP_ROOT="${BACKUP_ROOT:-/var/backups/ops}"
CONTRACT_VALIDATOR="${RESTORE_CONTRACT_VALIDATOR:-$SCRIPT_DIR/restore_contract.py}"
MANIFEST_TOOL="${BACKUP_MANIFEST_TOOL:-$SCRIPT_DIR/backup_manifest.py}"
ENV_FILE="${AREAFORGE_RESTORE_ENV_FILE:-/opt/areaforge/.env.production}"
RUNTIME_COMPOSE="${AREAFORGE_RESTORE_RUNTIME_COMPOSE:-/opt/areaforge/docker-compose.prod.yml}"
CONTROLLED_COMPOSE="${AREAFORGE_RESTORE_CONTROLLED_COMPOSE:-/opt/ops/services/areaforge/compose.yml}"
POSTGRES_CONTAINER="${AREAFORGE_RESTORE_POSTGRES_CONTAINER:-areaforge-postgres}"
APP_CONTAINER="${AREAFORGE_RESTORE_APP_CONTAINER:-areaforge-web}"
APP_SERVICE="${AREAFORGE_RESTORE_APP_SERVICE:-web}"
POSTGRES_SERVICE="${AREAFORGE_RESTORE_POSTGRES_SERVICE:-postgres}"
HEALTH_URL="${AREAFORGE_RESTORE_HEALTH_URL:-http://127.0.0.1:3020/api/health}"
ENV_SWITCHER="${RESTORE_ENV_SWITCHER:-$SCRIPT_DIR/restore_env.py}"
WORK_ROOT="${AREAFORGE_RESTORE_WORK_ROOT:-/var/lib/areasong-ops/production-restore}"

fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
result() {
  local summary="$1" data="${2:-}" changed="${3:-false}"
  [[ -n "$data" ]] || data='{}'
  jq -cn --arg action "$action" --arg phase "$phase" --arg summary "$summary" \
    --argjson data "$data" --argjson changed "$changed" \
    '{schemaVersion:2,action:$action,phase:$phase,ok:true,summary:$summary,
      data:($data + {productionChanged:$changed})}'
}

[[ "$action" == restore ]] || fail "unsupported action"
[[ "$phase" =~ ^(preflight|quiesce|restore|verify|resume)$ ]] || fail "unsupported restore phase"
[[ -d "$operation_dir" && ! -L "$operation_dir" ]] || fail "operation directory is unsafe"
[[ "$target" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$ ]] || fail "restore target is invalid"
[[ "$(id -u)" == 0 ]] || fail "production restore must run as root"

for command_name in awk chmod chown cmp cp curl date docker find grep gzip install jq mktemp mv python3 rm seq sha256sum sleep stat touch; do
  command -v "$command_name" >/dev/null || fail "missing command: $command_name"
done
for path in "$CONTRACT_VALIDATOR" "$MANIFEST_TOOL" "$ENV_SWITCHER" "$ENV_FILE" "$RUNTIME_COMPOSE" "$CONTROLLED_COMPOSE"; do
  [[ -f "$path" && ! -L "$path" ]] || fail "required path is missing or unsafe: $path"
done

state_file="$operation_dir/areaforge-production-state.json"
verified_file="$operation_dir/restore-verified"
resumed_file="$operation_dir/restore-resumed"
work_dir=""

cleanup() {
  if [[ -n "$work_dir" && -d "$work_dir" ]]; then
    rm -rf -- "$work_dir"
  fi
}
trap cleanup EXIT

validate_contract() {
  local output
  output="$("$CONTRACT_VALIDATOR" --contract "$operation_dir/recovery-point.json" \
    --service areaforge --target "$target" --backup-root "$BACKUP_ROOT" \
    --required-role postgres-areaforge \
    --required-role volume-areaforge-uploads \
    --required-role volume-areaforge-ops-state)" || fail "恢复合同校验失败"
  CONTRACT_JSON="$output"
}

require_state() {
  [[ -f "$state_file" && ! -L "$state_file" ]] || fail "restore state is missing"
  [[ "$(stat -c %a "$state_file" 2>/dev/null || stat -f %Lp "$state_file")" == 600 ]] || fail "restore state mode is unsafe"
}

artifact_path() {
  local role="$1"
  jq -er --arg role "$role" '.artifacts[$role].path' <<<"$CONTRACT_JSON"
}

container_env() {
  local container="$1" key="$2"
  docker inspect "$container" | jq -er --arg prefix "$key=" \
    '.[0].Config.Env[] | select(startswith($prefix)) | .[($prefix|length):]'
}

compose() {
  docker compose --env-file "$ENV_FILE" -f "$RUNTIME_COMPOSE" "$@"
}

safe_name() {
  [[ "$1" =~ ^[A-Za-z0-9_.-]{1,63}$ ]] || fail "generated resource name is invalid"
}

extract_tar() {
  local archive="$1" destination="$2"
  install -d -m 0700 "$destination"
  python3 "$MANIFEST_TOOL" extract-tar --archive "$archive" --destination "$destination" \
    --max-members 200000 --max-bytes $((50 * 1024 * 1024 * 1024)) >/dev/null
}

copy_tree_into_volume() {
  local source="$1" volume="$2" mountpoint docker_root
  docker volume inspect "$volume" >/dev/null || fail "staging volume is unavailable"
  mountpoint="$(docker volume inspect -f '{{.Mountpoint}}' "$volume")"
  docker_root="$(docker info --format '{{.DockerRootDir}}')"
  [[ "$docker_root" == /* && -d "$docker_root" && ! -L "$docker_root" ]] || fail "Docker root directory is unsafe"
  [[ "$mountpoint" == "$docker_root"/volumes/*/_data && -d "$mountpoint" && ! -L "$mountpoint" ]] ||
    fail "staging volume mountpoint is unsafe"
  cp -a "$source"/. "$mountpoint"/
}

wait_postgres() {
  local container="$1" user="$2" database="$3"
  for _ in $(seq 1 120); do
    if docker logs "$container" 2>&1 | grep -Fq 'PostgreSQL init process complete; ready for start up.' \
      && docker exec "$container" psql -v ON_ERROR_STOP=1 -U "$user" -d "$database" -Atc 'select 1' >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done
  docker logs "$container" >"$operation_dir/staging-postgres.log" 2>&1 || true
  chmod 0600 "$operation_dir/staging-postgres.log"
  fail "staging PostgreSQL did not become ready"
}

create_staging_postgres() {
  local artifact="$1" volume="$2" image="$3" user="$4" database="$5" password="$6"
  local env_file container
  safe_name "$volume"
  if docker volume inspect "$volume" >/dev/null 2>&1; then
    fail "staging PostgreSQL volume already exists"
  fi
  container="areasong-restore-pg-${target//-/}"
  safe_name "$container"
  if docker inspect "$container" >/dev/null 2>&1; then
    fail "staging PostgreSQL container already exists; refusing to remove existing recovery evidence"
  fi
  docker volume create "$volume" >/dev/null
  env_file="$work_dir/postgres.env"
  printf 'POSTGRES_USER=postgres\nPOSTGRES_PASSWORD=%s\nPOSTGRES_DB=postgres\n' "$password" >"$env_file"
  chmod 0600 "$env_file"
  docker run --detach --name "$container" --network none --env-file "$env_file" \
    --volume "$volume:/var/lib/postgresql/data" "$image" >/dev/null
  wait_postgres "$container" postgres postgres
  gzip -dc "$artifact" | docker exec -i "$container" psql -v ON_ERROR_STOP=1 -U postgres -d postgres >/dev/null
  docker exec "$container" psql -v ON_ERROR_STOP=1 -U "$user" -d "$database" -Atc 'select 1' >/dev/null
  docker stop "$container" >/dev/null
  docker rm "$container" >/dev/null
}

write_state() {
  local postgres_volume="$1" uploads_volume="$2" ops_state_dir="$3" old_postgres="$4" old_uploads="$5" old_state="$6"
  local pg_image="$7" database="$8" user="$9" app_uid="${10}" app_gid="${11}"
  jq -nS --arg recoveryPointId "$target" --arg postgresVolume "$postgres_volume" --arg uploadsVolume "$uploads_volume" \
    --arg opsStateDir "$ops_state_dir" --arg oldPostgres "$old_postgres" --arg oldUploads "$old_uploads" \
    --arg oldState "$old_state" --arg postgresImage "$pg_image" --arg database "$database" --arg user "$user" \
    --argjson appUid "$app_uid" --argjson appGid "$app_gid" \
    '{schemaVersion:1,service:"areaforge",recoveryPointId:$recoveryPointId,status:"prepared",quiesced:false,
      verified:false,resumed:false,staging:{postgresVolume:$postgresVolume,uploadsVolume:$uploadsVolume,opsStateDir:$opsStateDir},
      previous:{postgresVolume:$oldPostgres,uploadsVolume:$oldUploads,opsStateDir:$oldState},
      postgres:{image:$postgresImage,database:$database,user:$user},app:{uid:$appUid,gid:$appGid}}' >"$state_file"
  chmod 0600 "$state_file"
}

set_state_flag() {
  local key="$1" value="$2" temporary
  temporary="$(mktemp "$operation_dir/.restore-state.XXXXXX")"
  jq --arg key "$key" --argjson value "$value" '.[$key] = $value' "$state_file" >"$temporary"
  chmod 0600 "$temporary"
  mv -f "$temporary" "$state_file"
}

case "$phase" in
  preflight)
    validate_contract
    [[ -f "$operation_dir/task-contract.json" && ! -L "$operation_dir/task-contract.json" ]] || fail "task contract is missing"
    cmp -s "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" || fail "controlled and runtime Compose files differ"
    compose config --quiet >/dev/null
    [[ "$(docker inspect --format '{{.State.Status}}' "$POSTGRES_CONTAINER")" == running ]] || fail "AreaForge PostgreSQL is not running"
    [[ "$(docker inspect --format '{{.State.Status}}' "$APP_CONTAINER")" == running ]] || fail "AreaForge Web is not running"
    old_postgres="$(python3 "$ENV_SWITCHER" --file "$ENV_FILE" --get AREAFORGE_POSTGRES_VOLUME --default areaforge_areaforge-postgres-data)"
    old_uploads="$(python3 "$ENV_SWITCHER" --file "$ENV_FILE" --get AREAFORGE_UPLOADS_VOLUME --default areaforge_areaforge-uploads)"
    old_state="$(python3 "$ENV_SWITCHER" --file "$ENV_FILE" --get AREAFORGE_OPS_STATE_HOST_DIR --default /opt/areaforge/ops-state)"
    [[ "$old_postgres" =~ ^[A-Za-z0-9_.-]+$ && "$old_uploads" =~ ^[A-Za-z0-9_.-]+$ && "$old_state" == /opt/areaforge/* ]] || fail "current AreaForge data binding is unsafe"
    pg_image="$(docker inspect --format '{{.Config.Image}}' "$POSTGRES_CONTAINER")"
    database="$(container_env "$POSTGRES_CONTAINER" POSTGRES_DB)"
    user="$(container_env "$POSTGRES_CONTAINER" POSTGRES_USER)"
    app_uid="$(docker exec "$APP_CONTAINER" id -u)"
    app_gid="$(docker exec "$APP_CONTAINER" id -g)"
    [[ "$database" =~ ^[A-Za-z_][A-Za-z0-9_]*$ && "$user" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || fail "database identity is invalid"
    [[ "$app_uid" =~ ^[0-9]+$ && "$app_gid" =~ ^[0-9]+$ ]] || fail "AreaForge application identity is invalid"
    write_state "" "" "" "$old_postgres" "$old_uploads" "$old_state" "$pg_image" "$database" "$user" "$app_uid" "$app_gid"
    result "AreaForge 生产恢复合同、运行身份与数据绑定已核验" '{}' false
    ;;
  quiesce)
    validate_contract
    require_state
    compose stop "$APP_SERVICE" "$POSTGRES_SERVICE" >/dev/null
    [[ "$(docker inspect --format '{{.State.Status}}' "$APP_CONTAINER")" != running ]] || fail "AreaForge Web 未停止"
    [[ "$(docker inspect --format '{{.State.Status}}' "$POSTGRES_CONTAINER")" != running ]] || fail "AreaForge PostgreSQL 未停止"
    set_state_flag quiesced true
    result "AreaForge Web 与 PostgreSQL 已排空并停止" '{}' true
    ;;
  restore)
    validate_contract
    require_state
    [[ "$(jq -r .quiesced "$state_file")" == true ]] || fail "restore requires quiesce"
    install -d -m 0700 "$WORK_ROOT"
    [[ -d "$WORK_ROOT" && ! -L "$WORK_ROOT" ]] || fail "restore work root is unsafe"
    work_dir="$(mktemp -d "$WORK_ROOT/areaforge-${target//-/}-XXXXXX")"
    chmod 0700 "$work_dir"
    postgres_artifact="$(artifact_path postgres-areaforge)"
    uploads_artifact="$(artifact_path volume-areaforge-uploads)"
    ops_artifact="$(artifact_path volume-areaforge-ops-state)"
    postgres_volume="areasong-restore-areaforge-${target//-/}"
    uploads_volume="areasong-restore-uploads-${target//-/}"
    ops_state_dir="$WORK_ROOT/ops-state-${target//-/}"
    safe_name "$postgres_volume"; safe_name "$uploads_volume"
    pg_image="$(jq -r .postgres.image "$state_file")"
    database="$(jq -r .postgres.database "$state_file")"
    user="$(jq -r .postgres.user "$state_file")"
    password="$(container_env "$POSTGRES_CONTAINER" POSTGRES_PASSWORD 2>/dev/null || true)"
    [[ -n "$password" && "$password" != *$'\n'* && "$password" != *$'\r'* ]] ||
      fail "PostgreSQL password is unavailable or unsafe without exposing it in arguments"
    extract_tar "$uploads_artifact" "$work_dir/uploads"
    extract_tar "$ops_artifact" "$work_dir/ops"
    [[ ! -e "$ops_state_dir" && ! -L "$ops_state_dir" ]] || fail "staging ops-state directory already exists"
    if docker volume inspect "$uploads_volume" >/dev/null 2>&1; then
      fail "staging uploads volume already exists"
    fi
    install -d -m 0700 "$ops_state_dir"
    ops_source="$work_dir/ops"
    if [[ -d "$ops_source/areaforge-ops-state" && ! -L "$ops_source/areaforge-ops-state" ]]; then
      ops_source="$ops_source/areaforge-ops-state"
    fi
    cp -a "$ops_source"/. "$ops_state_dir"/
    app_uid="$(jq -er .app.uid "$state_file")"
    app_gid="$(jq -er .app.gid "$state_file")"
    [[ "$app_uid" =~ ^[0-9]+$ && "$app_gid" =~ ^[0-9]+$ ]] || fail "stored AreaForge application identity is invalid"
    chown -R "$app_uid:$app_gid" "$ops_state_dir"
    docker volume create "$uploads_volume" >/dev/null
    copy_tree_into_volume "$work_dir/uploads" "$uploads_volume"
    uploads_mountpoint="$(docker volume inspect -f '{{.Mountpoint}}' "$uploads_volume")"
    chown -R "$app_uid:$app_gid" "$uploads_mountpoint"
    create_staging_postgres "$postgres_artifact" "$postgres_volume" "$pg_image" "$user" "$database" "$password"
    temporary_env_backup="$operation_dir/areaforge.env.before"
    python3 "$ENV_SWITCHER" --file "$ENV_FILE" --backup "$temporary_env_backup" \
      --set "AREAFORGE_POSTGRES_VOLUME=$postgres_volume" \
      --set "AREAFORGE_UPLOADS_VOLUME=$uploads_volume" \
      --set "AREAFORGE_OPS_STATE_HOST_DIR=$ops_state_dir"
    jq --arg postgresVolume "$postgres_volume" --arg uploadsVolume "$uploads_volume" --arg opsStateDir "$ops_state_dir" \
      '.staging={postgresVolume:$postgresVolume,uploadsVolume:$uploadsVolume,opsStateDir:$opsStateDir}' \
      "$state_file" >"$operation_dir/.state.new"
    chmod 0600 "$operation_dir/.state.new"; mv -f "$operation_dir/.state.new" "$state_file"
    compose config --quiet >/dev/null
    compose up -d "$POSTGRES_SERVICE" "$APP_SERVICE" >/dev/null
    set_state_flag restored true
    result "AreaForge 恢复数据已写入新卷并切换 Compose 数据绑定" \
      "$(jq -c '{staging,previous}' "$state_file")" true
    ;;
  verify)
    validate_contract
    require_state
    [[ "$(jq -r .restored "$state_file")" == true ]] || fail "restore has not completed"
    for _ in $(seq 1 120); do
      if curl -fsS "$HEALTH_URL" >/dev/null 2>&1; then break; fi
      sleep 1
    done
    curl -fsS "$HEALTH_URL" >/dev/null || fail "AreaForge health check failed after restore"
    [[ "$(docker inspect --format '{{.State.Health.Status}}' "$POSTGRES_CONTAINER")" == healthy ]] || fail "AreaForge PostgreSQL is not healthy"
    database="$(jq -r .postgres.database "$state_file")"; user="$(jq -r .postgres.user "$state_file")"
    docker exec "$POSTGRES_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$user" -d "$database" -Atc 'select 1' >/dev/null
    [[ -d "$(jq -r .staging.opsStateDir "$state_file")" ]] || fail "restored ops-state directory is missing"
    touch "$verified_file"; chmod 0600 "$verified_file"
    set_state_flag verified true
    result "AreaForge 恢复后数据库、应用健康与 ops-state 校验通过" '{}' true
    ;;
  resume)
    validate_contract
    require_state
    [[ -f "$verified_file" ]] || fail "restore verification evidence is missing"
    compose up -d "$POSTGRES_SERVICE" "$APP_SERVICE" >/dev/null
    curl -fsS "$HEALTH_URL" >/dev/null || fail "AreaForge resume health check failed"
    touch "$resumed_file"; chmod 0600 "$resumed_file"
    set_state_flag resumed true
    result "AreaForge 生产服务已恢复流量并完成最终健康检查" '{}' true
    ;;
esac
