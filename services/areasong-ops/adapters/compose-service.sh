#!/usr/bin/env bash
set -Eeuo pipefail

umask 077

action="${1:-}"
phase="${2:-}"
operation_dir="${3:-}"
target="${4:-}"
source_dir="${5:-}"
service="${OPS_SERVICE_NAME:-}"
catalog="${OPS_SERVICE_CATALOG:-/etc/areasong-ops/services.json}"

fail() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

result() {
  local summary="$1"
  local data="${2:-\{\}}"
  jq -cn --arg action "$action" --arg phase "$phase" --arg summary "$summary" --argjson data "$data" \
    '{schemaVersion:2,action:$action,phase:$phase,ok:true,summary:$summary,data:$data}'
}

[[ "$service" =~ ^[a-z][a-z0-9-]{1,39}$ ]] || fail "service name is invalid"
[[ -d "$operation_dir" && ! -L "$operation_dir" ]] || fail "operation directory is unsafe"
[[ -f "$catalog" && ! -L "$catalog" ]] || fail "service catalog is unsafe"

spec="$(jq -cer --arg service "$service" '.services[$service] | select(.template == "compose-service-v1") | .runtime' "$catalog")"
value() { jq -er "$1" <<<"$spec"; }

controlled_compose="$(value '.controlledCompose')"
runtime_compose="$(value '.runtimeCompose')"
env_file="$(value '.envFile')"
app_service="$(value '.applicationService')"
app_container="$(value '.applicationContainer')"
health_url="$(value '.healthUrl')"
release_repository="$(value '.releaseRepository')"
release_catalog="$(value '.releaseCatalog')"
prepared_release_dir="$(value '.preparedReleaseDir')"
inspect_executable="$(value '.inspectExecutable')"
restore_executable="$(jq -r '.restoreDrillExecutable // ""' <<<"$spec")"
prepare_executable="$(jq -r '.prepareExecutable // ""' <<<"$spec")"
update_executable="$(jq -r '.updateExecutable // ""' <<<"$spec")"
backup_evidence_executable="$(jq -r '.backupEvidenceExecutable // ""' <<<"$spec")"

require_regular_file() {
  [[ -f "$1" && ! -L "$1" ]] || fail "required file is missing or unsafe: $1"
}

require_executable() {
  require_regular_file "$1"
  [[ -x "$1" ]] || fail "required executable is not executable: $1"
}

delegate() {
  local executable="$1"
  shift
  require_executable "$executable"
  "$executable" "$@"
}

dependency_snapshot() {
  local names=()
  mapfile -t names < <(jq -r '.dependencyContainers[]?' <<<"$spec")
  if [[ "${#names[@]}" -eq 0 ]]; then
    printf '[]\n'
    return
  fi
  docker inspect "${names[@]}" | jq -cS '[.[] | {name:(.Name|ltrimstr("/")),id:.Id,startedAt:.State.StartedAt,image:.Image}] | sort_by(.name)'
}

assert_dependencies_unchanged() {
  local baseline="$operation_dir/dependencies.before.json"
  [[ -f "$baseline" && ! -L "$baseline" ]] || fail "dependency baseline is missing"
  dependency_snapshot >"$operation_dir/dependencies.after.json"
  cmp -s "$baseline" "$operation_dir/dependencies.after.json" || fail "dependency container identity changed"
}

wait_health() {
  local temporary code
  temporary="$(mktemp "$operation_dir/.health.XXXXXX")"
  for _ in $(seq 1 60); do
    code="$(curl -sS -o "$temporary" -w '%{http_code}' "$health_url" || true)"
    if [[ "$code" == 200 ]]; then
      rm -f "$temporary"
      return
    fi
    if ! docker inspect --format '{{.State.Running}}' "$app_container" 2>/dev/null | grep -Fxq true; then
      rm -f "$temporary"
      fail "application container exited before health passed"
    fi
    sleep 2
  done
  rm -f "$temporary"
  fail "application health endpoint did not return HTTP 200"
}

case "$action:$phase" in
  inspect:inspect)
    delegate "$inspect_executable" "$action" "$phase" "$operation_dir" "$target" "$source_dir"
    ;;
  check:discover)
    require_regular_file "$release_catalog"
    release_file="$(mktemp "$operation_dir/.release.XXXXXX")"
    curl -fsS -H 'Accept: application/vnd.github+json' -H 'X-GitHub-Api-Version: 2022-11-28' \
      "https://api.github.com/repos/${release_repository}/releases/latest" -o "$release_file"
    latest="$(jq -er '.tag_name | select(test("^v[0-9]+\\.[0-9]+\\.[0-9]+([+-][0-9A-Za-z.-]+)?$"))' "$release_file")"
    published="$(jq -er '.published_at' "$release_file")"
    current_json="$(delegate "$inspect_executable" inspect inspect "$operation_dir" "" "")"
    current="$(jq -er '.data.currentVersion' <<<"$current_json")"
    prepared=false
    blockers='["目标尚未完成隔离恢复、迁移和旧镜像兼容演练"]'
    prepared_record="$prepared_release_dir/${latest}.json"
    if jq -e --arg target "$latest" '.targets[$target].status == "prepared"' "$release_catalog" >/dev/null 2>&1 ||
      { [[ -f "$prepared_record" && ! -L "$prepared_record" ]] && jq -e --arg target "$latest" '.tag == $target and .status == "prepared"' "$prepared_record" >/dev/null 2>&1; }; then
      prepared=true
      blockers='[]'
    fi
    rm -f "$release_file"
    result "${service} 最新发布检查完成" "$(jq -cn --arg currentVersion "$current" --arg latestTag "$latest" \
      --arg publishedAt "$published" --argjson prepared "$prepared" --argjson blockers "$blockers" \
      '{currentVersion:$currentVersion,latestTag:$latestTag,publishedAt:$publishedAt,prepared:$prepared,
        blockers:$blockers,updateAvailable:($latestTag != ("v"+$currentVersion))}')"
    ;;
  backup:preflight)
    if [[ -n "$backup_evidence_executable" ]]; then
      delegate "$backup_evidence_executable" "$action" "$phase" "$operation_dir" "$target" "$source_dir"
      exit 0
    fi
    require_regular_file "$controlled_compose"
    require_regular_file "$runtime_compose"
    require_regular_file "$env_file"
    cmp -s "$controlled_compose" "$runtime_compose" || fail "controlled and runtime Compose files differ"
    result "备份前 Compose 双副本一致"
    ;;
  backup:backup)
    if [[ -n "$backup_evidence_executable" ]]; then
      delegate "$backup_evidence_executable" "$action" "$phase" "$operation_dir" "$target" "$source_dir"
      exit 0
    fi
    mapfile -t backup_executables < <(jq -r '.backupExecutables[]?' <<<"$spec")
    [[ "${#backup_executables[@]}" -gt 0 ]] || fail "backup executables are not configured"
    for executable in "${backup_executables[@]}"; do
      require_executable "$executable"
      "$executable" >/dev/null
    done
    result "受控备份作业全部完成"
    ;;
  backup:verify)
    if [[ -n "$backup_evidence_executable" ]]; then
      delegate "$backup_evidence_executable" "$action" "$phase" "$operation_dir" "$target" "$source_dir"
      exit 0
    fi
    delegate "$inspect_executable" inspect inspect "$operation_dir" "" "" >/dev/null
    result "备份后服务运行身份检查通过"
    ;;
  restart:preflight)
    require_regular_file "$controlled_compose"
    require_regular_file "$runtime_compose"
    require_regular_file "$env_file"
    cmp -s "$controlled_compose" "$runtime_compose" || fail "controlled and runtime Compose files differ"
    dependency_snapshot >"$operation_dir/dependencies.before.json"
    result "Compose 与依赖容器基线已记录"
    ;;
  restart:restart)
    docker compose --env-file "$env_file" -f "$runtime_compose" config --quiet
    docker compose --env-file "$env_file" -f "$runtime_compose" up -d --no-deps --force-recreate "$app_service"
    assert_dependencies_unchanged
    result "仅重建应用服务 ${app_service}"
    ;;
  restart:health|restart:smoke)
    wait_health
    assert_dependencies_unchanged
    result "应用健康检查与依赖身份检查通过"
    ;;
  restore-drill:*)
    [[ -n "$restore_executable" ]] || fail "restore drill executable is not configured"
    delegate "$restore_executable" "$action" "$phase" "$operation_dir" "$target" "$source_dir"
    ;;
  prepare:*)
    [[ -n "$prepare_executable" ]] || fail "prepare executable is not configured"
    delegate "$prepare_executable" "$action" "$phase" "$operation_dir" "$target" "$source_dir"
    ;;
  update:*|rollback:*)
    [[ -n "$update_executable" ]] || fail "update executable is not configured"
    delegate "$update_executable" "$action" "$phase" "$operation_dir" "$target" "$source_dir"
    ;;
  *)
    fail "unsupported action phase"
    ;;
esac
