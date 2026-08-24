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
ENV_SWITCHER="${RESTORE_ENV_SWITCHER:-$SCRIPT_DIR/restore_env.py}"
ENV_FILE="${SUB2API_RESTORE_ENV_FILE:-/opt/services/sub2api/.env}"
RUNTIME_COMPOSE="${SUB2API_RESTORE_RUNTIME_COMPOSE:-/opt/services/sub2api/compose.yml}"
CONTROLLED_COMPOSE="${SUB2API_RESTORE_CONTROLLED_COMPOSE:-/opt/ops/services/sub2api/compose.yml}"
DATA_ROOT="${SUB2API_RESTORE_DATA_ROOT:-/var/lib/sub2api}"
WORK_ROOT="${SUB2API_RESTORE_WORK_ROOT:-/var/lib/areasong-ops/production-restore}"
APP_CONTAINER="${SUB2API_RESTORE_APP_CONTAINER:-sub2api}"
POSTGRES_CONTAINER="${SUB2API_RESTORE_POSTGRES_CONTAINER:-sub2api-postgres}"
REDIS_CONTAINER="${SUB2API_RESTORE_REDIS_CONTAINER:-sub2api-redis}"
APP_SERVICE="${SUB2API_RESTORE_APP_SERVICE:-sub2api}"
POSTGRES_SERVICE="${SUB2API_RESTORE_POSTGRES_SERVICE:-postgres}"
REDIS_SERVICE="${SUB2API_RESTORE_REDIS_SERVICE:-redis}"
HEALTH_URL="${SUB2API_RESTORE_HEALTH_URL:-http://127.0.0.1:8080/health}"

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
[[ "$DATA_ROOT" == /* && "$DATA_ROOT" != / && "$WORK_ROOT" == /* && "$WORK_ROOT" != / ]] || fail "restore roots are unsafe"

for command_name in chmod chown cmp cp curl docker find grep gzip install jq mkdir mktemp mv python3 rm seq sleep stat touch; do
  command -v "$command_name" >/dev/null || fail "missing command: $command_name"
done
for path in "$CONTRACT_VALIDATOR" "$MANIFEST_TOOL" "$ENV_SWITCHER" "$ENV_FILE" "$RUNTIME_COMPOSE" "$CONTROLLED_COMPOSE"; do
  [[ -f "$path" && ! -L "$path" ]] || fail "required path is missing or unsafe: $path"
done

state_file="$operation_dir/sub2api-production-state.json"
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
  CONTRACT_JSON="$("$CONTRACT_VALIDATOR" --contract "$operation_dir/recovery-point.json" \
    --service sub2api --target "$target" --backup-root "$BACKUP_ROOT" \
    --required-role postgres-sub2api --required-role redis \
    --required-role volume-sub2api-data)" || fail "恢复合同校验失败"
}

require_state() {
  [[ -f "$state_file" && ! -L "$state_file" ]] || fail "restore state is missing"
  [[ "$(stat -c %a "$state_file" 2>/dev/null || stat -f %Lp "$state_file")" == 600 ]] || fail "restore state mode is unsafe"
  jq -e --arg target "$target" \
    '.schemaVersion == 1 and .service == "sub2api" and .recoveryPointId == $target' "$state_file" >/dev/null ||
    fail "restore state identity is invalid"
}

artifact_path() {
  jq -er --arg role "$1" '.artifacts[$role].path' <<<"$CONTRACT_JSON"
}

container_env() {
  local container="$1" key="$2"
  docker inspect "$container" | jq -er --arg prefix "$key=" \
    '.[0].Config.Env[] | select(startswith($prefix)) | .[($prefix|length):]'
}

container_mount_source() {
  local container="$1" destination="$2"
  docker inspect "$container" | jq -er --arg destination "$destination" \
    '.[0].Mounts[] | select(.Type == "bind" and .Destination == $destination) | .Source'
}

compose() {
  docker compose --env-file "$ENV_FILE" -f "$RUNTIME_COMPOSE" "$@"
}

safe_data_path() {
  local path="$1"
  [[ "$path" == "$DATA_ROOT"/* && "$path" != "$DATA_ROOT" && ! -L "$path" ]] || fail "Sub2API data path is unsafe: $path"
}

extract_tar() {
  local archive="$1" destination="$2"
  install -d -m 0700 "$destination"
  python3 "$MANIFEST_TOOL" extract-tar --archive "$archive" --destination "$destination" \
    --max-members 200000 --max-bytes $((50 * 1024 * 1024 * 1024)) >/dev/null
}

wait_postgres() {
  local container="$1"
  for _ in $(seq 1 120); do
    if docker logs "$container" 2>&1 | grep -Fq 'PostgreSQL init process complete; ready for start up.' \
      && docker exec "$container" psql -v ON_ERROR_STOP=1 -U postgres -d postgres -Atc 'select 1' >/dev/null 2>&1; then
      return
    fi
    sleep 1
  done
  docker logs "$container" >"$operation_dir/staging-postgres.log" 2>&1 || true
  chmod 0600 "$operation_dir/staging-postgres.log"
  fail "staging PostgreSQL did not reach final ready state"
}

create_staging_postgres() {
  local artifact="$1" directory="$2" image="$3" database="$4" user="$5" password="$6"
  local container env_file
  container="areasong-restore-sub2api-pg-${target//-/}"
  if docker inspect "$container" >/dev/null 2>&1; then
    fail "staging PostgreSQL container already exists; refusing to remove recovery evidence"
  fi
  env_file="$work_dir/postgres.env"
  printf 'POSTGRES_USER=postgres\nPOSTGRES_PASSWORD=%s\nPOSTGRES_DB=postgres\nPGDATA=/var/lib/postgresql/data\n' \
    "$password" >"$env_file"
  chmod 0600 "$env_file"
  docker run --detach --name "$container" --network none --env-file "$env_file" \
    --volume "$directory:/var/lib/postgresql/data" "$image" >/dev/null
  wait_postgres "$container"
  gzip -dc "$artifact" | docker exec -i "$container" \
    psql -v ON_ERROR_STOP=1 -U postgres -d postgres >/dev/null
  docker exec "$container" psql -v ON_ERROR_STOP=1 -U "$user" -d "$database" -Atc \
    'select count(*) from schema_migrations' >/dev/null
  docker stop "$container" >/dev/null
  docker rm "$container" >/dev/null
}

validate_staging_redis() {
  local directory="$1" image="$2" password="$3" container env_file
  container="areasong-restore-sub2api-redis-${target//-/}"
  if docker inspect "$container" >/dev/null 2>&1; then
    fail "staging Redis container already exists; refusing to remove recovery evidence"
  fi
  env_file="$work_dir/redis.env"
  printf 'REDISCLI_AUTH=%s\n' "$password" >"$env_file"
  chmod 0600 "$env_file"
  docker run --detach --name "$container" --network none --env-file "$env_file" \
    --volume "$directory:/data" "$image" sh -c \
    'exec redis-server --appendonly no --aclfile /data/users.acl --requirepass "$REDISCLI_AUTH"' >/dev/null
  for _ in $(seq 1 60); do
    if docker exec "$container" redis-cli ping 2>/dev/null | grep -Fxq PONG; then
      docker stop "$container" >/dev/null
      docker rm "$container" >/dev/null
      return
    fi
    sleep 1
  done
  docker logs "$container" >"$operation_dir/staging-redis.log" 2>&1 || true
  chmod 0600 "$operation_dir/staging-redis.log"
  fail "staging Redis did not become ready"
}

write_state() {
  local old_data="$1" old_postgres="$2" old_redis="$3" pg_image="$4" redis_image="$5"
  local database="$6" user="$7" app_uid="$8" app_gid="$9" pg_uid="${10}" pg_gid="${11}" redis_uid="${12}" redis_gid="${13}"
  jq -nS --arg recoveryPointId "$target" --arg oldData "$old_data" --arg oldPostgres "$old_postgres" \
    --arg oldRedis "$old_redis" --arg postgresImage "$pg_image" --arg redisImage "$redis_image" \
    --arg database "$database" --arg user "$user" --argjson appUid "$app_uid" --argjson appGid "$app_gid" \
    --argjson pgUid "$pg_uid" --argjson pgGid "$pg_gid" --argjson redisUid "$redis_uid" --argjson redisGid "$redis_gid" \
    '{schemaVersion:1,service:"sub2api",recoveryPointId:$recoveryPointId,status:"prepared",
      quiesced:false,restored:false,verified:false,resumed:false,
      previous:{dataDir:$oldData,postgresDir:$oldPostgres,redisDir:$oldRedis},staging:{},
      postgres:{image:$postgresImage,database:$database,user:$user,uid:$pgUid,gid:$pgGid},
      redis:{image:$redisImage,uid:$redisUid,gid:$redisGid},app:{uid:$appUid,gid:$appGid}}' >"$state_file"
  chmod 0600 "$state_file"
}

update_state() {
  local expression="$1" temporary
  shift
  temporary="$(mktemp "$operation_dir/.sub2api-state.XXXXXX")"
  jq "$@" "$expression" "$state_file" >"$temporary"
  chmod 0600 "$temporary"
  mv -f "$temporary" "$state_file"
}

case "$phase" in
  preflight)
    validate_contract
    [[ -f "$operation_dir/task-contract.json" && ! -L "$operation_dir/task-contract.json" ]] || fail "task contract is missing"
    cmp -s "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" || fail "controlled and runtime Compose files differ"
    compose config --quiet >/dev/null
    for container in "$APP_CONTAINER" "$POSTGRES_CONTAINER" "$REDIS_CONTAINER"; do
      [[ "$(docker inspect --format '{{.State.Status}}' "$container")" == running ]] || fail "Sub2API production container is not running: $container"
    done
    old_data="$(python3 "$ENV_SWITCHER" --file "$ENV_FILE" --get SUB2API_DATA_DIR --default "$DATA_ROOT/data")"
    old_postgres="$(python3 "$ENV_SWITCHER" --file "$ENV_FILE" --get SUB2API_POSTGRES_DATA_DIR --default "$DATA_ROOT/postgres_data")"
    old_redis="$(python3 "$ENV_SWITCHER" --file "$ENV_FILE" --get SUB2API_REDIS_DATA_DIR --default "$DATA_ROOT/redis_data")"
    for directory in "$old_data" "$old_postgres" "$old_redis"; do
      safe_data_path "$directory"
      [[ -d "$directory" && ! -L "$directory" ]] || fail "current Sub2API data directory is unavailable: $directory"
    done
    [[ "$(container_mount_source "$APP_CONTAINER" /app/data)" == "$old_data" ]] || fail "Sub2API application mount differs from its env binding"
    [[ "$(container_mount_source "$POSTGRES_CONTAINER" /var/lib/postgresql/data)" == "$old_postgres" ]] || fail "PostgreSQL mount differs from its env binding"
    [[ "$(container_mount_source "$REDIS_CONTAINER" /data)" == "$old_redis" ]] || fail "Redis mount differs from its env binding"
    pg_image="$(docker inspect --format '{{.Config.Image}}' "$POSTGRES_CONTAINER")"
    redis_image="$(docker inspect --format '{{.Config.Image}}' "$REDIS_CONTAINER")"
    database="$(container_env "$POSTGRES_CONTAINER" POSTGRES_DB)"
    user="$(container_env "$POSTGRES_CONTAINER" POSTGRES_USER)"
    app_uid="$(docker exec "$APP_CONTAINER" id -u)"; app_gid="$(docker exec "$APP_CONTAINER" id -g)"
    pg_uid="$(docker exec "$POSTGRES_CONTAINER" id -u postgres)"; pg_gid="$(docker exec "$POSTGRES_CONTAINER" id -g postgres)"
    redis_uid="$(docker exec "$REDIS_CONTAINER" id -u)"; redis_gid="$(docker exec "$REDIS_CONTAINER" id -g)"
    [[ "$database" =~ ^[A-Za-z_][A-Za-z0-9_]*$ && "$user" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || fail "database identity is invalid"
    for identity in "$app_uid" "$app_gid" "$pg_uid" "$pg_gid" "$redis_uid" "$redis_gid"; do
      [[ "$identity" =~ ^[0-9]+$ ]] || fail "container filesystem identity is invalid"
    done
    write_state "$old_data" "$old_postgres" "$old_redis" "$pg_image" "$redis_image" \
      "$database" "$user" "$app_uid" "$app_gid" "$pg_uid" "$pg_gid" "$redis_uid" "$redis_gid"
    result "Sub2API 生产恢复合同、运行身份与三个数据源绑定已核验" '{}' false
    ;;
  quiesce)
    validate_contract
    require_state
    compose stop "$APP_SERVICE" "$REDIS_SERVICE" "$POSTGRES_SERVICE" >/dev/null
    for container in "$APP_CONTAINER" "$POSTGRES_CONTAINER" "$REDIS_CONTAINER"; do
      [[ "$(docker inspect --format '{{.State.Status}}' "$container")" != running ]] || fail "Sub2API container did not stop: $container"
    done
    update_state '.quiesced = true'
    result "Sub2API、Redis 与 PostgreSQL 已停止并保留旧数据源" '{}' true
    ;;
  restore)
    validate_contract
    require_state
    [[ "$(jq -r .quiesced "$state_file")" == true ]] || fail "restore requires quiesce"
    install -d -m 0700 "$DATA_ROOT" "$WORK_ROOT"
    [[ -d "$DATA_ROOT" && ! -L "$DATA_ROOT" && -d "$WORK_ROOT" && ! -L "$WORK_ROOT" ]] || fail "restore root is unsafe"
    work_dir="$(mktemp -d "$WORK_ROOT/sub2api-${target//-/}-XXXXXX")"
    chmod 0700 "$work_dir"
    restore_parent="$DATA_ROOT/restores"
    install -d -m 0700 "$restore_parent"
    [[ -d "$restore_parent" && ! -L "$restore_parent" ]] || fail "restore parent directory is unsafe"
    restore_root="$restore_parent/${target//-/}"
    data_dir="$restore_root/data"
    postgres_dir="$restore_root/postgres_data"
    redis_dir="$restore_root/redis_data"
    [[ ! -e "$restore_root" && ! -L "$restore_root" ]] || fail "staging Sub2API restore directory already exists"
    mkdir -m 0700 -- "$restore_root"
    mkdir -m 0700 -- "$data_dir" "$postgres_dir" "$redis_dir"
    postgres_artifact="$(artifact_path postgres-sub2api)"
    redis_artifact="$(artifact_path redis)"
    data_artifact="$(artifact_path volume-sub2api-data)"
    extract_tar "$redis_artifact" "$work_dir/redis"
    extract_tar "$data_artifact" "$work_dir/app"
    [[ -d "$work_dir/redis/redis_data" && ! -L "$work_dir/redis/redis_data" ]] ||
      fail "Redis recovery archive layout is invalid"
    [[ -f "$work_dir/redis/metadata.txt" && ! -L "$work_dir/redis/metadata.txt" ]] ||
      fail "Redis recovery artifact metadata is missing or unsafe"
    grep -Fxq 'aclfile_included=yes' "$work_dir/redis/metadata.txt" ||
      fail "Redis recovery artifact does not declare its ACL file"
    [[ -f "$work_dir/redis/redis_data/dump.rdb" && ! -L "$work_dir/redis/redis_data/dump.rdb" &&
       -s "$work_dir/redis/redis_data/dump.rdb" &&
       -f "$work_dir/redis/redis_data/users.acl" && ! -L "$work_dir/redis/redis_data/users.acl" &&
       -s "$work_dir/redis/redis_data/users.acl" ]] ||
      fail "Redis recovery artifact must contain non-empty dump.rdb and users.acl"
    if find "$work_dir/redis/redis_data" -xdev \( -iname 'appendonly*' -o -iname '*.aof' \) -print -quit | grep -q .; then
      fail "Redis recovery artifact contains legacy AOF state and cannot be used"
    fi
    [[ -d "$work_dir/app/data" && ! -L "$work_dir/app/data" ]] || fail "Sub2API application data archive layout is invalid"
    cp -a "$work_dir/app/data"/. "$data_dir"/
    cp -a "$work_dir/redis/redis_data/dump.rdb" "$work_dir/redis/redis_data/users.acl" "$redis_dir"/
    app_uid="$(jq -er .app.uid "$state_file")"; app_gid="$(jq -er .app.gid "$state_file")"
    pg_uid="$(jq -er .postgres.uid "$state_file")"; pg_gid="$(jq -er .postgres.gid "$state_file")"
    redis_uid="$(jq -er .redis.uid "$state_file")"; redis_gid="$(jq -er .redis.gid "$state_file")"
    chown -R "$app_uid:$app_gid" "$data_dir"
    chown -R "$pg_uid:$pg_gid" "$postgres_dir"
    chown -R "$redis_uid:$redis_gid" "$redis_dir"
    database="$(jq -er .postgres.database "$state_file")"; user="$(jq -er .postgres.user "$state_file")"
    pg_image="$(jq -er .postgres.image "$state_file")"; redis_image="$(jq -er .redis.image "$state_file")"
    pg_password="$(container_env "$POSTGRES_CONTAINER" POSTGRES_PASSWORD 2>/dev/null || true)"
    redis_password="$(container_env "$REDIS_CONTAINER" REDISCLI_AUTH 2>/dev/null || true)"
    [[ -n "$pg_password" && "$pg_password" != *$'\n'* && "$pg_password" != *$'\r'* ]] || fail "PostgreSQL password is unavailable or unsafe"
    [[ -n "$redis_password" && "$redis_password" != *$'\n'* && "$redis_password" != *$'\r'* ]] || fail "Redis password is unavailable or unsafe"
    update_state ".staging = {dataDir:\$data,postgresDir:\$postgres,redisDir:\$redis}" \
      --arg data "$data_dir" --arg postgres "$postgres_dir" --arg redis "$redis_dir"
    create_staging_postgres "$postgres_artifact" "$postgres_dir" "$pg_image" "$database" "$user" "$pg_password"
    validate_staging_redis "$redis_dir" "$redis_image" "$redis_password"
    python3 "$ENV_SWITCHER" --file "$ENV_FILE" --backup "$operation_dir/sub2api.env.before" \
      --set "SUB2API_DATA_DIR=$data_dir" --set "SUB2API_POSTGRES_DATA_DIR=$postgres_dir" \
      --set "SUB2API_REDIS_DATA_DIR=$redis_dir"
    compose config --quiet >/dev/null
    compose up -d --force-recreate "$POSTGRES_SERVICE" "$REDIS_SERVICE" >/dev/null
    update_state '.restored = true'
    result "Sub2API 已恢复到三个全新目录并切换 Compose 数据绑定" \
      "$(jq -c '{staging,previous}' "$state_file")" true
    ;;
  verify)
    validate_contract
    require_state
    [[ "$(jq -r .restored "$state_file")" == true ]] || fail "restore has not completed"
    data_dir="$(jq -er .staging.dataDir "$state_file")"; postgres_dir="$(jq -er .staging.postgresDir "$state_file")"; redis_dir="$(jq -er .staging.redisDir "$state_file")"
    for directory in "$data_dir" "$postgres_dir" "$redis_dir"; do
      safe_data_path "$directory"
      [[ -d "$directory" && ! -L "$directory" ]] || fail "restored Sub2API data directory is unavailable: $directory"
    done
    [[ "$(python3 "$ENV_SWITCHER" --file "$ENV_FILE" --get SUB2API_DATA_DIR --default '')" == "$data_dir" ]] || fail "Sub2API data binding drifted"
    [[ "$(python3 "$ENV_SWITCHER" --file "$ENV_FILE" --get SUB2API_POSTGRES_DATA_DIR --default '')" == "$postgres_dir" ]] || fail "PostgreSQL data binding drifted"
    [[ "$(python3 "$ENV_SWITCHER" --file "$ENV_FILE" --get SUB2API_REDIS_DATA_DIR --default '')" == "$redis_dir" ]] || fail "Redis data binding drifted"
    [[ "$(docker inspect --format '{{.State.Health.Status}}' "$POSTGRES_CONTAINER")" == healthy ]] || fail "Sub2API PostgreSQL is not healthy"
    [[ "$(docker inspect --format '{{.State.Health.Status}}' "$REDIS_CONTAINER")" == healthy ]] || fail "Sub2API Redis is not healthy"
    [[ "$(docker inspect --format '{{.State.Status}}' "$APP_CONTAINER")" != running ]] || fail "Sub2API application must remain stopped before resume"
    [[ "$(container_mount_source "$POSTGRES_CONTAINER" /var/lib/postgresql/data)" == "$postgres_dir" ]] || fail "PostgreSQL container mount did not switch"
    [[ "$(container_mount_source "$REDIS_CONTAINER" /data)" == "$redis_dir" ]] || fail "Redis container mount did not switch"
    [[ -f "$redis_dir/dump.rdb" && ! -L "$redis_dir/dump.rdb" && -s "$redis_dir/dump.rdb" &&
       -f "$redis_dir/users.acl" && ! -L "$redis_dir/users.acl" && -s "$redis_dir/users.acl" ]] ||
      fail "restored Redis RDB or ACL file is missing"
    database="$(jq -er .postgres.database "$state_file")"; user="$(jq -er .postgres.user "$state_file")"
    migrations="$(docker exec "$POSTGRES_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$user" -d "$database" -Atc 'select count(*) from schema_migrations')"
    [[ "$migrations" =~ ^[0-9]+$ ]] || fail "restored migration count is invalid"
    docker exec "$REDIS_CONTAINER" redis-cli ping | grep -Fxq PONG || fail "restored Redis ping failed"
    touch "$verified_file"; chmod 0600 "$verified_file"
    update_state '.verified = true'
    result "Sub2API 恢复后数据库迁移、Redis ACL/RDB 与数据绑定验证通过" \
      "$(jq -cn --argjson migrations "$migrations" '{migrations:$migrations}')" true
    ;;
  resume)
    validate_contract
    require_state
    [[ -f "$verified_file" && ! -L "$verified_file" ]] || fail "restore verification evidence is missing"
    [[ "$(jq -r .verified "$state_file")" == true ]] || fail "restore state is not verified"
    data_dir="$(jq -er .staging.dataDir "$state_file")"
    compose up -d --force-recreate "$APP_SERVICE" >/dev/null
    [[ "$(container_mount_source "$APP_CONTAINER" /app/data)" == "$data_dir" ]] || fail "Sub2API application container mount did not switch"
    for _ in $(seq 1 180); do
      if curl -fsS "$HEALTH_URL" >/dev/null 2>&1; then break; fi
      sleep 1
    done
    curl -fsS "$HEALTH_URL" >/dev/null || fail "Sub2API resume health check failed"
    touch "$resumed_file"; chmod 0600 "$resumed_file"
    update_state '.resumed = true'
    result "Sub2API 生产服务已恢复并完成最终健康检查" '{}' true
    ;;
esac
