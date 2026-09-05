#!/usr/bin/env bash
set -Eeuo pipefail

fail() {
  printf 'release asset verification failed: %s\n' "$1" >&2
  exit 1
}

[ "$#" -eq 4 ] || fail "usage: verify-release-assets.sh <manifest> <runner-archive> <checksum> <sigstore-bundle>"

manifest="$1"
archive="$2"
checksum="$3"
bundle="$4"

expected_identity="https://github.com/AreaSong/ops/.github/workflows/areasong-ops-release.yml@refs/heads/main"
expected_issuer="https://token.actions.githubusercontent.com"

[ -f "$manifest" ] || fail "manifest is missing"
[ -f "$archive" ] || fail "Runner archive is missing"
[ -f "$checksum" ] || fail "checksum file is missing"
[ -f "$bundle" ] || fail "Sigstore bundle is missing"
command -v jq >/dev/null 2>&1 || fail "jq is required"
command -v cosign >/dev/null 2>&1 || fail "cosign is required"

archive_name="$(basename "$archive")"
checksum_line="$(cat "$checksum")"
checksum_digest="${checksum_line%% *}"
checksum_target="${checksum_line#*  }"

[[ "$checksum_line" == "$checksum_digest  $checksum_target" ]] || fail "checksum must use the canonical two-space format"
[[ "$checksum_digest" =~ ^[a-f0-9]{64}$ ]] || fail "checksum digest is invalid"
[[ "$checksum_target" == "$archive_name" ]] || fail "checksum must reference only the archive basename"

manifest_fields="$(jq -er '
  select(
    .schemaVersion == 2 and
    .service == "areasong-ops" and
    .platform == "linux/amd64" and
    .web.cosign == "keyless" and
    .runner.cosign == "keyless"
  ) |
  [.version, .revision, .web.image, .runner.archive, .runner.sha256] |
  select(all(.[]; type == "string")) |
  @tsv
' "$manifest")" || fail "manifest contract is invalid"
IFS=$'\t' read -r manifest_version manifest_revision web_image manifest_archive manifest_digest <<<"$manifest_fields"

[[ "$manifest_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] ||
  fail "manifest version is invalid"
[[ "$manifest_revision" =~ ^[a-f0-9]{40}$ ]] || fail "manifest revision is invalid"
[[ "$web_image" =~ ^ghcr\.io/areasong/areasong-ops-web:${manifest_revision}@sha256:[a-f0-9]{64}$ ]] ||
  fail "manifest Web image is not bound to its revision and digest"
[[ "$manifest_archive" == "areasong-ops-runner-$manifest_revision-linux-amd64.tar.gz" ]] ||
  fail "manifest Runner archive is not bound to its revision"
[[ "$manifest_digest" =~ ^sha256:[a-f0-9]{64}$ ]] || fail "manifest Runner digest is invalid"

[[ "$manifest_archive" == "$archive_name" ]] || fail "manifest archive does not match the downloaded file"
[[ "$manifest_digest" == "sha256:$checksum_digest" ]] || fail "manifest and checksum digests differ"

if command -v sha256sum >/dev/null 2>&1; then
  actual_digest="$(sha256sum "$archive" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  actual_digest="$(shasum -a 256 "$archive" | awk '{print $1}')"
else
  fail "no SHA-256 implementation is available"
fi

[[ "$actual_digest" == "$checksum_digest" ]] || fail "Runner archive digest mismatch"

cosign verify-blob \
  --bundle "$bundle" \
  --certificate-identity "$expected_identity" \
  --certificate-oidc-issuer "$expected_issuer" \
  "$archive" >/dev/null || fail "Runner signature verification failed"

cosign verify \
  --certificate-identity "$expected_identity" \
  --certificate-oidc-issuer "$expected_issuer" \
  "$web_image" >/dev/null || fail "Web image signature verification failed"

printf 'release asset verification: PASS (%s)\n' "$manifest_digest"
