# Backup scripts

Local backup targets are stored under `/var/backups/ops/` and logs under `/var/log/backup/`.

Scripts:
- `backup-configs.sh`: backs up x-ui, nginx, ops repo, service config files.
- `backup-postgres.sh`: logical `pg_dumpall` backups for known Postgres containers.
- `backup-redis.sh`: requests Redis BGSAVE, waits for completion, and archives a stable `dump.rdb` snapshot plus `users.acl` when present as root-only `redis-*.tar.gz`.
- `backup-volumes.sh`: archives non-database application data volumes/bind mounts.
- `sync-r2.sh`: uploads local backup artifacts to Cloudflare R2 with `/etc/ops/r2-backup.env`; uses `--s3-no-head` to avoid Cloudflare R2 post-upload HEAD 501 false failures with the current rclone build.
- `restore-areaforge-isolated.sh`: restores explicitly selected AreaForge artifacts from local storage or R2 into a network-isolated temporary PostgreSQL container and root-only extraction directories.
- `create-backup-manifest.sh`: selects all nine required artifacts from one bounded backup window, validates every archive, writes a JSON manifest plus SHA-256 sidecar, and emits backup-set metrics.
- `verify-backup-set-r2.sh`: downloads the latest manifest and all selected artifacts from R2, verifies sizes, SHA-256, archive readability, exact required roles, and emits verification metrics.
- `archive-compliance-logs.sh`: filters the previous UTC day of auditd, auth, Nginx, and daily-report data, uploads immutable-by-key parts through the compliance Worker, and invokes independent read-back verification.
- `verify-compliance-log-archive.sh`: uses a separate read-only R2 credential to verify the newest archive and the complete manifest hash chain.

Retention:
- Local backup files older than 7 days are deleted by each local backup script.
- R2 sync uses copy semantics and does not delete remote objects. Configure Cloudflare R2 lifecycle rules separately when a remote retention window is decided.

Complete backup sets:
- The `04:00 UTC` manifest job groups the latest artifacts produced by the existing staggered backup jobs. It is an explicit recovery set, not an atomic filesystem snapshot.
- A valid set contains exactly nine roles and must have no more than three hours between its oldest and newest artifact.
- Manifests are root-only under `/var/backups/ops/manifests/`; `latest-manifest.txt` identifies the set used by automated R2 verification.
- Normal `sync-r2.sh` runs do not publish a success metric until every selected R2 object has been downloaded and SHA-256 verified. `--skip-verify` is an explicit emergency bypass and leaves the separate verification freshness alert stale.
- R2 verification requires a separate root-only `/etc/ops/r2-verify.env` (or `R2_VERIFY_ENV`) and rejects the upload credential file, the same underlying file, or the same access key ID. Normal upload success is not published until this independent download path succeeds.
- Compliance log archives use `/etc/ops/compliance-archive.env` for an append-only Worker URL/token and `/etc/ops/compliance-archive-verify.env` for the independent read-only R2 token. The Worker rejects overwrite and exposes no delete route. Cloudflare R2 does not support Object Lock, so this is access isolation rather than provider-level WORM.

AreaForge restore drill:
- R2 restores require `--manifest manifests/backup-set-...json`; the script resolves and verifies the four AreaForge roles from that exact set. Local legacy backups may pass all four artifact paths plus `--postgres-image`. Never select each artifact with an independent "latest file" query.
- Use `--source r2` to prove the offsite copy is readable. R2 restores require `R2_VERIFY_ENV` pointing to a separate credential file, and the script rejects the upload config path or the same underlying file. Cloudflare permissions must still be verified as read-only before deployment. The sidecar and manifest prove object consistency, not storage-side immutability; legacy R2 restores are rejected.
- Config restore extracts only `opt/areaforge/docker-compose.prod.yml` and `opt/areaforge/.env.production`. Volume extraction accepts regular files and directories only; links, device nodes, duplicate names, absolute paths, and `..` traversal are rejected.
- The temporary PostgreSQL container requires the image ID recorded in the manifest; a configured tag is accepted only when it still resolves to that ID. It uses `--network none`, no published port, and a unique temporary Docker volume.
- Manifests record archive member counts and unpacked sizes. Restore checks those values, bounds extraction, and preflights both the work filesystem and Docker storage before import.
- Production comparison is enabled by default: it compares normalized user schema and table name lists, records both database sizes, and rejects a restored database that is unexpectedly small. `--no-compare-production` performs an offline import check and does not publish success metrics.
- Successful runs publish `areaforge_restore_drill_*` textfile metrics and write a root-readable log under `/var/log/backup/`.

Sensitive Redis note:
- Redis backups include `users.acl` when present. The file contains ACL password hashes, so backup artifacts must remain root-only locally and access-controlled in R2.

R2 compatibility note:
- `rclone v1.60.1-DEV` can upload to Cloudflare R2 successfully while returning `NotImplemented` on post-upload HEAD checks. Keep `--s3-no-head` in `sync-r2.sh` unless a future rclone/R2 compatibility test proves it is no longer needed.
