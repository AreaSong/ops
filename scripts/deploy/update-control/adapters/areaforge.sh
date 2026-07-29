#!/usr/bin/env bash
set -Eeuo pipefail

umask 077
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CATALOG="${AREAFORGE_UPDATE_CONTROL_RELEASES:-$SCRIPT_DIR/../releases/areaforge.json}"
UPDATER="${AREAFORGE_UPDATE_CONTROL_UPDATER:-/opt/areaforge/ops/github-release-updater/areaforge-updater.sh}"
SMOKE="${AREAFORGE_UPDATE_CONTROL_SMOKE:-/opt/areaforge/ops/update-agent/areaforge-release-readonly-smoke.sh}"
CONTROLLED_COMPOSE="${AREAFORGE_UPDATE_CONTROL_CONTROLLED_COMPOSE:-/opt/ops/services/areaforge/compose.yml}"
RUNTIME_COMPOSE="${AREAFORGE_UPDATE_CONTROL_RUNTIME_COMPOSE:-/opt/areaforge/docker-compose.prod.yml}"
ENV_FILE="${AREAFORGE_UPDATE_CONTROL_ENV_FILE:-/opt/areaforge/.env.production}"
BACKUP_POSTGRES="${AREAFORGE_UPDATE_CONTROL_BACKUP_POSTGRES:-/opt/ops/scripts/backup/backup-postgres.sh}"
BACKUP_VOLUMES="${AREAFORGE_UPDATE_CONTROL_BACKUP_VOLUMES:-/opt/ops/scripts/backup/backup-volumes.sh}"
CONTAINER="areaforge-web"
BASE_URL="https://forge.areasong.top"

fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
result() { jq -cn --arg phase "$1" --arg detail "$2" '{ok:true,phase:$phase,detail:$detail}'; }
sha256_text() { printf '%s' "$1" | sha256sum | awk '{print "sha256:"$1}'; }

phase="${1:-}"
request="${2:-}"
operation_dir="${3:-}"
[[ "$phase" =~ ^(preflight|backup|migration|apply|health|smoke|identity|rollback)$ ]] || fail "unsupported phase"
[[ -f "$request" && ! -L "$request" ]] || fail "request must be a regular file"
[[ -d "$operation_dir" && ! -L "$operation_dir" ]] || fail "operation directory is unsafe"
target_id="$(jq -er '.targetId' "$request")"
jq -e --arg target "$target_id" '.schemaVersion == 1 and (.targets[$target] | type == "object")' "$CATALOG" >/dev/null ||
  fail "target is not present in the AreaForge release catalog"
tag="$(jq -er --arg target "$target_id" '.targets[$target].tag' "$CATALOG")"
target_image="$(jq -er --arg target "$target_id" '.targets[$target].target.webImageDigest' "$CATALOG")"
target_image_id="$(jq -er --arg target "$target_id" '.targets[$target].dockerImageId' "$CATALOG")"
target_runtime_hash="$(jq -er --arg target "$target_id" '.targets[$target].runtimeIdentityHash' "$CATALOG")"
target_git_commit="$(jq -er --arg target "$target_id" '.targets[$target].gitCommit' "$CATALOG")"

observed_identity() {
  local health current_version current_image current_image_id runtime_hash
  health="$(curl -fsS "$BASE_URL/api/health")"
  current_version="$(jq -er '.version' <<<"$health")"
  current_image="$(docker inspect --format '{{.Config.Image}}' "$CONTAINER")"
  current_image_id="$(docker inspect --format '{{.Image}}' "$CONTAINER")"
  runtime_hash="$(jq -er '.runtimeIdentity.identityHash' <<<"$health")"
  jq -cnS --arg currentVersion "$current_version" --arg currentImage "$current_image" \
    --arg currentImageId "$current_image_id" --arg runtimeIdentityHash "$runtime_hash" \
    '{currentVersion:$currentVersion,currentImage:$currentImage,currentImageId:$currentImageId,runtimeIdentityHash:$runtimeIdentityHash}'
}

assert_expected_before() {
  local observed expected
  observed="$(observed_identity)"
  expected="$(jq -cS '.expectedBefore | {currentVersion,currentImage,currentImageId,runtimeIdentityHash}' "$request")"
  [[ "$observed" == "$expected" ]] || fail "EXPECTED_BEFORE_MISMATCH observed=$observed"
}

write_guard() {
  local expected params target expected_projection expected_hash semantic_projection semantic_hash request_projection request_hash
  expected="$(jq -cS '.expectedBefore | {autoApply,currentImage,currentVersion,rollbackAvailable,rollbackSourceRecordSha256,rollbackTargetImage,rollbackTargetVersion,signatureRequired}' "$request")"
  params="$(jq -cS --arg target "$target_id" '.targets[$target].params' "$CATALOG")"
  target="$(jq -cS --arg target "$target_id" '.targets[$target].target' "$CATALOG")"
  expected_projection="$(jq -cnS --argjson expectedBefore "$expected" '{domain:"areaforge.update-request.expected-before.v2",expectedBefore:$expectedBefore}')"
  expected_hash="$(sha256_text "$expected_projection")"
  semantic_projection="$(jq -cnS --argjson params "$params" --argjson target "$target" --argjson expectedBefore "$expected" '{domain:"areaforge.update-request.semantic.v2",action:"apply",params:$params,target:$target,expectedBefore:$expectedBefore}')"
  semantic_hash="$(sha256_text "$semantic_projection")"
  request_projection="$(jq -cS --argjson params "$params" --argjson target "$target" --argjson expectedBefore "$expected" --arg expectedBeforeHash "$expected_hash" --arg semanticHash "$semantic_hash" '{domain:"areaforge.update-request.v2",schemaVersion:2,id,idempotencyKey,action,status,requestedAt,expiresAt,actorEmailHash,params:$params,target:$target,expectedBefore:$expectedBefore,expectedBeforeHash:$expectedBeforeHash,semanticHash:$semanticHash}' "$request")"
  request_hash="$(sha256_text "$request_projection")"
  jq -cS --argjson params "$params" --argjson target "$target" --argjson expectedBefore "$expected" \
    --arg expectedBeforeHash "$expected_hash" --arg semanticHash "$semantic_hash" --arg requestHash "$request_hash" \
    '{schemaVersion:2,id,idempotencyKey,action,status,requestedAt,expiresAt,actorEmailHash,params:$params,target:$target,expectedBefore:$expectedBefore,expectedBeforeHash:$expectedBeforeHash,semanticHash:$semanticHash,requestHash:$requestHash}' \
    "$request" >"$operation_dir/areaforge-request-v2.json"
  chmod 0600 "$operation_dir/areaforge-request-v2.json"
}

case "$phase" in
  preflight)
    for command_name in curl docker jq sha256sum awk; do command -v "$command_name" >/dev/null || fail "missing command: $command_name"; done
    for path in "$CATALOG" "$UPDATER" "$SMOKE" "$CONTROLLED_COMPOSE" "$RUNTIME_COMPOSE" "$ENV_FILE"; do
      [[ -e "$path" ]] || fail "required path is missing: $path"
    done
    assert_expected_before
    cp -- "$CONTROLLED_COMPOSE" "$operation_dir/controlled-compose.before.yml"
    cp -- "$RUNTIME_COMPOSE" "$operation_dir/runtime-compose.before.yml"
    write_guard
    result "$phase" "expected-before and strict V2 request guard verified"
    ;;
  backup)
    "$BACKUP_POSTGRES" >/dev/null
    "$BACKUP_VOLUMES" >/dev/null
    result "$phase" "fresh PostgreSQL and volume backup jobs completed"
    ;;
  migration)
    result "$phase" "migration is manifest-driven and executed inside the transactional updater"
    ;;
  apply)
    assert_expected_before
    "$UPDATER" apply --tag "$tag" --yes --request-guard "$operation_dir/areaforge-request-v2.json"
    result "$phase" "transactional updater completed"
    ;;
  health)
    curl -fsS "$BASE_URL/api/health" | jq -e '.ok == true' >/dev/null
    result "$phase" "public health passed"
    ;;
  smoke)
    "$SMOKE" >/dev/null
    result "$phase" "authenticated read-only smoke passed"
    ;;
  identity)
    [[ "$(docker inspect --format '{{.Config.Image}}' "$CONTAINER")" == "$target_image" ]] || fail "target Docker image mismatch"
    [[ "$(docker inspect --format '{{.Image}}' "$CONTAINER")" == "$target_image_id" ]] || fail "target Docker Image ID mismatch"
    curl -fsS "$BASE_URL/api/health" | jq -e --arg version "${tag#v}" --arg identityHash "$target_runtime_hash" --arg gitCommit "$target_git_commit" \
      '.version == $version and .runtimeIdentity.status == "verified" and .runtimeIdentity.identityHash == $identityHash and .runtimeIdentity.gitCommit == $gitCommit' >/dev/null
    result "$phase" "desired image, Docker identity and runtime identity agree"
    ;;
  rollback)
    old_image="$(jq -er '.expectedBefore.currentImage' "$request")"
    install -m 0644 "$operation_dir/controlled-compose.before.yml" "$CONTROLLED_COMPOSE"
    install -m 0644 "$operation_dir/runtime-compose.before.yml" "$RUNTIME_COMPOSE"
    AREAFORGE_IMAGE="$old_image" docker compose --env-file "$ENV_FILE" -f "$RUNTIME_COMPOSE" up -d --no-deps --force-recreate web
    [[ "$(docker inspect --format '{{.Config.Image}}' "$CONTAINER")" == "$old_image" ]] || fail "rollback image mismatch"
    curl -fsS "$BASE_URL/api/health" | jq -e '.ok == true' >/dev/null
    "$SMOKE" >/dev/null
    if [[ "$(jq -r --arg target "$target_id" '.targets[$target].requiresMigration' "$CATALOG")" == "true" ]]; then
      fail "MIGRATION_STATE_UNCERTAIN: application identity restored but database migration is not reversed"
    fi
    result "$phase" "pre-operation Compose and actual image identity restored"
    ;;
esac
