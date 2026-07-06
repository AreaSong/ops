# Backup scripts

Local backup targets are stored under `/var/backups/ops/` and logs under `/var/log/backup/`.

Scripts:
- `backup-configs.sh`: backs up x-ui, nginx, ops repo, service config files.
- `backup-postgres.sh`: logical `pg_dumpall` backups for known Postgres containers.
- `backup-redis.sh`: requests Redis BGSAVE, waits for completion, and archives a stable `dump.rdb` snapshot plus `users.acl` when present as root-only `redis-*.tar.gz`.
- `backup-volumes.sh`: archives non-database application data volumes/bind mounts.
- `sync-r2.sh`: uploads local backup artifacts to Cloudflare R2 with `/etc/ops/r2-backup.env`; uses `--s3-no-head` to avoid Cloudflare R2 post-upload HEAD 501 false failures with the current rclone build.

Retention:
- Local backup files older than 7 days are deleted by each local backup script.
- R2 sync uses copy semantics and does not delete remote objects. Configure Cloudflare R2 lifecycle rules separately when a remote retention window is decided.

Sensitive Redis note:
- Redis backups include `users.acl` when present. The file contains ACL password hashes, so backup artifacts must remain root-only locally and access-controlled in R2.

R2 compatibility note:
- `rclone v1.60.1-DEV` can upload to Cloudflare R2 successfully while returning `NotImplemented` on post-upload HEAD checks. Keep `--s3-no-head` in `sync-r2.sh` unless a future rclone/R2 compatibility test proves it is no longer needed.
