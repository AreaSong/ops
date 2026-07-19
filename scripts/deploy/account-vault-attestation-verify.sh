#!/usr/bin/env bash
set -Eeuo pipefail

umask 077
IMAGE="${1:-}"
GIT_SHA="${2:-}"
RECEIPT_OUT="${3:-}"
TOKEN_FILE="${ACCOUNT_VAULT_GITHUB_TOKEN_FILE:-/etc/account-vault/github-read-token}"
REPOSITORY="AreaSong/sorryiosSearch"
SIGNER_WORKFLOW="AreaSong/sorryiosSearch/.github/workflows/ci.yml"

[[ "$IMAGE" =~ ^ghcr\.io/areasong/sorryiossearch@sha256:[0-9a-f]{64}$ ]] || {
  echo "Attestation verifier requires the immutable Account Vault RepoDigest." >&2
  exit 2
}
[[ "$GIT_SHA" =~ ^[0-9a-f]{40}$ ]] || {
  echo "Attestation verifier requires the approved 40-character Git SHA." >&2
  exit 2
}
[ -n "$RECEIPT_OUT" ] || {
  echo "Attestation verifier requires a receipt output path." >&2
  exit 2
}
command -v gh >/dev/null 2>&1 || {
  echo "GitHub CLI is required for provenance verification." >&2
  exit 1
}
command -v jq >/dev/null 2>&1 || {
  echo "jq is required for provenance receipt validation." >&2
  exit 1
}
[ "$(id -u)" -eq 0 ] || {
  echo "Attestation verification must run as root." >&2
  exit 1
}
[ "$(stat -c '%a %U:%G' "$TOKEN_FILE")" = "600 root:root" ] || {
  echo "GitHub read token must be root:root 0600." >&2
  exit 1
}

temporary="${RECEIPT_OUT}.tmp"
provenance="${temporary}.provenance"
sbom="${temporary}.sbom"
trivy="${temporary}.trivy"
cleanup() {
  rm -f "$temporary" "$provenance" "$sbom" "$trivy"
}
trap cleanup EXIT

verify_predicate() {
  local predicate_type="$1"
  local output="$2"
  GH_TOKEN="$(<"$TOKEN_FILE")" gh attestation verify "oci://$IMAGE" \
    --repo "$REPOSITORY" \
    --signer-workflow "$SIGNER_WORKFLOW" \
    --source-digest "$GIT_SHA" \
    --source-ref refs/heads/main \
    --predicate-type "$predicate_type" \
    --deny-self-hosted-runners \
    --bundle-from-oci \
    --format json >"$output"
  jq -e 'type == "array" and length > 0' "$output" >/dev/null
}

verify_predicate https://slsa.dev/provenance/v1 "$provenance"
verify_predicate https://cyclonedx.org/bom "$sbom"
verify_predicate https://areasong.top/attestations/trivy/v1 "$trivy"
jq -n \
  --slurpfile provenance "$provenance" \
  --slurpfile sbom "$sbom" \
  --slurpfile trivy "$trivy" \
  '{provenance: $provenance[0], sbom: $sbom[0], trivy: $trivy[0]}' >"$temporary"
chmod 0600 "$temporary"
mv "$temporary" "$RECEIPT_OUT"
trap - EXIT
cleanup
