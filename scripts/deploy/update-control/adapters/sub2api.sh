#!/usr/bin/env bash
set -Eeuo pipefail

umask 077
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CATALOG="${SUB2API_UPDATE_CONTROL_RELEASES:-$SCRIPT_DIR/../releases/sub2api.json}"
CONTROLLED_COMPOSE="${SUB2API_UPDATE_CONTROL_CONTROLLED_COMPOSE:-/opt/ops/services/sub2api/compose.yml}"
RUNTIME_COMPOSE="${SUB2API_UPDATE_CONTROL_RUNTIME_COMPOSE:-/opt/services/sub2api/compose.yml}"
ENV_FILE="${SUB2API_UPDATE_CONTROL_ENV_FILE:-/opt/services/sub2api/.env}"
BACKUP_POSTGRES="${SUB2API_UPDATE_CONTROL_BACKUP_POSTGRES:-/opt/ops/scripts/backup/backup-postgres.sh}"
BACKUP_REDIS="${SUB2API_UPDATE_CONTROL_BACKUP_REDIS:-/opt/ops/scripts/backup/backup-redis.sh}"
BACKUP_VOLUMES="${SUB2API_UPDATE_CONTROL_BACKUP_VOLUMES:-/opt/ops/scripts/backup/backup-volumes.sh}"
APP_CONTAINER="${SUB2API_UPDATE_CONTROL_APP_CONTAINER:-sub2api}"
POSTGRES_CONTAINER="${SUB2API_UPDATE_CONTROL_POSTGRES_CONTAINER:-sub2api-postgres}"
REDIS_CONTAINER="${SUB2API_UPDATE_CONTROL_REDIS_CONTAINER:-sub2api-redis}"
BASE_URL="${SUB2API_UPDATE_CONTROL_BASE_URL:-http://127.0.0.1:8080}"
TEMP_ADMIN_KEY=""

fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
result() { jq -cn --arg phase "$1" --arg detail "$2" '{ok:true,phase:$phase,detail:$detail}'; }
sha256_text() { printf '%s' "$1" | sha256sum | awk '{print "sha256:"$1}'; }

phase="${1:-}"
request="${2:-}"
operation_dir="${3:-}"
[[ "$phase" =~ ^(preflight|backup|migration|apply|health|smoke|identity|rollback)$ ]] || fail "unsupported phase"
[[ -f "$request" && ! -L "$request" ]] || fail "request must be a regular file"
[[ -d "$operation_dir" && ! -L "$operation_dir" ]] || fail "operation directory is unsafe"
DEPENDENCY_BASELINE="${SUB2API_UPDATE_CONTROL_DEPENDENCY_BASELINE:-$operation_dir/dependencies.before.json}"
target_id="$(jq -er '.targetId' "$request")"
jq -e --arg target "$target_id" '.schemaVersion == 1 and (.targets[$target] | type == "object")' "$CATALOG" >/dev/null ||
  fail "target is not present in the Sub2API release catalog"

catalog_value() { jq -er --arg target "$target_id" ".targets[\$target].$1" "$CATALOG"; }
target_image="$(catalog_value target.image)"
target_image_id="$(catalog_value target.imageId)"
target_version="$(catalog_value target.version)"
target_git_commit="$(catalog_value target.gitCommit)"
old_image="$(catalog_value expectedBefore.currentImage)"
old_version="$(catalog_value expectedBefore.currentVersion)"
old_image_id="$(catalog_value expectedBefore.currentImageId)"
old_git_commit="$(catalog_value expectedBefore.gitCommit)"
baseline_migrations="$(catalog_value migration.baselineCount)"
target_migrations="$(catalog_value migration.targetCount)"
rehearsal_path="$(catalog_value rehearsal.evidencePath)"
rehearsal_manifest_sha="$(catalog_value rehearsal.manifestSha256)"

image_identity() {
  local image="$1"
  docker image inspect "$image" | jq -ceS '.[0] | {
    imageId:.Id,
    version:.Config.Labels["org.opencontainers.image.version"],
    gitCommit:.Config.Labels["org.opencontainers.image.revision"]
  }'
}

runtime_identity_hash() {
  local version="$1" image="$2" image_id="$3" git_commit="$4" projection
  projection="$(jq -cnS --arg version "$version" --arg image "$image" --arg imageId "$image_id" \
    --arg gitCommit "$git_commit" '{version:$version,image:$image,imageId:$imageId,gitCommit:$gitCommit}')"
  sha256_text "$projection"
}

observed_identity() {
  local image image_id labels version git_commit runtime_hash
  image="$(docker inspect --format '{{.Config.Image}}' "$APP_CONTAINER")"
  image_id="$(docker inspect --format '{{.Image}}' "$APP_CONTAINER")"
  labels="$(image_identity "$image_id")"
  version="$(jq -er '.version' <<<"$labels")"
  git_commit="$(jq -er '.gitCommit' <<<"$labels")"
  runtime_hash="$(runtime_identity_hash "$version" "$image" "$image_id" "$git_commit")"
  jq -cnS --arg currentVersion "$version" --arg currentImage "$image" --arg currentImageId "$image_id" \
    --arg runtimeIdentityHash "$runtime_hash" \
    '{currentVersion:$currentVersion,currentImage:$currentImage,currentImageId:$currentImageId,runtimeIdentityHash:$runtimeIdentityHash}'
}

assert_expected_before() {
  local observed expected policy catalog_policy
  observed="$(observed_identity)"
  expected="$(jq -cS '.expectedBefore | {currentVersion,currentImage,currentImageId,runtimeIdentityHash}' "$request")"
  [[ "$observed" == "$expected" ]] || fail "EXPECTED_BEFORE_MISMATCH observed=$observed"
  policy="$(jq -cS '.expectedBefore | {autoApply,signatureRequired,rollbackAvailable,rollbackTargetVersion,rollbackTargetImage,rollbackSourceRecordSha256}' "$request")"
  catalog_policy="$(jq -cS --arg target "$target_id" '.targets[$target].expectedBefore | {autoApply,signatureRequired,rollbackAvailable,rollbackTargetVersion,rollbackTargetImage,rollbackSourceRecordSha256}' "$CATALOG")"
  [[ "$policy" == "$catalog_policy" ]] || fail "expected-before policy does not match the release catalog"
}

container_identity() {
  docker inspect "$@" | jq -cS '[.[] | {name:.Name,id:.Id,startedAt:.State.StartedAt,image:.Config.Image}]'
}

assert_dependencies_unchanged() {
  local expected observed
  [[ -f "$DEPENDENCY_BASELINE" && ! -L "$DEPENDENCY_BASELINE" ]] || fail "dependency baseline is missing or unsafe"
  expected="$(jq -cS . "$DEPENDENCY_BASELINE")"
  observed="$(container_identity "$POSTGRES_CONTAINER" "$REDIS_CONTAINER")"
  [[ "$observed" == "$expected" ]] || fail "PostgreSQL or Redis container identity changed"
}

psql_value() {
  local sql="$1" db_user db_name
  db_user="$(docker inspect "$POSTGRES_CONTAINER" | jq -er '.[0].Config.Env[] | select(startswith("POSTGRES_USER=")) | split("=")[1]')"
  db_name="$(docker inspect "$POSTGRES_CONTAINER" | jq -er '.[0].Config.Env[] | select(startswith("POSTGRES_DB=")) | split("=")[1]')"
  docker exec "$POSTGRES_CONTAINER" psql -v ON_ERROR_STOP=1 -U "$db_user" -d "$db_name" -Atc "$sql"
}

migration_count() {
  psql_value 'SELECT count(*) FROM schema_migrations;'
}

wait_for_health() {
  local code body
  body="$(mktemp "$operation_dir/.health.XXXXXX")"
  for _ in $(seq 1 120); do
    code="$(curl -sS -o "$body" -w '%{http_code}' "$BASE_URL/health" || true)"
    if [[ "$code" == 200 ]] && jq -e '.status == "ok" or .ok == true' "$body" >/dev/null 2>&1; then
      rm -f "$body"
      return
    fi
    if ! docker inspect --format '{{.State.Running}}' "$APP_CONTAINER" 2>/dev/null | grep -Fxq true; then
      rm -f "$body"
      fail "application exited before health passed"
    fi
    sleep 1
  done
  rm -f "$body"
  fail "application did not become healthy"
}

replace_compose_image() {
  local path="$1" from="$2" to="$3"
  python3 - "$path" "$from" "$to" <<'PY'
import os
import stat
import sys
from pathlib import Path

path = Path(sys.argv[1])
old = f"    image: {sys.argv[2]}\n"
new = f"    image: {sys.argv[3]}\n"
content = path.read_text(encoding="utf-8")
if content.count(old) != 1:
    raise SystemExit(f"expected exactly one pinned application image in {path}")
temporary = path.with_name(f".{path.name}.update-control.tmp")
temporary.write_text(content.replace(old, new), encoding="utf-8")
os.chmod(temporary, stat.S_IMODE(path.stat().st_mode))
os.replace(temporary, path)
PY
}

assert_target_image() {
  local actual labels
  actual="$(docker inspect --format '{{.Config.Image}}' "$APP_CONTAINER")"
  [[ "$actual" == "$target_image" ]] || fail "target Docker image reference mismatch"
  [[ "$(docker inspect --format '{{.Image}}' "$APP_CONTAINER")" == "$target_image_id" ]] || fail "target Docker Image ID mismatch"
  labels="$(image_identity "$target_image_id")"
  jq -e --arg version "$target_version" --arg commit "$target_git_commit" --arg imageId "$target_image_id" \
    '.version == $version and .gitCommit == $commit and .imageId == $imageId' <<<"$labels" >/dev/null ||
    fail "target OCI version, Git commit and Docker identity disagree"
}

cleanup_temp_admin_key() {
  if [[ -n "$TEMP_ADMIN_KEY" ]]; then
    psql_value "DELETE FROM settings WHERE key='admin_api_key' AND value='$TEMP_ADMIN_KEY';" >/dev/null 2>&1 || true
    TEMP_ADMIN_KEY=""
  fi
}
trap cleanup_temp_admin_key EXIT

http_json() {
  local label="$1" path="$2" header_name="${3:-}" header_value="${4:-}" body code
  body="$(mktemp "$operation_dir/.http-$label.XXXXXX")"
  if [[ -n "$header_name" ]]; then
    code="$(curl -sS -o "$body" -w '%{http_code}' -H "$header_name: $header_value" "$BASE_URL$path")"
  else
    code="$(curl -sS -o "$body" -w '%{http_code}' "$BASE_URL$path")"
  fi
  if [[ "$code" != 200 ]]; then
    rm -f "$body"
    fail "$label returned HTTP $code"
  fi
  if ! jq -e '.code == 0' "$body" >/dev/null; then
    rm -f "$body"
    fail "$label returned a non-success JSON envelope"
  fi
  if [[ "$label" == "system-version" ]]; then
    jq -cS '{version:.data.version}' "$body" >"$operation_dir/system-version-summary.json"
    chmod 0600 "$operation_dir/system-version-summary.json"
  fi
  rm -f "$body"
  printf '%s|http=200|code=0\n' "$label"
}

authenticated_smoke() {
  local admin_key admin_id user_key code smoke_file models_file
  admin_key="$(psql_value "SELECT value FROM settings WHERE key='admin_api_key' LIMIT 1;")"
  admin_id="$(psql_value "SELECT id FROM users WHERE role='admin' AND deleted_at IS NULL ORDER BY id LIMIT 1;")"
  user_key="$(psql_value "SELECT key FROM api_keys WHERE status='active' AND deleted_at IS NULL AND (expires_at IS NULL OR expires_at > NOW()) ORDER BY id LIMIT 1;")"
  [[ "$admin_id" =~ ^[0-9]+$ ]] || fail "database has no active admin user"
  [[ -n "$user_key" ]] || fail "database has no active user API key for gateway smoke"
  if [[ -z "$admin_key" ]]; then
    TEMP_ADMIN_KEY="admin-update-smoke-$(openssl rand -hex 24)"
    psql_value "INSERT INTO settings (key,value,updated_at) VALUES ('admin_api_key','$TEMP_ADMIN_KEY',NOW()) ON CONFLICT (key) DO NOTHING;" >/dev/null
    [[ "$(psql_value "SELECT value FROM settings WHERE key='admin_api_key' LIMIT 1;")" == "$TEMP_ADMIN_KEY" ]] ||
      fail "temporary admin API key was not installed atomically"
    admin_key="$TEMP_ADMIN_KEY"
  fi
  smoke_file="$operation_dir/smoke.txt"
  {
    http_json public-settings /api/v1/settings/public
    http_json admin-settings /api/v1/admin/settings x-api-key "$admin_key"
    http_json admin-accounts '/api/v1/admin/accounts?page=1&page_size=1' x-api-key "$admin_key"
    http_json admin-api-keys "/api/v1/admin/users/$admin_id/api-keys?page=1&page_size=1" x-api-key "$admin_key"
    http_json system-version /api/v1/admin/system/version x-api-key "$admin_key"
    models_file="$(mktemp "$operation_dir/.http-models.XXXXXX")"
    code="$(curl -sS -o "$models_file" -w '%{http_code}' -H "Authorization: Bearer $user_key" "$BASE_URL/v1/models")"
    rm -f "$models_file"
    [[ "$code" == 200 || "$code" == 402 || "$code" == 403 ]] || fail "gateway models returned unexpected HTTP $code"
    printf 'gateway-models|http=%s|authentication-path=accepted\n' "$code"
  } >"$smoke_file"
  chmod 0600 "$smoke_file"
  cleanup_temp_admin_key
}

case "$phase" in
  preflight)
    for command_name in curl docker jq sha256sum awk python3 openssl; do command -v "$command_name" >/dev/null || fail "missing command: $command_name"; done
    for path in "$CATALOG" "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" "$ENV_FILE" "$BACKUP_POSTGRES" "$BACKUP_REDIS" "$BACKUP_VOLUMES"; do
      [[ -e "$path" ]] || fail "required path is missing: $path"
    done
    cmp -s "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" || fail "controlled and runtime Compose files differ before update"
    assert_expected_before
    [[ "$(migration_count)" == "$baseline_migrations" ]] || fail "production migration baseline drifted"
    [[ "$(image_identity "$old_image_id" | jq -r '.gitCommit')" == "$old_git_commit" ]] || fail "current image Git commit mismatch"
    [[ "$(image_identity "$target_image_id" | jq -r '.gitCommit')" == "$target_git_commit" ]] || fail "target image Git commit mismatch"
    cp -p -- "$CONTROLLED_COMPOSE" "$operation_dir/controlled-compose.before.yml"
    cp -p -- "$RUNTIME_COMPOSE" "$operation_dir/runtime-compose.before.yml"
    container_identity "$POSTGRES_CONTAINER" "$REDIS_CONTAINER" >"$operation_dir/dependencies.before.json"
    container_identity "$APP_CONTAINER" >"$operation_dir/application.before.json"
    chmod 0600 "$operation_dir"/*
    result "$phase" "expected-before, migration baseline, Compose pair and image identities verified"
    ;;
  backup)
    "$BACKUP_POSTGRES" >"$operation_dir/backup-postgres.log" 2>&1
    "$BACKUP_REDIS" >"$operation_dir/backup-redis.log" 2>&1
    "$BACKUP_VOLUMES" >"$operation_dir/backup-volumes.log" 2>&1
    chmod 0600 "$operation_dir"/backup-*.log
    result "$phase" "fresh PostgreSQL, Redis and application-data backup jobs completed"
    ;;
  migration)
    [[ -d "$rehearsal_path" && ! -L "$rehearsal_path" ]] || fail "approved rehearsal evidence is missing"
    [[ "sha256:$(sha256sum "$rehearsal_path/SHA256SUMS" | awk '{print $1}')" == "$rehearsal_manifest_sha" ]] || fail "rehearsal manifest identity mismatch"
    (cd "$rehearsal_path" && sha256sum -c SHA256SUMS >/dev/null)
    grep -Fxq 'result=success' "$rehearsal_path/isolated-drill-result.txt" || fail "rehearsal did not succeed"
    grep -Fxq "base_migrations=$baseline_migrations" "$rehearsal_path/isolated-drill-result.txt" || fail "rehearsal baseline mismatch"
    grep -Fxq "target_migrations=$target_migrations" "$rehearsal_path/isolated-drill-result.txt" || fail "rehearsal target mismatch"
    grep -Fxq 'old_image_on_new_schema=healthy' "$rehearsal_path/isolated-drill-result.txt" || fail "automatic image rollback was not rehearsed"
    result "$phase" "isolated restore, eight migrations and old-image rollback rehearsal verified"
    ;;
  apply)
    assert_expected_before
    replace_compose_image "$CONTROLLED_COMPOSE" "$old_image" "$target_image"
    replace_compose_image "$RUNTIME_COMPOSE" "$old_image" "$target_image"
    cmp -s "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" || fail "Compose copies differ after target mutation"
    docker compose --env-file "$ENV_FILE" -f "$RUNTIME_COMPOSE" config --quiet
    docker compose --env-file "$ENV_FILE" -f "$RUNTIME_COMPOSE" up -d --no-deps --force-recreate sub2api
    assert_dependencies_unchanged
    result "$phase" "only the Sub2API application container was recreated"
    ;;
  health)
    wait_for_health
    assert_dependencies_unchanged
    result "$phase" "loopback health passed and dependency identities are unchanged"
    ;;
  smoke)
    authenticated_smoke
    assert_dependencies_unchanged
    result "$phase" "authenticated admin and gateway read-only smoke passed"
    ;;
  identity)
    assert_target_image
    [[ "$(migration_count)" == "$target_migrations" ]] || fail "target migration count mismatch"
    jq -e --arg version "$target_version" '.version == $version' "$operation_dir/system-version-summary.json" >/dev/null || fail "runtime version endpoint mismatch"
    cmp -s "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" || fail "controlled and runtime Compose files differ"
    assert_dependencies_unchanged
    result "$phase" "version, Git commit, image digest, migrations and dependency identities agree"
    ;;
  rollback)
    install -o root -g root -m 0644 "$operation_dir/controlled-compose.before.yml" "$CONTROLLED_COMPOSE"
    install -o root -g root -m 0644 "$operation_dir/runtime-compose.before.yml" "$RUNTIME_COMPOSE"
    cmp -s "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" || fail "rollback Compose copies differ"
    docker compose --env-file "$ENV_FILE" -f "$RUNTIME_COMPOSE" config --quiet
    docker compose --env-file "$ENV_FILE" -f "$RUNTIME_COMPOSE" up -d --no-deps --force-recreate sub2api
    wait_for_health
    [[ "$(docker inspect --format '{{.Config.Image}}' "$APP_CONTAINER")" == "$old_image" ]] || fail "rollback image reference mismatch"
    [[ "$(docker inspect --format '{{.Image}}' "$APP_CONTAINER")" == "$old_image_id" ]] || fail "rollback Docker Image ID mismatch"
    [[ "$(image_identity "$old_image_id" | jq -r '.version')" == "$old_version" ]] || fail "rollback version mismatch"
    [[ "$(migration_count)" == "$target_migrations" ]] || fail "rollback unexpectedly changed migrated schema"
    authenticated_smoke
    assert_dependencies_unchanged
    result "$phase" "old image restored on rehearsed new schema; PostgreSQL and Redis were not recreated"
    ;;
esac
