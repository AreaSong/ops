#!/usr/bin/env bash
set -Eeuo pipefail

repo_root="${1:-}"
source_revision="${2:-}"
deployed_revision="${3:-}"

fail() {
  printf 'preflight failed: %s\n' "$*" >&2
  exit 1
}

[[ -d "$repo_root" ]] || fail "repository root is missing"
[[ "$source_revision" =~ ^[a-f0-9]{40}$ ]] || fail "source revision is not a full Git commit"
[[ "$deployed_revision" =~ ^[a-f0-9]{40}$ ]] || fail "deployed revision is not a full Git commit"

git -C "$repo_root" cat-file -e "${source_revision}^{commit}" 2>/dev/null ||
  fail "source revision is not present in the repository"
git -C "$repo_root" cat-file -e "${deployed_revision}^{commit}" 2>/dev/null ||
  fail "deployed revision is not present in the repository"
git -C "$repo_root" merge-base --is-ancestor "$deployed_revision" "$source_revision" ||
  fail "deployed revision is not an ancestor of source HEAD"

printf 'deployed revision: %s\n' "$deployed_revision"
if [[ "$source_revision" == "$deployed_revision" ]]; then
  printf 'source/runtime drift: none\n'
else
  printf 'source/runtime drift: source HEAD is ahead of the deployed revision\n'
fi
