# Backup scripts

Local backup targets are stored under `/var/backups/ops/` and logs under `/var/log/backup/`.

Scripts:
- `backup-configs.sh`: backs up x-ui, nginx, ops repo, service config files.
- `backup-postgres.sh`: logical `pg_dumpall` backups for known Postgres containers.
- `backup-redis.sh`: requests Redis BGSAVE, waits for completion, and archives a stable `dump.rdb` snapshot as `redis-*.tar.gz`.
- `backup-volumes.sh`: archives non-database application data volumes/bind mounts.
- `sync-r2.sh`: uploads local backup artifacts to Cloudflare R2 with `/etc/ops/r2-backup.env`.

Retention:
- Local backup files older than 7 days are deleted by each local backup script.
- R2 sync uses copy semantics and does not delete remote objects. Configure Cloudflare R2 lifecycle rules separately when a remote retention window is decided.
