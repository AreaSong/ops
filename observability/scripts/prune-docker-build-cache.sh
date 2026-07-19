#!/usr/bin/env bash
set -Eeuo pipefail

readonly DOCKER_BIN=/usr/bin/docker
readonly RETENTION=336h

if [[ ! -x "$DOCKER_BIN" ]]; then
  echo "docker CLI is unavailable: $DOCKER_BIN" >&2
  exit 1
fi

exec "$DOCKER_BIN" builder prune --force --filter "until=${RETENTION}"
