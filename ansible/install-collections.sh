#!/usr/bin/env bash
set -Eeuo pipefail

VERSION="10.7.9"
SHA256="7d88906f7d62d6e2bab6a38b20eac51479766c536d2efd652b3d396b9dc90e2c"
DESTINATION="${ANSIBLE_COLLECTIONS_DESTINATION:-/var/lib/ops/ansible-collections}"
ARCHIVE_URL="https://galaxy.ansible.com/api/v3/plugin/ansible/content/published/collections/artifacts/community-general-${VERSION}.tar.gz"

work_dir="$(mktemp -d)"
trap 'rm -rf -- "$work_dir"' EXIT
archive="$work_dir/community-general-${VERSION}.tar.gz"

curl --proto '=https' --tlsv1.2 --fail --location --silent --show-error \
  --output "$archive" "$ARCHIVE_URL"
printf '%s  %s\n' "$SHA256" "$archive" | sha256sum --check --status
install -d -m 0755 "$DESTINATION"
ansible-galaxy collection install --force --collections-path "$DESTINATION" "$archive"
