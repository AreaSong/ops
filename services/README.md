# LosAngeles service compose controlled copies

This directory stores Git-controlled copies of production service Compose files.

Active runtime files currently live under `/opt/services/<service>/compose.yml`.
The files here are operational records and recovery references. When changing a
service Compose file, update the runtime file first, verify the service, then sync
the verified file back here and commit it.

Current controlled copies:

- `services/sub2api/compose.yml` from `/opt/services/sub2api/compose.yml`
- `services/account-vault/compose.yml` from `/opt/services/account-vault/compose.yml`
- `services/resume-jadeai/compose.yml` from `/opt/services/resume-jadeai/compose.yml`

Secret handling:

- Do not commit `.env` files or secret material.
- Compose files may reference environment variables and root-only env files, but
  should not contain plaintext passwords, API tokens, or private keys.
