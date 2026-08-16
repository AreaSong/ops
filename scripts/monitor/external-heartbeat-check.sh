#!/usr/bin/env bash
set -euo pipefail

readonly MARKER='<!-- areasong-external-heartbeat:v1 -->'
readonly MAX_AGE_SECONDS="${HEARTBEAT_MAX_AGE_SECONDS:-600}"
readonly REPOSITORY="${GITHUB_REPOSITORY:-AreaSong/ops}"
WORK_DIR="$(mktemp -d)"
ISSUES_JSON="$WORK_DIR/issues.json"
trap 'rm -rf "$WORK_DIR"' EXIT

if ! [[ "$MAX_AGE_SECONDS" =~ ^[0-9]+$ ]] || [ "$MAX_AGE_SECONDS" -lt 60 ]; then
  echo "HEARTBEAT_FAIL reason=invalid_max_age" >&2
  exit 2
fi
if ! [[ "$REPOSITORY" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
  echo "HEARTBEAT_FAIL reason=invalid_repository" >&2
  exit 2
fi
command -v gh >/dev/null 2>&1 || {
  echo "HEARTBEAT_FAIL reason=gh_missing" >&2
  exit 2
}

if ! gh api \
  --method GET \
  "repos/${REPOSITORY}/issues?state=all&sort=updated&direction=desc&per_page=100" \
  >"$ISSUES_JSON"; then
  echo "HEARTBEAT_FAIL reason=github_api" >&2
  exit 1
fi

match_count="$(jq --arg marker "$MARKER" '[.[] | select(.pull_request == null) | select((.body // "") | contains($marker))] | length' "$ISSUES_JSON")"
if [ "$match_count" -eq 0 ]; then
  echo "HEARTBEAT_FAIL reason=heartbeat_issue_missing" >&2
  exit 1
fi
if [ "$match_count" -ne 1 ]; then
  echo "HEARTBEAT_FAIL reason=heartbeat_issue_duplicate count=$match_count" >&2
  exit 1
fi

body="$(jq -r --arg marker "$MARKER" '.[] | select(.pull_request == null) | select((.body // "") | contains($marker)) | .body' "$ISSUES_JSON")"
tick="$(printf '\140')"
timestamp="$(printf '%s\n' "$body" | awk -F "$tick" '/^Last heartbeat \(UTC\): / { print $2; exit }')"
if [ -z "$timestamp" ]; then
  echo "HEARTBEAT_FAIL reason=timestamp_missing" >&2
  exit 1
fi

heartbeat_epoch="$(
  date -u -d "$timestamp" +%s 2>/dev/null ||
    date -u -j -f '%Y-%m-%dT%H:%M:%SZ' "$timestamp" +%s 2>/dev/null ||
    true
)"
now_epoch="$(date -u +%s)"
if ! [[ "$heartbeat_epoch" =~ ^[0-9]+$ ]]; then
  echo "HEARTBEAT_FAIL reason=timestamp_invalid" >&2
  exit 1
fi
age_seconds=$((now_epoch - heartbeat_epoch))
if [ "$age_seconds" -lt 0 ]; then
  echo "HEARTBEAT_FAIL reason=timestamp_in_future" >&2
  exit 1
fi
if [ "$age_seconds" -gt "$MAX_AGE_SECONDS" ]; then
  echo "HEARTBEAT_FAIL reason=heartbeat_stale age_seconds=$age_seconds max_age_seconds=$MAX_AGE_SECONDS" >&2
  exit 1
fi

printf 'HEARTBEAT_OK age_seconds=%s max_age_seconds=%s\n' "$age_seconds" "$MAX_AGE_SECONDS"
