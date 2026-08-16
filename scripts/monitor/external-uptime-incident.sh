#!/usr/bin/env bash
set -euo pipefail

readonly OUTCOME="${1:-}"
readonly RESULT_FILE="${2:-}"
readonly INCIDENT_KIND="${3:-production}"
WORK_DIR="$(mktemp -d)"
MANAGED_BODY="$WORK_DIR/managed-incident.md"
ISSUES_JSON="$WORK_DIR/open-issues.json"

trap 'rm -rf "$WORK_DIR"' EXIT

case "$INCIDENT_KIND" in
  production)
    readonly TITLE='[monitor] LosAngeles external uptime failure'
    readonly LABEL='external-uptime'
    readonly MARKER='<!-- areasong-external-uptime-managed:v1 -->'
    readonly RECOVERY_SUBJECT='External HTTPS checks'
    ;;
  simulation)
    readonly TITLE='[monitor:test] LosAngeles external uptime simulation'
    readonly LABEL='external-uptime-test'
    readonly MARKER='<!-- areasong-external-uptime-simulation:v1 -->'
    readonly RECOVERY_SUBJECT='External HTTPS simulation'
    ;;
  heartbeat)
    readonly TITLE='[monitor] LosAngeles external heartbeat missing'
    readonly LABEL='external-heartbeat'
    readonly MARKER='<!-- areasong-external-heartbeat-incident:v1 -->'
    readonly RECOVERY_SUBJECT='LosAngeles external heartbeat'
    ;;
  *)
    echo "incident kind must be production, simulation, or heartbeat" >&2
    exit 2
    ;;
esac

case "$OUTCOME" in
  success|failure) ;;
  *)
    echo "usage: $0 success|failure RESULT_FILE [production|simulation|heartbeat]" >&2
    exit 2
    ;;
esac

command -v jq >/dev/null 2>&1 || {
  echo "jq is required for managed Issue filtering" >&2
  exit 1
}

if [ "$OUTCOME" = failure ] && [ ! -r "$RESULT_FILE" ]; then
  echo "monitor result is missing or unreadable: $RESULT_FILE" >&2
  exit 1
fi

if [ "$OUTCOME" = failure ]; then
  {
    printf '%s\n\n' "$MARKER"
    cat "$RESULT_FILE"
  } >"$MANAGED_BODY"
fi

gh label create "$LABEL" --color B60205 \
  --description 'Managed external availability incident' --force

issue_numbers=()
gh issue list --state open --label "$LABEL" --limit 1000 \
  --json number,body >"$ISSUES_JSON"
while IFS= read -r issue_number; do
  [ -n "$issue_number" ] && issue_numbers+=("$issue_number")
done < <(
  jq -r --arg marker "$MARKER" \
    '.[] | select((.body // "") | contains($marker)) | .number' "$ISSUES_JSON"
)
primary_issue="${issue_numbers[0]:-}"

if [ "$OUTCOME" = failure ] && [ -z "$primary_issue" ]; then
  gh issue create --title "$TITLE" --label "$LABEL" --body-file "$MANAGED_BODY"
elif [ "$OUTCOME" = failure ]; then
  gh issue edit "$primary_issue" --title "$TITLE" --body-file "$MANAGED_BODY"
  for duplicate in "${issue_numbers[@]:1}"; do
    gh issue close "$duplicate" --comment 'Closed as a duplicate external uptime incident.'
  done
else
  recovered_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  for issue_number in "${issue_numbers[@]}"; do
    gh issue close "$issue_number" \
      --comment "$RECOVERY_SUBJECT recovered at $recovered_at."
  done
fi
