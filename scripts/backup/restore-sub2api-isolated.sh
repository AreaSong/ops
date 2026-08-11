#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

action="${1:-}"
phase="${2:-}"
operation_dir="${3:-}"
target="${4:-}"

BACKUP_ROOT="${BACKUP_ROOT:-/var/backups/ops}"
PREPARED_DIR="${SUB2API_PREPARED_RELEASE_DIR:-/var/lib/areasong-ops/prepared-releases/sub2api}"
ENV_FILE="${SUB2API_RESTORE_ENV_FILE:-/opt/services/sub2api/.env}"
BACKUP_POSTGRES="${SUB2API_RESTORE_BACKUP_POSTGRES:-/opt/ops/scripts/backup/backup-postgres.sh}"
BACKUP_REDIS="${SUB2API_RESTORE_BACKUP_REDIS:-/opt/ops/scripts/backup/backup-redis.sh}"
BACKUP_VOLUMES="${SUB2API_RESTORE_BACKUP_VOLUMES:-/opt/ops/scripts/backup/backup-volumes.sh}"
METRIC_OUT="${SUB2API_RESTORE_METRIC_OUT:-/var/lib/node_exporter/textfile_collector/sub2api-restore-drill.prom}"
APP_CONTAINER="${SUB2API_RESTORE_APP_CONTAINER:-sub2api}"
POSTGRES_CONTAINER="${SUB2API_RESTORE_POSTGRES_CONTAINER:-sub2api-postgres}"
REDIS_CONTAINER="${SUB2API_RESTORE_REDIS_CONTAINER:-sub2api-redis}"
IMAGE_REPOSITORY="${SUB2API_RESTORE_IMAGE_REPOSITORY:-weishaw/sub2api}"

fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
result() {
  local summary="$1" data="${2:-\{\}}"
  jq -cn --arg summary "$summary" --argjson data "$data" '{ok:true,summary:$summary,data:$data}'
}

[[ "$action" == prepare || "$action" == restore-drill ]] || fail "unsupported action"
[[ "$phase" =~ ^(preflight|backup|drill|verify|publish)$ ]] || fail "unsupported phase"
[[ -d "$operation_dir" && ! -L "$operation_dir" ]] || fail "operation directory is unsafe"
[[ "$(id -u)" -eq 0 ]] || fail "restore drill must run as root"

for command_name in curl docker find gzip install jq mktemp python3 sha256sum; do
  command -v "$command_name" >/dev/null || fail "missing command: $command_name"
done
for path in "$ENV_FILE" "$BACKUP_POSTGRES" "$BACKUP_REDIS" "$BACKUP_VOLUMES"; do
  [[ -f "$path" && ! -L "$path" ]] || fail "required path is missing or unsafe: $path"
done

state_file="$operation_dir/sub2api-drill-state.json"
backup_file="$operation_dir/sub2api-drill-backups.json"
drill_result="$operation_dir/isolated-drill-result.txt"

image_identity() {
  local reference="$1" inspected image_id version commit projection identity_hash
  inspected="$(docker image inspect "$reference")"
  image_id="$(jq -er '.[0].Id' <<<"$inspected")"
  version="$(jq -er '.[0].Config.Labels["org.opencontainers.image.version"]' <<<"$inspected")"
  commit="$(jq -er '.[0].Config.Labels["org.opencontainers.image.revision"]' <<<"$inspected")"
  projection="$(jq -cnS --arg version "$version" --arg image "$reference" --arg imageId "$image_id" --arg gitCommit "$commit" \
    '{version:$version,image:$image,imageId:$imageId,gitCommit:$gitCommit}')"
  identity_hash="sha256:$(printf '%s' "$projection" | sha256sum | awk '{print $1}')"
  jq -cnS --arg version "$version" --arg image "$reference" --arg imageId "$image_id" \
    --arg gitCommit "$commit" --arg runtimeIdentityHash "$identity_hash" \
    '{version:$version,image:$image,imageId:$imageId,gitCommit:$gitCommit,runtimeIdentityHash:$runtimeIdentityHash}'
}

production_database() {
  local user database migrations
  user="$(docker inspect "$POSTGRES_CONTAINER" | jq -er '.[0].Config.Env[] | select(startswith("POSTGRES_USER=")) | split("=")[1]')"
  database="$(docker inspect "$POSTGRES_CONTAINER" | jq -er '.[0].Config.Env[] | select(startswith("POSTGRES_DB=")) | split("=")[1]')"
  migrations="$(docker exec "$POSTGRES_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$user" -d "$database" -Atc 'SELECT count(*) FROM schema_migrations;')"
  [[ "$migrations" =~ ^[0-9]+$ ]] || fail "production migration baseline is invalid"
  jq -cn --arg user "$user" --arg database "$database" --argjson migrations "$migrations" \
    '{user:$user,database:$database,migrations:$migrations}'
}

latest_fresh() {
  local pattern="$1" started="$2"
  find "$BACKUP_ROOT" -type f -name "$pattern" -newermt "@${started}" -printf '%T@ %p\n' |
    sort -nr | awk 'NR == 1 {sub(/^[^ ]+ /, ""); print}'
}

case "$phase" in
  preflight)
    if [[ "$action" == prepare ]]; then
      [[ "$target" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([+-][0-9A-Za-z.-]+)?$ ]] || fail "target release tag is invalid"
      image_tag="${target#v}"
      target_reference="${IMAGE_REPOSITORY}:${image_tag}"
      docker pull "$target_reference" >/dev/null
      target_digest="$(docker image inspect "$target_reference" | jq -er --arg repository "$IMAGE_REPOSITORY" \
        '.[0].RepoDigests[] | select(startswith($repository+"@sha256:"))' | head -1)"
      target_identity="$(image_identity "$target_digest")"
      [[ "v$(jq -r .version <<<"$target_identity")" == "$target" ]] || fail "target image version label mismatch"
    else
      [[ -z "$target" ]] || fail "restore drill does not accept a target"
      target_digest="$(docker inspect --format '{{.Config.Image}}' "$APP_CONTAINER")"
      target_identity="$(image_identity "$target_digest")"
    fi
    current_reference="$(docker inspect --format '{{.Config.Image}}' "$APP_CONTAINER")"
    current_identity="$(image_identity "$current_reference")"
    production_database="$(production_database)"
    baseline="$(jq -er .migrations <<<"$production_database")"
    postgres_image="$(docker inspect --format '{{.Config.Image}}' "$POSTGRES_CONTAINER")"
    redis_image="$(docker inspect --format '{{.Config.Image}}' "$REDIS_CONTAINER")"
    jq -nS --arg action "$action" --arg target "$target" --argjson startedAt "$(date +%s)" \
      --argjson baselineMigrations "$baseline" --argjson current "$current_identity" --argjson targetIdentity "$target_identity" \
      --arg postgresImage "$postgres_image" --arg redisImage "$redis_image" --argjson productionDatabase "$production_database" \
      '{schemaVersion:1,action:$action,target:$target,startedAt:$startedAt,baselineMigrations:$baselineMigrations,
        current:$current,targetIdentity:$targetIdentity,postgresImage:$postgresImage,redisImage:$redisImage,
        productionDatabase:$productionDatabase}' >"$state_file"
    chmod 0600 "$state_file"
    result "生产身份、目标镜像与迁移基线已锁定" "$(jq -c '{target,baselineMigrations,targetIdentity:{version,image,imageId,gitCommit}}' "$state_file")"
    ;;
  backup)
    [[ -f "$state_file" && ! -L "$state_file" ]] || fail "drill state is missing"
    "$BACKUP_POSTGRES" >/dev/null
    "$BACKUP_REDIS" >/dev/null
    "$BACKUP_VOLUMES" >/dev/null
    started="$(jq -er .startedAt "$state_file")"
    postgres_backup="$(latest_fresh 'sub2api-postgres-*.sql.gz' "$started")"
    redis_backup="$(latest_fresh 'redis-*.tar.gz' "$started")"
    data_backup="$(latest_fresh 'sub2api-data-*.tar.gz' "$started")"
    for artifact in "$postgres_backup" "$redis_backup" "$data_backup"; do
      [[ -n "$artifact" && -f "$artifact" && ! -L "$artifact" ]] || fail "fresh Sub2API backup artifact is missing"
    done
    jq -nS --arg postgres "$postgres_backup" --arg redis "$redis_backup" --arg data "$data_backup" \
      --arg postgresSha "sha256:$(sha256sum "$postgres_backup" | awk '{print $1}')" \
      --arg redisSha "sha256:$(sha256sum "$redis_backup" | awk '{print $1}')" \
      --arg dataSha "sha256:$(sha256sum "$data_backup" | awk '{print $1}')" \
      '{schemaVersion:1,postgres:{path:$postgres,sha256:$postgresSha},redis:{path:$redis,sha256:$redisSha},data:{path:$data,sha256:$dataSha}}' >"$backup_file"
    chmod 0600 "$backup_file"
    result "Sub2API fresh PostgreSQL、Redis 与应用卷备份完成"
    ;;
  drill)
    [[ -f "$state_file" && ! -L "$state_file" && -f "$backup_file" && ! -L "$backup_file" ]] || fail "drill inputs are missing"
    work_dir="$(mktemp -d "$operation_dir/sub2api-restore.XXXXXXXX")"
    chmod 0700 "$work_dir"
    project="ops-sub2api-$(basename "$operation_dir" | tr -cd 'a-zA-Z0-9' | tail -c 24)"
    compose_file="$work_dir/compose.yml"
    cleanup() {
      set +e
      docker compose --project-name "$project" --env-file "$ENV_FILE" -f "$compose_file" down -v --remove-orphans >/dev/null 2>&1
      rm -rf "$work_dir"
    }
    trap cleanup EXIT INT TERM
    postgres_backup="$(jq -er .postgres.path "$backup_file")"
    redis_backup="$(jq -er .redis.path "$backup_file")"
    data_backup="$(jq -er .data.path "$backup_file")"
    for key in postgres redis data; do
      artifact="$(jq -er --arg key "$key" '.[$key].path' "$backup_file")"
      expected="$(jq -er --arg key "$key" '.[$key].sha256' "$backup_file")"
      [[ "sha256:$(sha256sum "$artifact" | awk '{print $1}')" == "$expected" ]] || fail "$key backup digest mismatch"
    done
    install -d -m 0700 "$work_dir/redis" "$work_dir/app"
    python3 /opt/ops/scripts/backup/backup_manifest.py extract-tar --archive "$redis_backup" --destination "$work_dir/redis"
    python3 /opt/ops/scripts/backup/backup_manifest.py extract-tar --archive "$data_backup" --destination "$work_dir/app"
    [[ -f "$work_dir/redis/redis_data/dump.rdb" && -d "$work_dir/app/data" ]] || fail "restored Redis or application data is incomplete"
    chown -R 999:1000 "$work_dir/redis/redis_data"
    chown -R 1000:1000 "$work_dir/app/data"
    target_image="$(jq -er .targetIdentity.image "$state_file")"
    current_image="$(jq -er .current.image "$state_file")"
    postgres_image="$(jq -er .postgresImage "$state_file")"
    redis_image="$(jq -er .redisImage "$state_file")"
    cat >"$compose_file" <<'YAML'
services:
  postgres:
    image: ${DRILL_POSTGRES_IMAGE}
    environment:
      POSTGRES_USER: postgres
      POSTGRES_DB: postgres
      POSTGRES_HOST_AUTH_METHOD: trust
    volumes:
      - pgdata:/var/lib/postgresql/data
    networks: [drill]
  redis:
    image: ${DRILL_REDIS_IMAGE}
    user: "999:1000"
    env_file: ${DRILL_ENV_FILE}
    command: ["sh", "-c", "exec redis-server --appendonly yes --aclfile /data/users.acl --requirepass \"$$REDISCLI_AUTH\""]
    environment:
      REDISCLI_AUTH: ${REDIS_PASSWORD:-}
    volumes:
      - ${DRILL_REDIS_DIR}:/data
    networks: [drill]
  app:
    image: ${DRILL_APP_IMAGE}
    user: "1000:1000"
    read_only: true
    env_file: ${DRILL_ENV_FILE}
    environment:
      AUTO_SETUP: "true"
      SERVER_HOST: 0.0.0.0
      SERVER_PORT: "8080"
      HOME: /tmp
      TMPDIR: /tmp
      DATABASE_HOST: postgres
      DATABASE_PORT: "5432"
      DATABASE_USER: ${POSTGRES_USER:-sub2api}
      DATABASE_PASSWORD: ${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}
      DATABASE_DBNAME: ${POSTGRES_DB:-sub2api}
      DATABASE_SSLMODE: disable
      REDIS_HOST: redis
      REDIS_PORT: "6379"
      REDIS_PASSWORD: ${REDIS_PASSWORD:-}
    tmpfs:
      - /tmp:rw,noexec,nosuid,nodev,size=64m,mode=1777
    volumes:
      - ${DRILL_APP_DIR}:/app/data
    networks: [drill]
volumes:
  pgdata:
networks:
  drill:
    internal: true
YAML
    export DRILL_POSTGRES_IMAGE="$postgres_image" DRILL_REDIS_IMAGE="$redis_image" DRILL_APP_IMAGE="$target_image"
    export DRILL_ENV_FILE="$ENV_FILE" DRILL_REDIS_DIR="$work_dir/redis/redis_data" DRILL_APP_DIR="$work_dir/app/data"
    drill_compose() {
      docker compose --project-name "$project" --env-file "$ENV_FILE" -f "$compose_file" "$@"
    }
    drill_compose config --quiet
    drill_compose up -d postgres redis
    postgres_id="$(drill_compose ps -q postgres)"
    redis_id="$(drill_compose ps -q redis)"
    for _ in $(seq 1 90); do docker exec "$postgres_id" pg_isready -U postgres -d postgres >/dev/null 2>&1 && break; sleep 1; done
    docker exec "$postgres_id" pg_isready -U postgres -d postgres >/dev/null || fail "isolated PostgreSQL did not become ready"
    gzip -dc "$postgres_backup" | docker exec -i "$postgres_id" psql -v ON_ERROR_STOP=1 -U postgres -d postgres >/dev/null
    for _ in $(seq 1 60); do docker exec "$redis_id" redis-cli ping >/dev/null 2>&1 && break; sleep 1; done
    docker exec "$redis_id" redis-cli ping >/dev/null || fail "isolated Redis did not become ready"
    drill_compose up -d app
    app_id="$(drill_compose ps -q app)"
    for _ in $(seq 1 180); do docker exec "$app_id" wget -qO /dev/null http://127.0.0.1:8080/health >/dev/null 2>&1 && break; sleep 1; done
    docker exec "$app_id" wget -qO /dev/null http://127.0.0.1:8080/health || fail "target image did not become healthy"
    database="$(jq -er .productionDatabase.database "$state_file")"
    target_migrations="$(docker exec "$postgres_id" psql -v ON_ERROR_STOP=1 -U postgres -d "$database" -Atc 'SELECT count(*) FROM schema_migrations;')"
    [[ "$target_migrations" =~ ^[0-9]+$ ]] || fail "target migration count is invalid"
    drill_compose stop app >/dev/null
    export DRILL_APP_IMAGE="$current_image"
    drill_compose up -d --force-recreate app
    app_id="$(drill_compose ps -q app)"
    for _ in $(seq 1 180); do docker exec "$app_id" wget -qO /dev/null http://127.0.0.1:8080/health >/dev/null 2>&1 && break; sleep 1; done
    docker exec "$app_id" wget -qO /dev/null http://127.0.0.1:8080/health || fail "old image is not healthy on migrated schema"
    docker exec "$app_id" wget -qO /dev/null http://127.0.0.1:8080/api/v1/settings/public || fail "old image public smoke failed"
    baseline="$(jq -er .baselineMigrations "$state_file")"
    {
      printf 'result=success\n'
      printf 'base_migrations=%s\n' "$baseline"
      printf 'target_migrations=%s\n' "$target_migrations"
      printf 'old_image_on_new_schema=healthy\n'
      printf 'network_internal=true\n'
      printf 'host_ports=false\n'
    } >"$drill_result"
    chmod 0600 "$drill_result"
    result "隔离恢复、目标迁移与旧镜像兼容演练完成" "$(jq -cn --argjson baseline "$baseline" --argjson targetMigrations "$target_migrations" '{baselineMigrations:$baseline,targetMigrations:$targetMigrations,oldImageOnNewSchema:"healthy"}')"
    ;;
  verify)
    [[ -f "$drill_result" && ! -L "$drill_result" ]] || fail "drill result is missing"
    grep -Fxq 'result=success' "$drill_result" || fail "drill did not succeed"
    grep -Fxq 'old_image_on_new_schema=healthy' "$drill_result" || fail "old image compatibility was not proven"
    completed="$(date +%s)"
    install -d -m 0755 "$(dirname "$METRIC_OUT")"
    temporary_metric="$(mktemp "$(dirname "$METRIC_OUT")/.sub2api-restore.XXXXXX")"
    printf 'sub2api_restore_drill_last_success_timestamp_seconds %s\n' "$completed" >"$temporary_metric"
    chmod 0644 "$temporary_metric"
    mv -f "$temporary_metric" "$METRIC_OUT"
    result "Sub2API 隔离演练证据验证通过"
    ;;
  publish)
    [[ "$action" == prepare ]] || fail "restore drill has no publish phase"
    [[ -f "$state_file" && -f "$backup_file" && -f "$drill_result" ]] || fail "publish evidence is incomplete"
    tag="$(jq -er .target "$state_file")"
    evidence_dir="$BACKUP_ROOT/change/$(date -u +%Y%m%dT%H%M%SZ)-sub2api-${tag//./}"
    install -d -m 0700 "$evidence_dir"
    install -m 0600 "$state_file" "$backup_file" "$drill_result" "$evidence_dir/"
    (cd "$evidence_dir" && sha256sum sub2api-drill-state.json sub2api-drill-backups.json isolated-drill-result.txt >SHA256SUMS)
    chmod 0600 "$evidence_dir/SHA256SUMS"
    manifest_sha="sha256:$(sha256sum "$evidence_dir/SHA256SUMS" | awk '{print $1}')"
    baseline="$(jq -er .baselineMigrations "$state_file")"
    target_migrations="$(awk -F= '$1 == "target_migrations" {print $2}' "$drill_result")"
    install -d -m 0700 "$PREPARED_DIR"
    record_tmp="$(mktemp "$PREPARED_DIR/.${tag}.XXXXXX")"
    jq -nS --arg tag "$tag" --arg evidencePath "$evidence_dir" --arg manifestSha256 "$manifest_sha" \
      --argjson expected "$(jq -c '.current' "$state_file")" --argjson targetIdentity "$(jq -c '.targetIdentity' "$state_file")" \
      --argjson baseline "$baseline" --argjson targetMigrations "$target_migrations" \
      '{tag:$tag,status:"prepared",requiresMigration:($targetMigrations != $baseline),
        expectedBefore:($expected | {currentVersion:.version,currentImage:.image,currentImageId:.imageId,runtimeIdentityHash,
          gitCommit,autoApply:"none",signatureRequired:false,rollbackAvailable:true,
          rollbackTargetVersion:.version,rollbackTargetImage:.image,rollbackSourceRecordSha256:$manifestSha256}),
        target:{version:$targetIdentity.version,image:$targetIdentity.image,imageId:$targetIdentity.imageId,
          runtimeIdentityHash:$targetIdentity.runtimeIdentityHash,gitCommit:$targetIdentity.gitCommit},
        migration:{baselineCount:$baseline,targetCount:$targetMigrations,newCount:($targetMigrations-$baseline)},
        rehearsal:{evidencePath:$evidencePath,manifestSha256:$manifestSha256,networkInternal:true,hostPorts:false,oldImageOnNewSchema:"healthy"}}' >"$record_tmp"
    chmod 0600 "$record_tmp"
    mv -f "$record_tmp" "$PREPARED_DIR/${tag}.json"
    result "受控发布准备记录已发布" "$(jq -c '{tag,status,migration,rehearsal}' "$PREPARED_DIR/${tag}.json")"
    ;;
esac
