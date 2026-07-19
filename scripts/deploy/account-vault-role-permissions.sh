#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VALIDATOR="${ACCOUNT_VAULT_RELEASE_VALIDATOR:-$SCRIPT_DIR/account-vault-release-validate.py}"
GRANTS_SQL="${ACCOUNT_VAULT_ROLE_GRANTS_SQL:-$SCRIPT_DIR/account-vault-role-grants.sql}"
VERIFY_SQL="${ACCOUNT_VAULT_ROLE_VERIFY_SQL:-$SCRIPT_DIR/account-vault-role-verify.sql}"
POSTGRES_CONTAINER="${ACCOUNT_VAULT_POSTGRES_CONTAINER:-account-vault-postgres-1}"
ACTION="${1:-}"
ENV_FILE="${2:-}"
[ "$ACTION" = apply ] || [ "$ACTION" = verify ] || {
  echo "Usage: account-vault-role-permissions.sh <apply|verify> <release-env-file>" >&2
  exit 2
}
[ -n "$ENV_FILE" ] || {
  echo "Usage: account-vault-role-permissions.sh <apply|verify> <release-env-file>" >&2
  exit 2
}

[ "$(id -u)" -eq 0 ] || {
  echo "Account Vault role permissions must run as root." >&2
  exit 1
}
for path in "$VALIDATOR" "$GRANTS_SQL" "$VERIFY_SQL" "$ENV_FILE"; do
  [ -r "$path" ] || {
    echo "Required role-permission input is missing: $path" >&2
    exit 1
  }
done

IFS=$'\t' read -r migration_user app_user < <(python3 "$VALIDATOR" roles "$ENV_FILE")
[ -n "$migration_user" ] && [ -n "$app_user" ]

if [ "$ACTION" = apply ]; then
  docker exec -i "$POSTGRES_CONTAINER" psql \
    -v ON_ERROR_STOP=1 \
    -v "migration_user=$migration_user" \
    -v "app_user=$app_user" \
    -U "$migration_user" -d accountvault -f - <"$GRANTS_SQL"
fi

result="$(docker exec -i "$POSTGRES_CONTAINER" psql \
  -v ON_ERROR_STOP=1 \
  -v verify_contract=1 \
  -v "app_user=$app_user" \
  -U "$migration_user" -d accountvault -At -f - <"$VERIFY_SQL")"
[ "$result" = "0|0|false|1|true|false|false|false|false|false|0|false" ] || {
  echo "Account Vault runtime-role permission verification failed: $result" >&2
  exit 1
}

echo "Account Vault runtime-role attributes, memberships, grants and default privileges verified."
