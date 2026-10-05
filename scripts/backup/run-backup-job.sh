#!/bin/bash -p
set -Eeuo pipefail

umask 077
SCRIPT_DIR="$(cd -- "${BASH_SOURCE[0]%/*}" && pwd -P)"
# 限定分支先于共享环境、目录和作业枚举。
if [[ "${1:-}" == sub2api-set ]]; then
  for variable in $(compgen -e); do
    case "$variable" in PATH|LANG|LC_ALL|TZ|HOME|TMPDIR|SHLVL|_|PWD) ;;
      *) printf 'ERROR: environment_override\n' >&2; exit 2 ;;
    esac
  done
  shift
  scoped_python="$SCRIPT_DIR/../../tools/python3"
  [[ -x "$scoped_python" && ! -L "$scoped_python" ]] || { printf 'ERROR: controlled_interpreter_required\n' >&2; exit 2; }
  "$scoped_python" -I -B "$SCRIPT_DIR/sub2api_backup_contract.py" scoped "$@" || exit 2
  exec "$scoped_python" -I -B "$SCRIPT_DIR/sub2api_backup.py" "$@"
fi
JOB_SCRIPT_DIR="${BACKUP_JOB_SCRIPT_DIR:-$SCRIPT_DIR}"
METRIC_DIR="${BACKUP_JOB_METRIC_DIR:-/var/lib/node_exporter/textfile_collector}"
LOCK_DIR="${BACKUP_JOB_LOCK_DIR:-/run/lock}"

job="${1:-}"
shift || true
case "$job" in
  postgres) timeout_seconds=2700 ;;
  redis) timeout_seconds=600 ;;
  configs | volumes) timeout_seconds=3600 ;;
  *) printf 'unsupported backup job: %s\n' "$job" >&2; exit 2 ;;
esac
[[ "$#" -eq 0 ]] || { printf 'unexpected backup arguments\n' >&2; exit 2; }
control_digest="$(python3 -B "$SCRIPT_DIR/sub2api_backup_contract.py" control)" || exit 2
timeout_seconds="${BACKUP_JOB_TIMEOUT_SECONDS:-$timeout_seconds}"
[[ "$timeout_seconds" =~ ^[1-9][0-9]*$ ]] || {
  printf 'invalid backup timeout: %s\n' "$timeout_seconds" >&2
  exit 2
}

script="$JOB_SCRIPT_DIR/backup-${job}.sh"
[ -x "$script" ] || { printf 'backup script is not executable: %s\n' "$script" >&2; exit 2; }
install -d -m 0755 "$LOCK_DIR" "$METRIC_DIR"
exec 9>"$LOCK_DIR/ops-backup-${job}.lock"
chmod 0600 "$LOCK_DIR/ops-backup-${job}.lock"
flock -n 9 || { printf 'backup job is already running: %s\n' "$job" >&2; exit 75; }

python3 -B "$SCRIPT_DIR/sub2api_backup_contract.py" shared "$job" "$LOCK_DIR/ops-backup-${job}.lock" "$control_digest" 9 || exit 75
internal=()
[[ "$job" == configs ]] || internal=(--backup-internal-v1 "$job" "$LOCK_DIR/ops-backup-${job}.lock" "$control_digest")
started_at="$(date +%s)"
set +e
OPS_BACKUP_JOB_WRAPPED=1 timeout --signal=TERM --kill-after=120s "${timeout_seconds}s" \
  nice -n 10 "$script" "${internal[@]}"
status=$?
set -e
finished_at="$(date +%s)"
duration=$((finished_at - started_at))
result=0
[ "$status" -eq 0 ] && result=1

tmp_metric="$(mktemp "$METRIC_DIR/.backup-job-${job}.XXXXXX")"
trap 'rm -f -- "${tmp_metric:-}"' EXIT
{
  printf '# HELP backup_job_last_attempt_timestamp Unix timestamp of the latest backup job attempt.\n'
  printf '# TYPE backup_job_last_attempt_timestamp gauge\n'
  printf 'backup_job_last_attempt_timestamp{backup_job="%s"} %s\n' "$job" "$started_at"
  printf '# HELP backup_job_last_result Whether the latest backup job attempt succeeded.\n'
  printf '# TYPE backup_job_last_result gauge\n'
  printf 'backup_job_last_result{backup_job="%s"} %s\n' "$job" "$result"
  printf '# HELP backup_job_last_duration_seconds Duration of the latest backup job attempt.\n'
  printf '# TYPE backup_job_last_duration_seconds gauge\n'
  printf 'backup_job_last_duration_seconds{backup_job="%s"} %s\n' "$job" "$duration"
} >"$tmp_metric"
chmod 0644 "$tmp_metric"
mv -f -- "$tmp_metric" "$METRIC_DIR/backup-job-${job}.prom"
trap - EXIT
exit "$status"
