# LosAngeles service compose controlled copies

This directory stores Git-controlled copies of production service Compose files.

Active runtime files currently live under `/opt/services/<service>/compose.yml`,
except AreaForge, whose authoritative runtime path is recorded in the asset
inventory. The files here are the reviewed source of truth, recovery references,
and deployment inputs. Change and validate the controlled copy first; an approved
deployment then installs that exact file to the runtime path. Do not edit runtime
first and backfill Git afterward.

Current controlled copies:

- `services/sub2api/compose.yml` from `/opt/services/sub2api/compose.yml`
- `services/account-vault/compose.yml` from `/opt/services/account-vault/compose.yml`
- `services/resume-jadeai/compose.yml` from `/opt/services/resume-jadeai/compose.yml`
- `services/areaforge/compose.yml` from `/opt/areaforge/docker-compose.prod.yml`

Secret handling:

- Do not commit `.env` files or secret material.
- Compose files may reference environment variables and root-only env files, but
  should not contain plaintext passwords, API tokens, or private keys.
