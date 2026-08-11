#!/usr/bin/env bash
set -Eeuo pipefail

mode="${1:-source}"
case "$mode" in
  source | installed | runtime) ;;
  *) printf 'usage: %s [source|installed|runtime]\n' "$0" >&2; exit 2 ;;
esac

REPO_ROOT="${OPS_PREFLIGHT_REPO_ROOT:-/opt/ops}"
SOURCE_DIR="$REPO_ROOT/services/areasong-ops"
RUNTIME_DIR="${OPS_PREFLIGHT_RUNTIME_DIR:-/opt/services/areasong-ops}"
CONFIG_DIR="${OPS_PREFLIGHT_CONFIG_DIR:-/etc/areasong-ops}"
RUNNER_ROOT="${OPS_PREFLIGHT_RUNNER_ROOT:-/usr/local/libexec/areasong-ops}"
UNIT_PATH="${OPS_PREFLIGHT_UNIT_PATH:-/etc/systemd/system/areasong-ops-runner.service}"
SOCKET_PATH="${OPS_PREFLIGHT_SOCKET_PATH:-/var/lib/areasong-ops/run/runner.sock}"
CONTAINER_NAME="${OPS_PREFLIGHT_CONTAINER_NAME:-areasong-ops-web}"

fail() { printf 'preflight failed: %s\n' "$*" >&2; exit 1; }
require_command() { command -v "$1" >/dev/null || fail "missing command: $1"; }

require_regular_file() {
  local path="$1"
  if [ ! -f "$path" ] || [ -L "$path" ]; then
    fail "unsafe or missing regular file: $path"
  fi
}

require_owner_mode() {
  local path="$1" expected_owner="$2" expected_group="$3" expected_mode="$4"
  require_regular_file "$path"
  [ "$(stat -c '%U:%G' "$path")" = "$expected_owner:$expected_group" ] ||
    fail "unexpected owner for $path"
  [ "$(stat -c '%a' "$path")" = "$expected_mode" ] || fail "unexpected mode for $path"
}

read_env_value() {
  local name="$1" path="$2"
  awk -F= -v key="$name" '$1 == key {sub(/^[^=]*=/, ""); print; exit}' "$path"
}

for command_name in git jq docker; do require_command "$command_name"; done
require_regular_file "$SOURCE_DIR/Dockerfile"
require_regular_file "$SOURCE_DIR/config/services.example.json"
jq -e '.schemaVersion == 4 and (.adapters | length > 0) and (.services | length > 0) and
  (.automaticTasks | type == "object")' \
  "$SOURCE_DIR/config/services.example.json" >/dev/null || fail "source service catalog is invalid"

revision="$(git -C "$REPO_ROOT" rev-parse HEAD)"
[[ "$revision" =~ ^[a-f0-9]{40}$ ]] || fail "source revision is not a full Git commit"
printf 'source revision: %s\n' "$revision"

if [ "$mode" = source ]; then
  exit 0
fi

[ "$(uname -s)" = Linux ] || fail "installed checks require Linux"
for command_name in getent stat systemd-analyze systemctl curl; do require_command "$command_name"; done
[ "$(id -u)" -eq 0 ] || fail "installed checks require root"

group_record="$(getent group areasong-ops)" || fail "areasong-ops group is missing"
group_gid="$(cut -d: -f3 <<<"$group_record")"
[[ "$group_gid" =~ ^[0-9]+$ ]] || fail "areasong-ops group GID is invalid"

require_owner_mode "$CONFIG_DIR/services.json" root root 600
require_owner_mode "$CONFIG_DIR/web.env" root root 600
grafana_url="$(read_env_value OPS_GRAFANA_URL "$CONFIG_DIR/web.env")"
[[ "$grafana_url" =~ ^https://[^/?#]+/?$ ]] || fail "Grafana URL must be an HTTPS origin"
require_owner_mode "$RUNNER_ROOT/areasong-ops-runner" root root 755
require_owner_mode "$UNIT_PATH" root root 644
for adapter in "$RUNNER_ROOT"/adapters/*.sh; do
  require_owner_mode "$adapter" root root 755
done

require_regular_file "$RUNTIME_DIR/.env"
require_regular_file "$RUNTIME_DIR/compose.yml"
configured_revision="$(read_env_value OPS_BUILD_REVISION "$RUNTIME_DIR/.env")"
configured_gid="$(read_env_value OPS_RUNNER_GID "$RUNTIME_DIR/.env")"
[ "$configured_revision" = "$revision" ] || fail "runtime revision differs from source revision"
[ "$configured_gid" = "$group_gid" ] || fail "runtime Runner GID differs from the system group"

systemd-analyze verify "$UNIT_PATH"
docker compose --project-directory "$RUNTIME_DIR" --env-file "$RUNTIME_DIR/.env" \
  -f "$RUNTIME_DIR/compose.yml" config --quiet

if [ "$mode" = installed ]; then
  exit 0
fi

systemctl is-active --quiet areasong-ops-runner.service || fail "Runner service is not active"
[ "$(stat -c '%a %U:%G' /var/lib/areasong-ops)" = "710 root:areasong-ops" ] ||
  fail "Runner state root owner or mode is invalid"
[ "$(stat -c '%a %U:%G' /var/lib/areasong-ops/run)" = "750 root:areasong-ops" ] ||
  fail "Runner socket directory owner or mode is invalid"
[ -S "$SOCKET_PATH" ] || fail "Runner socket is missing"
[ "$(stat -c '%a %U:%G' "$SOCKET_PATH")" = "660 root:areasong-ops" ] ||
  fail "Runner socket owner or mode is invalid"
curl -fsS --unix-socket "$SOCKET_PATH" http://runner/healthz >/dev/null || fail "Runner health failed"
curl -fsS http://127.0.0.1:9093/-/ready >/dev/null || fail "Alertmanager readiness failed"

docker inspect "$CONTAINER_NAME" | jq -e --arg revision "$revision" '
  length == 1 and
  .[0].State.Running == true and
  .[0].HostConfig.ReadonlyRootfs == true and
  .[0].Config.User == "65532:65532" and
  .[0].Config.Labels["org.opencontainers.image.revision"] == $revision and
  any(.[0].Mounts[]; .Source == "/var/lib/areasong-ops/run" and .Destination == "/run/areasong-ops" and .RW == false) and
  ([.[0].Mounts[].Destination] | index("/var/run/docker.sock") | not)
' >/dev/null || fail "Web runtime identity or isolation check failed"

curl -fsS http://127.0.0.1:3200/healthz >/dev/null || fail "Web health failed"
metrics="$(curl -fsS http://127.0.0.1:3200/metrics)"
grep -Fq "component=\"web\",version=" <<<"$metrics" || fail "Web build metric is missing"
grep -Fq "component=\"runner\",version=" <<<"$metrics" || fail "Runner build metric is missing"
[ "$(grep -Fc "revision=\"$revision\"" <<<"$metrics")" -eq 2 ] ||
  fail "Web and Runner revisions do not match the deployed commit"

printf 'runtime preflight: PASS\n'
