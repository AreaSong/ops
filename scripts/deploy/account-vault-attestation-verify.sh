#!/usr/bin/env bash
set -Eeuo pipefail

umask 077
IMAGE="${1:-}"
GIT_SHA="${2:-}"
RECEIPT_OUT="${3:-}"
TOKEN_FILE="${ACCOUNT_VAULT_GITHUB_TOKEN_FILE:-/etc/account-vault/github-read-token}"
REGISTRY_USER="${ACCOUNT_VAULT_GHCR_USER:-AreaSong}"
CERTIFICATE_IDENTITY="https://github.com/AreaSong/sorryiosSearch/.github/workflows/ci.yml@refs/heads/main"
OIDC_ISSUER="https://token.actions.githubusercontent.com"

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
for command_name in cosign jq mktemp stat; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "Required attestation verifier command is missing: $command_name" >&2
    exit 1
  }
done
[ "$(id -u)" -eq 0 ] || {
  echo "Attestation verification must run as root." >&2
  exit 1
}
[ "$(stat -c '%a %U:%G' "$TOKEN_FILE")" = "600 root:root" ] || {
  echo "GitHub read token must be root:root 0600." >&2
  exit 1
}

work_dir="$(mktemp -d /var/tmp/account-vault-attestation.XXXXXX)"
docker_config="$work_dir/docker"
temporary="${RECEIPT_OUT}.tmp"
provenance="$work_dir/provenance.json"
sbom="$work_dir/sbom.json"
trivy="$work_dir/trivy.json"
cleanup() {
  rm -rf "$work_dir"
  rm -f "$temporary"
}
trap cleanup EXIT
install -d -m 0700 "$docker_config"
export DOCKER_CONFIG="$docker_config"

cosign login ghcr.io --username "$REGISTRY_USER" --password-stdin <"$TOKEN_FILE" >/dev/null

verify_predicate() {
  local predicate_type="$1"
  local output="$2"
  local raw_output="${output}.raw"
  cosign verify-attestation \
    --type "$predicate_type" \
    --certificate-identity "$CERTIFICATE_IDENTITY" \
    --certificate-oidc-issuer "$OIDC_ISSUER" \
    --certificate-github-workflow-name "Account Vault CI" \
    --certificate-github-workflow-ref refs/heads/main \
    --certificate-github-workflow-repository AreaSong/sorryiosSearch \
    --certificate-github-workflow-sha "$GIT_SHA" \
    --certificate-github-workflow-trigger push \
    --output json \
    "$IMAGE" >"$raw_output"
  jq -s -e '
    select(length > 0 and all(.[ ];
      type == "object" and
      (.payloadType | type == "string" and length > 0) and
      (.payload | type == "string" and length > 0) and
      (.signatures | type == "array" and length > 0)
    ))
  ' "$raw_output" >"$output"
}

verify_predicate slsaprovenance1 "$provenance"
verify_predicate cyclonedx "$sbom"
verify_predicate https://areasong.top/attestations/trivy/v1 "$trivy"
jq -n \
  --arg scheme sigstore-keyless-oci-v1 \
  --arg image "$IMAGE" \
  --arg gitSha "$GIT_SHA" \
  --arg certificateIdentity "$CERTIFICATE_IDENTITY" \
  --arg certificateOidcIssuer "$OIDC_ISSUER" \
  --slurpfile provenance "$provenance" \
  --slurpfile sbom "$sbom" \
  --slurpfile trivy "$trivy" \
  '{scheme: $scheme, image: $image, gitSha: $gitSha,
    certificateIdentity: $certificateIdentity,
    certificateOidcIssuer: $certificateOidcIssuer,
    provenance: $provenance[0], sbom: $sbom[0], trivy: $trivy[0]}' >"$temporary"
chmod 0600 "$temporary"
mv "$temporary" "$RECEIPT_OUT"
trap - EXIT
cleanup
