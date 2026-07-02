# Backup scripts

Local backup targets are stored under `/var/backups/ops/` and logs under `/var/log/backup/`.

Scripts:
- `backup-configs.sh`: backs up x-ui, nginx, ops repo, service config files.
- `backup-postgres.sh`: logical `pg_dumpall` backups for known Postgres containers.
- `backup-redis.sh`: requests Redis BGSAVE when available and archives Redis persistence files.
- `backup-volumes.sh`: archives non-database application data volumes/bind mounts.

Retention: local backup files older than 7 days are deleted by each script.
