#!/usr/bin/env bash
set -euo pipefail

umask 077

CONFIG_FILE="${R2_VERIFY_ENV:-/etc/ops/r2-verify.env}"
BACKUP_ROOT="${BACKUP_ROOT:-/var/backups/ops}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_DIR="${RESTORE_DRILL_LOG_DIR:-/var/log/backup}"
METRIC_OUT="${RESTORE_DRILL_METRIC_OUT:-/var/lib/node_exporter/textfile_collector/areaforge-restore-drill.prom}"
LOCK_FILE="${RESTORE_DRILL_LOCK_FILE:-/run/lock/areaforge-restore-drill.lock}"
WORK_ROOT="${RESTORE_DRILL_WORK_ROOT:-/var/tmp}"
MIN_FREE_BYTES="${RESTORE_DRILL_MIN_FREE_BYTES:-1073741824}"
SPACE_MULTIPLIER="${RESTORE_DRILL_SPACE_MULTIPLIER:-2}"
MIN_UPLOAD_FILES="${RESTORE_EXPECT_UPLOAD_FILES_MIN:-1}"
MIN_OPS_STATE_FILES="${RESTORE_EXPECT_OPS_STATE_FILES_MIN:-1}"
MIN_DB_SIZE_RATIO_PERCENT="${RESTORE_DB_SIZE_MIN_RATIO_PERCENT:-25}"
SOURCE="local"
MANIFEST_ARTIFACT=""
KEEP_WORKDIR=0
COMPARE_PRODUCTION=1
POSTGRES_ARTIFACT=""
CONFIGS_ARTIFACT=""
UPLOADS_ARTIFACT=""
OPS_STATE_ARTIFACT=""
POSTGRES_IMAGE_OVERRIDE=""
DATABASE_NAME="areaforge"
PRODUCTION_CONTAINER="areaforge-postgres"
REMOTE_NAME="r2"
STARTED_AT="$(date +%s)"
DRILL_ID="areaforge-restore-$(date +%Y%m%d-%H%M%S)-$$"
WORK_DIR=""
RESTORE_CONTAINER="${DRILL_ID}-postgres"
RESTORE_VOLUME="${DRILL_ID}-pgdata"

usage() {
  cat <<'USAGE'
Usage: restore-areaforge-isolated.sh [options]

Required artifacts are paths relative to /var/backups/ops or the configured
R2 prefix. R2 restores require a manifest. Explicit paths are local-only legacy
sets and require an explicit PostgreSQL image.

Options:
  --source local|r2
  --manifest manifests/backup-set-YYYYMMDD-HHMMSS.json
  --postgres-artifact postgres/areaforge-postgres-YYYYMMDD-HHMMSS.sql.gz
  --configs-artifact configs/configs-YYYYMMDD-HHMMSS.tar.gz
  --uploads-artifact volumes/areaforge-uploads-YYYYMMDD-HHMMSS.tar.gz
  --ops-state-artifact volumes/areaforge-ops-state-YYYYMMDD-HHMMSS.tar.gz
  --postgres-image IMAGE          Required with local legacy artifacts
  --database NAME                 Restored database name (default: areaforge)
  --compare-production           Compare exact user schema/table names and size (default)
  --no-compare-production        Offline import check; does not publish success metrics
  --keep-workdir                 Keep the root-only extraction directory
  -h, --help

Use either --manifest or all four explicit artifact options.
USAGE
}

fail() {
  echo "ERROR: $*" >&2
  exit 1
}

require_value() {
  [ "$#" -ge 2 ] || fail "missing value for $1"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --source)
      require_value "$@"
      SOURCE="$2"
      shift 2
      ;;
    --manifest)
      require_value "$@"
      MANIFEST_ARTIFACT="$2"
      shift 2
      ;;
    --postgres-artifact)
      require_value "$@"
      POSTGRES_ARTIFACT="$2"
      shift 2
      ;;
    --configs-artifact)
      require_value "$@"
      CONFIGS_ARTIFACT="$2"
      shift 2
      ;;
    --uploads-artifact)
      require_value "$@"
      UPLOADS_ARTIFACT="$2"
      shift 2
      ;;
    --ops-state-artifact)
      require_value "$@"
      OPS_STATE_ARTIFACT="$2"
      shift 2
      ;;
    --postgres-image)
      require_value "$@"
      POSTGRES_IMAGE_OVERRIDE="$2"
      shift 2
      ;;
    --database)
      require_value "$@"
      DATABASE_NAME="$2"
      shift 2
      ;;
    --compare-production)
      COMPARE_PRODUCTION=1
      shift
      ;;
    --no-compare-production)
      COMPARE_PRODUCTION=0
      shift
      ;;
    --keep-workdir)
      KEEP_WORKDIR=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      usage >&2
      fail "unknown option: $1"
      ;;
  esac
done

[ "$(id -u)" -eq 0 ] || fail "this script must run as root"
case "$SOURCE" in
  local|r2) ;;
  *) fail "source must be local or r2" ;;
esac

require_uint() {
  local name="$1"
  local value="$2"
  [[ "$value" =~ ^[0-9]+$ ]] || fail "$name must be a non-negative integer"
}

require_uint RESTORE_DRILL_MIN_FREE_BYTES "$MIN_FREE_BYTES"
require_uint RESTORE_DRILL_SPACE_MULTIPLIER "$SPACE_MULTIPLIER"
require_uint RESTORE_EXPECT_UPLOAD_FILES_MIN "$MIN_UPLOAD_FILES"
require_uint RESTORE_EXPECT_OPS_STATE_FILES_MIN "$MIN_OPS_STATE_FILES"
require_uint RESTORE_DB_SIZE_MIN_RATIO_PERCENT "$MIN_DB_SIZE_RATIO_PERCENT"
[ "$SPACE_MULTIPLIER" -ge 1 ] || fail "RESTORE_DRILL_SPACE_MULTIPLIER must be at least 1"
[ "$MIN_DB_SIZE_RATIO_PERCENT" -ge 1 ] || fail "RESTORE_DB_SIZE_MIN_RATIO_PERCENT must be at least 1"

for command_name in awk cp df docker find flock gzip grep install mktemp python3 sha256sum tee wc; do
  command -v "$command_name" >/dev/null 2>&1 || fail "missing command: $command_name"
done
if [ "$SOURCE" = "r2" ]; then
  command -v rclone >/dev/null 2>&1 || fail "missing command: rclone"
fi

validate_artifact() {
  local path="$1"
  local pattern="$2"

  [ -n "$path" ] || fail "missing required artifact for pattern: $pattern"
  [[ "$path" != /* ]] || fail "artifact path must be relative: $path"
  [[ ! "$path" =~ (^|/)\.\.(/|$) ]] || fail "artifact path traversal rejected: $path"
  # shellcheck disable=SC2053
  [[ "$path" == $pattern ]] || fail "unexpected artifact path: $path"
}

if [ -n "$MANIFEST_ARTIFACT" ]; then
  [ -z "$POSTGRES_ARTIFACT$CONFIGS_ARTIFACT$UPLOADS_ARTIFACT$OPS_STATE_ARTIFACT" ] || \
    fail "do not mix --manifest with explicit artifact options"
  [ -z "$POSTGRES_IMAGE_OVERRIDE" ] || fail "--postgres-image is only valid for local legacy artifacts"
  validate_artifact "$MANIFEST_ARTIFACT" 'manifests/backup-set-*.json'
else
  [ "$SOURCE" = "local" ] || fail "R2 restores require --manifest with a trusted SHA-256 sidecar"
  validate_artifact "$POSTGRES_ARTIFACT" 'postgres/areaforge-postgres-*.sql.gz'
  validate_artifact "$CONFIGS_ARTIFACT" 'configs/configs-*.tar.gz'
  validate_artifact "$UPLOADS_ARTIFACT" 'volumes/areaforge-uploads-*.tar.gz'
  validate_artifact "$OPS_STATE_ARTIFACT" 'volumes/areaforge-ops-state-*.tar.gz'
  [ -n "$POSTGRES_IMAGE_OVERRIDE" ] || fail "local legacy artifacts require --postgres-image"
fi

LOCK_DIR="$(dirname "$LOCK_FILE")"
[ -d "$LOCK_DIR" ] || install -d -m 0755 "$LOCK_DIR"
exec 9>"$LOCK_FILE"
flock -n 9 || fail "another AreaForge restore drill is already running"

[ -d "$WORK_ROOT" ] || install -d -m 0700 "$WORK_ROOT"
[ -w "$WORK_ROOT" ] || fail "restore work root is not writable: $WORK_ROOT"
WORK_DIR="$(mktemp -d "${WORK_ROOT%/}/areaforge-restore-XXXXXXXX")"
chmod 0700 "$WORK_DIR"
install -d -m 0750 "$LOG_DIR"
LOG_FILE="$LOG_DIR/${DRILL_ID}.log"
touch "$LOG_FILE"
chmod 0640 "$LOG_FILE"
exec > >(tee -a "$LOG_FILE") 2>&1

cleanup() {
  local exit_code="$1"
  trap - EXIT INT TERM
  set +e
  docker rm -f "$RESTORE_CONTAINER" >/dev/null 2>&1
  docker volume rm "$RESTORE_VOLUME" >/dev/null 2>&1
  if [ "$KEEP_WORKDIR" -eq 0 ]; then
    rm -rf "$WORK_DIR"
  else
    echo "work directory kept: $WORK_DIR"
  fi
  if [ "$exit_code" -ne 0 ]; then
    echo "restore drill failed with exit code $exit_code"
  fi
  exit "$exit_code"
}
trap 'cleanup "$?"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

load_r2_config() {
  local upload_config="${R2_BACKUP_ENV:-/etc/ops/r2-backup.env}"

  [ -r "$CONFIG_FILE" ] || fail "R2 config is missing or unreadable: $CONFIG_FILE"
  [ "$CONFIG_FILE" != "$upload_config" ] || fail "R2 restore verification config must differ from the upload config"
  if [ -e "$upload_config" ] && [ "$CONFIG_FILE" -ef "$upload_config" ]; then
    fail "R2 restore verification config resolves to the upload config"
  fi
  set -a
  # shellcheck disable=SC1090
  . "$CONFIG_FILE"
  set +a

  local required
  for required in R2_BUCKET R2_ENDPOINT R2_PREFIX R2_ACCESS_KEY_ID R2_SECRET_ACCESS_KEY; do
    [ -n "${!required:-}" ] || fail "missing R2 config: $required"
  done

  if ! python3 - "$R2_ENDPOINT" <<'PY'
import sys
from urllib.parse import urlsplit

try:
    endpoint = urlsplit(sys.argv[1])
    port = endpoint.port
except ValueError:
    raise SystemExit(1)
valid = (
    endpoint.scheme == "https"
    and endpoint.hostname is not None
    and endpoint.username is None
    and endpoint.password is None
    and endpoint.path in ("", "/")
    and not endpoint.query
    and not endpoint.fragment
    and (port is None or 1 <= port <= 65535)
)
raise SystemExit(0 if valid else 1)
PY
  then
    fail "R2_ENDPOINT must be an HTTPS origin without credentials, path, query, or fragment"
  fi
  R2_ENDPOINT="${R2_ENDPOINT%/}"

  case "$R2_PREFIX" in
    ""|*/) ;;
    *) R2_PREFIX="${R2_PREFIX}/" ;;
  esac

  export RCLONE_CONFIG_R2_TYPE="s3"
  export RCLONE_CONFIG_R2_PROVIDER="Cloudflare"
  export RCLONE_CONFIG_R2_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
  export RCLONE_CONFIG_R2_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"
  export RCLONE_CONFIG_R2_ENDPOINT="$R2_ENDPOINT"
  export RCLONE_CONFIG_R2_REGION="auto"
}

if [ "$SOURCE" = "r2" ]; then
  load_r2_config
fi

fetch_artifact() {
  local relative_path="$1"
  local destination="$2"
  local local_path="$BACKUP_ROOT/$relative_path"

  install -d -m 0700 "$(dirname "$destination")"
  if [ "$SOURCE" = "local" ]; then
    [ -f "$local_path" ] || fail "local artifact not found: $relative_path"
    cp "$local_path" "$destination"
  else
    local remote_path="${REMOTE_NAME}:${R2_BUCKET}/${R2_PREFIX}${relative_path}"
    rclone --config /dev/null copyto "$remote_path" "$destination" \
      --s3-no-check-bucket \
      --s3-no-head \
      --log-level ERROR
  fi
  chmod 0600 "$destination"

  local downloaded_sha
  downloaded_sha="$(sha256sum "$destination" | awk '{print $1}')"
  if [ -f "$local_path" ]; then
    local local_sha
    local_sha="$(sha256sum "$local_path" | awk '{print $1}')"
    [ "$downloaded_sha" = "$local_sha" ] || fail "checksum mismatch: $relative_path"
  fi
  printf 'artifact checksum OK: %s %s\n' "$downloaded_sha" "$relative_path"
}

extract_archive() {
  local archive="$1"
  local destination="$2"
  local max_members="$3"
  local max_bytes="$4"
  shift 4
  local member_args=()
  local member

  for member in "$@"; do
    member_args+=(--member "$member")
  done
  if [ "$#" -gt 0 ]; then
    python3 "$SCRIPT_DIR/backup_manifest.py" extract-tar \
      --archive "$archive" \
      --destination "$destination" \
      --max-members "$max_members" \
      --max-bytes "$max_bytes" \
      "${member_args[@]}"
  else
    python3 "$SCRIPT_DIR/backup_manifest.py" extract-tar \
      --archive "$archive" \
      --destination "$destination" \
      --max-members "$max_members" \
      --max-bytes "$max_bytes"
  fi
}

archive_field() {
  local archive="$1"
  local field="$2"

  python3 "$SCRIPT_DIR/backup_manifest.py" archive-field \
    --archive "$archive" \
    --type tar \
    --field "$field"
}

artifact_for_role() {
  local role="$1"
  local manifest_path="$2"
  local result

  result="$(python3 "$SCRIPT_DIR/backup_manifest.py" list-artifacts \
    --manifest "$manifest_path" \
    --role "$role")"
  [ -n "$result" ] || fail "manifest role is missing: $role"
  [[ "$result" != *$'\n'* ]] || fail "manifest role is duplicated: $role"
  printf '%s\n' "$result"
}

runtime_container_field() {
  local manifest_path="$1"
  local field="$2"

  python3 "$SCRIPT_DIR/backup_manifest.py" runtime-container \
    --manifest "$manifest_path" \
    --name "$PRODUCTION_CONTAINER" \
    --field "$field"
}

POSTGRES_IMAGE_ID=""
POSTGRES_CONFIGURED_IMAGE=""

if [ -n "$MANIFEST_ARTIFACT" ]; then
  MANIFEST_DATA_ROOT="$WORK_DIR/manifest-root"
  MANIFEST_FILE="$MANIFEST_DATA_ROOT/$MANIFEST_ARTIFACT"
  MANIFEST_SIDECAR="${MANIFEST_FILE}.sha256"
  fetch_artifact "$MANIFEST_ARTIFACT" "$MANIFEST_FILE"
  fetch_artifact "${MANIFEST_ARTIFACT}.sha256" "$MANIFEST_SIDECAR"
  (
    cd "$(dirname "$MANIFEST_FILE")"
    sha256sum -c "$(basename "$MANIFEST_SIDECAR")"
  ) >/dev/null || fail "manifest SHA-256 verification failed"

  POSTGRES_ARTIFACT="$(artifact_for_role postgres-areaforge "$MANIFEST_FILE")"
  CONFIGS_ARTIFACT="$(artifact_for_role configs "$MANIFEST_FILE")"
  UPLOADS_ARTIFACT="$(artifact_for_role volume-areaforge-uploads "$MANIFEST_FILE")"
  OPS_STATE_ARTIFACT="$(artifact_for_role volume-areaforge-ops-state "$MANIFEST_FILE")"
  validate_artifact "$POSTGRES_ARTIFACT" 'postgres/areaforge-postgres-*.sql.gz'
  validate_artifact "$CONFIGS_ARTIFACT" 'configs/configs-*.tar.gz'
  validate_artifact "$UPLOADS_ARTIFACT" 'volumes/areaforge-uploads-*.tar.gz'
  validate_artifact "$OPS_STATE_ARTIFACT" 'volumes/areaforge-ops-state-*.tar.gz'

  POSTGRES_FILE="$MANIFEST_DATA_ROOT/$POSTGRES_ARTIFACT"
  CONFIGS_FILE="$MANIFEST_DATA_ROOT/$CONFIGS_ARTIFACT"
  UPLOADS_FILE="$MANIFEST_DATA_ROOT/$UPLOADS_ARTIFACT"
  OPS_STATE_FILE="$MANIFEST_DATA_ROOT/$OPS_STATE_ARTIFACT"
  fetch_artifact "$POSTGRES_ARTIFACT" "$POSTGRES_FILE"
  fetch_artifact "$CONFIGS_ARTIFACT" "$CONFIGS_FILE"
  fetch_artifact "$UPLOADS_ARTIFACT" "$UPLOADS_FILE"
  fetch_artifact "$OPS_STATE_ARTIFACT" "$OPS_STATE_FILE"
  python3 "$SCRIPT_DIR/backup_manifest.py" verify \
    --backup-root "$MANIFEST_DATA_ROOT" \
    --manifest "$MANIFEST_FILE" \
    --role postgres-areaforge \
    --role configs \
    --role volume-areaforge-uploads \
    --role volume-areaforge-ops-state >/dev/null
  POSTGRES_IMAGE_ID="$(runtime_container_field "$MANIFEST_FILE" image_id)"
  POSTGRES_CONFIGURED_IMAGE="$(runtime_container_field "$MANIFEST_FILE" configured_image)"
else
  POSTGRES_FILE="$WORK_DIR/areaforge-postgres.sql.gz"
  CONFIGS_FILE="$WORK_DIR/configs.tar.gz"
  UPLOADS_FILE="$WORK_DIR/uploads.tar.gz"
  OPS_STATE_FILE="$WORK_DIR/ops-state.tar.gz"
  fetch_artifact "$POSTGRES_ARTIFACT" "$POSTGRES_FILE"
  fetch_artifact "$CONFIGS_ARTIFACT" "$CONFIGS_FILE"
  fetch_artifact "$UPLOADS_ARTIFACT" "$UPLOADS_FILE"
  fetch_artifact "$OPS_STATE_ARTIFACT" "$OPS_STATE_FILE"
  POSTGRES_CONFIGURED_IMAGE="$POSTGRES_IMAGE_OVERRIDE"
fi

echo "AreaForge isolated restore drill"
echo "source=$SOURCE drill_id=$DRILL_ID"
echo "manifest_artifact=${MANIFEST_ARTIFACT:-none}"
echo "postgres_artifact=$POSTGRES_ARTIFACT"
echo "configs_artifact=$CONFIGS_ARTIFACT"
echo "uploads_artifact=$UPLOADS_ARTIFACT"
echo "ops_state_artifact=$OPS_STATE_ARTIFACT"

gzip -t "$POSTGRES_FILE"
CONFIGS_MEMBERS="$(archive_field "$CONFIGS_FILE" member_count)"
CONFIGS_UNPACKED_BYTES="$(archive_field "$CONFIGS_FILE" unpacked_size_bytes)"
UPLOADS_MEMBERS="$(archive_field "$UPLOADS_FILE" member_count)"
UPLOADS_UNPACKED_BYTES="$(archive_field "$UPLOADS_FILE" unpacked_size_bytes)"
OPS_STATE_MEMBERS="$(archive_field "$OPS_STATE_FILE" member_count)"
OPS_STATE_UNPACKED_BYTES="$(archive_field "$OPS_STATE_FILE" unpacked_size_bytes)"
for value in "$CONFIGS_MEMBERS" "$CONFIGS_UNPACKED_BYTES" "$UPLOADS_MEMBERS" \
  "$UPLOADS_UNPACKED_BYTES" "$OPS_STATE_MEMBERS" "$OPS_STATE_UNPACKED_BYTES"; do
  require_uint archive_metadata "$value"
done
WORK_UNPACKED_BYTES="$(( CONFIGS_UNPACKED_BYTES + UPLOADS_UNPACKED_BYTES + OPS_STATE_UNPACKED_BYTES ))"
WORK_REQUIRED_BYTES="$(( WORK_UNPACKED_BYTES + MIN_FREE_BYTES ))"
WORK_AVAILABLE_KIB="$(df -Pk "$WORK_DIR" | awk 'NR == 2 { print $4 }')"
require_uint work_available_kib "$WORK_AVAILABLE_KIB"
WORK_AVAILABLE_BYTES="$(( WORK_AVAILABLE_KIB * 1024 ))"
[ "$WORK_AVAILABLE_BYTES" -ge "$WORK_REQUIRED_BYTES" ] || \
  fail "insufficient restore work storage: available=$WORK_AVAILABLE_BYTES required=$WORK_REQUIRED_BYTES"

extract_archive "$CONFIGS_FILE" "$WORK_DIR/configs" "$CONFIGS_MEMBERS" "$CONFIGS_UNPACKED_BYTES" \
  opt/areaforge/docker-compose.prod.yml \
  opt/areaforge/.env.production
extract_archive "$UPLOADS_FILE" "$WORK_DIR/uploads" "$UPLOADS_MEMBERS" "$UPLOADS_UNPACKED_BYTES"
extract_archive "$OPS_STATE_FILE" "$WORK_DIR/ops-state" "$OPS_STATE_MEMBERS" "$OPS_STATE_UNPACKED_BYTES"

[ -f "$WORK_DIR/configs/opt/areaforge/docker-compose.prod.yml" ] || fail "Compose config missing after extraction"
[ -f "$WORK_DIR/configs/opt/areaforge/.env.production" ] || fail "AreaForge environment file missing after extraction"

UPLOAD_FILE_COUNT="$(find "$WORK_DIR/uploads" -type f | wc -l | tr -d ' ')"
OPS_STATE_FILE_COUNT="$(find "$WORK_DIR/ops-state" -type f | wc -l | tr -d ' ')"
echo "uploads_files=$UPLOAD_FILE_COUNT ops_state_files=$OPS_STATE_FILE_COUNT"
[ "$UPLOAD_FILE_COUNT" -ge "$MIN_UPLOAD_FILES" ] || fail "uploads archive has fewer files than required"
[ "$OPS_STATE_FILE_COUNT" -ge "$MIN_OPS_STATE_FILES" ] || fail "ops-state archive has fewer files than required"

select_postgres_image() {
  local actual_id

  if [ -n "$POSTGRES_IMAGE_ID" ] && docker image inspect "$POSTGRES_IMAGE_ID" >/dev/null 2>&1; then
    printf '%s\n' "$POSTGRES_IMAGE_ID"
    return
  fi
  if [ -n "$POSTGRES_IMAGE_ID" ] && [ -n "$POSTGRES_CONFIGURED_IMAGE" ]; then
    actual_id="$(docker image inspect --format '{{.Id}}' "$POSTGRES_CONFIGURED_IMAGE" 2>/dev/null || true)"
    [ "$actual_id" = "$POSTGRES_IMAGE_ID" ] || \
      fail "recorded PostgreSQL image ID is unavailable and the configured tag points elsewhere"
    printf '%s\n' "$POSTGRES_CONFIGURED_IMAGE"
    return
  fi
  if [ -n "$POSTGRES_CONFIGURED_IMAGE" ] && docker image inspect "$POSTGRES_CONFIGURED_IMAGE" >/dev/null 2>&1; then
    printf '%s\n' "$POSTGRES_CONFIGURED_IMAGE"
    return
  fi
  fail "recorded PostgreSQL image is not available locally; pre-pull it before the drill"
}

query_production() {
  local sql="$1"

  docker exec "$PRODUCTION_CONTAINER" sh -c \
    'exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "$1"' sh "$sql"
}

count_nonempty_lines() {
  awk 'NF { count += 1 } END { print count + 0 }'
}

USER_SCHEMA_QUERY="SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname <> 'information_schema' ORDER BY 1"
USER_TABLE_QUERY="SELECT schemaname || '.' || tablename FROM pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema') ORDER BY 1"

POSTGRES_IMAGE="$(select_postgres_image)"
PRODUCTION_DATABASE=""
PRODUCTION_SCHEMA_LIST=""
PRODUCTION_TABLE_LIST=""
PRODUCTION_DB_SIZE=0
if [ "$COMPARE_PRODUCTION" -eq 1 ]; then
  docker inspect "$PRODUCTION_CONTAINER" >/dev/null 2>&1 || fail "production PostgreSQL container is unavailable"
  PRODUCTION_DATABASE="$(docker exec "$PRODUCTION_CONTAINER" sh -c 'printf %s "$POSTGRES_DB"')"
  [ -n "$PRODUCTION_DATABASE" ] || fail "production database name is unavailable"
  PRODUCTION_SCHEMA_LIST="$(query_production "$USER_SCHEMA_QUERY")"
  PRODUCTION_TABLE_LIST="$(query_production "$USER_TABLE_QUERY")"
  PRODUCTION_DB_SIZE="$(query_production 'SELECT pg_database_size(current_database())')"
  require_uint production_database_size "$PRODUCTION_DB_SIZE"
  [ "$PRODUCTION_DB_SIZE" -gt 0 ] || fail "production database size is invalid"
fi

SQL_UNCOMPRESSED_BYTES="$(gzip -dc "$POSTGRES_FILE" | wc -c | tr -d ' ')"
require_uint sql_uncompressed_bytes "$SQL_UNCOMPRESSED_BYTES"
[ "$SQL_UNCOMPRESSED_BYTES" -gt 0 ] || fail "PostgreSQL dump is empty"
CAPACITY_BASE_BYTES="$SQL_UNCOMPRESSED_BYTES"
if [ "$PRODUCTION_DB_SIZE" -gt "$CAPACITY_BASE_BYTES" ]; then
  CAPACITY_BASE_BYTES="$PRODUCTION_DB_SIZE"
fi
REQUIRED_FREE_BYTES="$(( CAPACITY_BASE_BYTES * SPACE_MULTIPLIER + MIN_FREE_BYTES ))"
DOCKER_ROOT="$(docker info --format '{{.DockerRootDir}}')"
[ -d "$DOCKER_ROOT" ] || fail "Docker root directory is unavailable: $DOCKER_ROOT"
AVAILABLE_FREE_KIB="$(df -Pk "$DOCKER_ROOT" | awk 'NR == 2 { print $4 }')"
require_uint docker_available_kib "$AVAILABLE_FREE_KIB"
AVAILABLE_FREE_BYTES="$(( AVAILABLE_FREE_KIB * 1024 ))"
[ "$AVAILABLE_FREE_BYTES" -ge "$REQUIRED_FREE_BYTES" ] || \
  fail "insufficient Docker storage: available=$AVAILABLE_FREE_BYTES required=$REQUIRED_FREE_BYTES"
echo "postgres_image=$POSTGRES_IMAGE sql_bytes=$SQL_UNCOMPRESSED_BYTES docker_free_bytes=$AVAILABLE_FREE_BYTES required_bytes=$REQUIRED_FREE_BYTES"

docker volume create --label ops.restore-drill=areaforge "$RESTORE_VOLUME" >/dev/null
docker run --detach \
  --name "$RESTORE_CONTAINER" \
  --network none \
  --mount "type=volume,source=$RESTORE_VOLUME,target=/var/lib/postgresql/data" \
  -e POSTGRES_PASSWORD=restore-drill-only \
  "$POSTGRES_IMAGE" >/dev/null

POSTGRES_READY=0
for _ in $(seq 1 90); do
  if docker logs "$RESTORE_CONTAINER" 2>&1 | grep -Fq 'PostgreSQL init process complete; ready for start up.' && \
     docker exec "$RESTORE_CONTAINER" pg_isready -U postgres >/dev/null 2>&1; then
    POSTGRES_READY=1
    break
  fi
  sleep 1
done
[ "$POSTGRES_READY" -eq 1 ] || fail "temporary Postgres did not reach final ready state"

gzip -dc "$POSTGRES_FILE" | docker exec -i "$RESTORE_CONTAINER" \
  psql -v ON_ERROR_STOP=1 -U postgres -d postgres >/dev/null

docker exec "$RESTORE_CONTAINER" psql -v ON_ERROR_STOP=1 -U postgres -d "$DATABASE_NAME" -Atc 'select 1' >/dev/null
RESTORED_SCHEMA_LIST="$(docker exec "$RESTORE_CONTAINER" psql -v ON_ERROR_STOP=1 -U postgres -d "$DATABASE_NAME" -Atc "$USER_SCHEMA_QUERY")"
RESTORED_TABLE_LIST="$(docker exec "$RESTORE_CONTAINER" psql -v ON_ERROR_STOP=1 -U postgres -d "$DATABASE_NAME" -Atc "$USER_TABLE_QUERY")"
RESTORED_SCHEMAS="$(printf '%s\n' "$RESTORED_SCHEMA_LIST" | count_nonempty_lines)"
RESTORED_TABLES="$(printf '%s\n' "$RESTORED_TABLE_LIST" | count_nonempty_lines)"
RESTORED_DB_SIZE="$(docker exec "$RESTORE_CONTAINER" psql -v ON_ERROR_STOP=1 -U postgres -d "$DATABASE_NAME" -Atc 'SELECT pg_database_size(current_database())')"
require_uint restored_database_size "$RESTORED_DB_SIZE"

[ "$RESTORED_SCHEMAS" -gt 0 ] || fail "restored database has no visible schemas"
[ "$RESTORED_TABLES" -gt 0 ] || fail "restored database has no user tables"
[ "$RESTORED_DB_SIZE" -gt 0 ] || fail "restored database size is invalid"

if [ "$COMPARE_PRODUCTION" -eq 1 ]; then
  [ "$DATABASE_NAME" = "$PRODUCTION_DATABASE" ] || fail "restored database name does not match production"
  [ "$RESTORED_SCHEMA_LIST" = "$PRODUCTION_SCHEMA_LIST" ] || fail "restored user schema names do not match production"
  [ "$RESTORED_TABLE_LIST" = "$PRODUCTION_TABLE_LIST" ] || fail "restored user table names do not match production"
  [ "$(( RESTORED_DB_SIZE * 100 ))" -ge "$(( PRODUCTION_DB_SIZE * MIN_DB_SIZE_RATIO_PERCENT ))" ] || \
    fail "restored database is unexpectedly small compared with production"
fi

DURATION_SECONDS="$(( $(date +%s) - STARTED_AT ))"
echo "restored_database=$DATABASE_NAME schemas=$RESTORED_SCHEMAS user_tables=$RESTORED_TABLES size_bytes=$RESTORED_DB_SIZE"
if [ "$COMPARE_PRODUCTION" -eq 1 ]; then
  echo "production_database=$PRODUCTION_DATABASE size_bytes=$PRODUCTION_DB_SIZE"
fi
echo "duration_seconds=$DURATION_SECONDS"

if [ "$COMPARE_PRODUCTION" -eq 0 ]; then
  echo "offline import check completed; production comparison was skipped and success metrics were not published"
  exit 0
fi

install -d -m 0755 "$(dirname "$METRIC_OUT")"
METRIC_TMP="${METRIC_OUT}.tmp"
{
  echo '# HELP areaforge_restore_drill_last_success_timestamp Unix timestamp of the latest successful isolated restore drill.'
  echo '# TYPE areaforge_restore_drill_last_success_timestamp gauge'
  printf 'areaforge_restore_drill_last_success_timestamp{source="%s"} %s\n' "$SOURCE" "$(date +%s)"
  echo '# HELP areaforge_restore_drill_duration_seconds Duration of the latest successful isolated restore drill.'
  echo '# TYPE areaforge_restore_drill_duration_seconds gauge'
  printf 'areaforge_restore_drill_duration_seconds{source="%s"} %s\n' "$SOURCE" "$DURATION_SECONDS"
  echo '# HELP areaforge_restore_drill_user_tables User table count in the latest successful isolated restore drill.'
  echo '# TYPE areaforge_restore_drill_user_tables gauge'
  printf 'areaforge_restore_drill_user_tables{source="%s"} %s\n' "$SOURCE" "$RESTORED_TABLES"
  echo '# HELP areaforge_restore_drill_database_size_bytes Restored database size in bytes in the latest successful isolated restore drill.'
  echo '# TYPE areaforge_restore_drill_database_size_bytes gauge'
  printf 'areaforge_restore_drill_database_size_bytes{source="%s"} %s\n' "$SOURCE" "$RESTORED_DB_SIZE"
} > "$METRIC_TMP"
mv "$METRIC_TMP" "$METRIC_OUT"
chmod 0644 "$METRIC_OUT"

echo "AreaForge isolated restore drill completed successfully"
