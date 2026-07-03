# LosAngeles /root 历史服务目录审计记录

日期：2026-07-03 12:10 BST
服务器：LosAngeles
审计类型：非破坏性路径与运行时引用审计

## 1. 结论

本次只做审计，没有迁移、删除、重启任何生产服务。

初步结论：

- 当前运行中的 Docker Compose 项目主要来自 `/opt/services` 与 `/opt/ops/observability`。
- `sub2api` 的 compose 文件已迁移到 `/opt/services/sub2api`，但容器仍通过 bind mount 使用 `/root/sub2api-deploy/data`、`/root/sub2api-deploy/postgres_data`、`/root/sub2api-deploy/redis_data`。
- `/root/sub2api-deploy` 当前不能整体归档或删除；需要单独安排 sub2api 数据目录迁移。
- `/root/JadeAI`、`/root/sorryiosSearch` 未在 Docker compose labels、进程 cwd、Nginx/cron 快速扫描中发现明确运行时引用，可列为归档候选。

## 2. 重点目录判断

| 目录 | 是否存在 | 判断 | 建议 |
| --- | --- | --- | --- |
| /root/sub2api-deploy | 是 | 仍被 sub2api 容器数据 bind mount 使用 | 不要归档或删除；应在维护窗口迁移数据目录到 `/opt/services/sub2api` 或 `/data`，并准备回滚方案。 |
| /root/JadeAI | 是 | 未发现运行时引用，但像历史部署目录 | 建议先归档/备份后再评估是否迁移到 /opt/services 或删除。 |
| /root/sorryiosSearch | 是 | 未发现运行时引用，但像历史部署目录 | 建议先归档/备份后再评估是否迁移到 /opt/services 或删除。 |

## 3. 运行中容器路径引用

| 容器 | compose project | working_dir | config_files | bind mounts under /root or /opt/services | 状态 |
| --- | --- | --- | --- | --- | --- |
| alertmanager | observability | /opt/ops/observability | /opt/ops/observability/docker-compose.yml | - | running |
| grafana | observability | /opt/ops/observability | /opt/ops/observability/docker-compose.yml | - | running |
| prometheus | observability | /opt/ops/observability | /opt/ops/observability/docker-compose.yml | - | running |
| promtail | observability | /opt/ops/observability | /opt/ops/observability/docker-compose.yml | - | running |
| blackbox-exporter | observability | /opt/ops/observability | /opt/ops/observability/docker-compose.yml | - | running |
| node-exporter | observability | /opt/ops/observability | /opt/ops/observability/docker-compose.yml | - | running |
| loki | observability | /opt/ops/observability | /opt/ops/observability/docker-compose.yml | - | running |
| resume-jadeai-app-1 | resume-jadeai | /opt/services/resume-jadeai | /opt/services/resume-jadeai/compose.yml | - | running |
| sub2api | sub2api | /opt/services/sub2api | /opt/services/sub2api/compose.yml | /root/sub2api-deploy/data | running |
| sub2api-redis | sub2api | /opt/services/sub2api | /opt/services/sub2api/compose.yml | /root/sub2api-deploy/redis_data | running |
| sub2api-postgres | sub2api | /opt/services/sub2api | /opt/services/sub2api/compose.yml | /root/sub2api-deploy/postgres_data | running |
| account-vault-web-1 | account-vault | /opt/services/account-vault | /opt/services/account-vault/compose.yml | - | running |
| account-vault-postgres-1 | account-vault | /opt/services/account-vault | /opt/services/account-vault/compose.yml | - | running |

## 4. /root 目录快照

```text
2026-04-28 09:45 /root/.cache
2026-04-28 09:47 /root/.acme.sh/23.185.200.12_ecc
2026-04-28 09:47 /root/.acme.sh/ca
2026-04-28 09:47 /root/cert/ip
2026-04-28 09:49 /root/cert/vlog.areasong.top
2026-04-28 09:50 /root/.acme.sh/vlog.areasong.top_ecc
2026-04-28 09:50 /root/cert
2026-04-28 09:51 /root/.acme.sh/log.areasong.top_ecc
2026-04-28 09:51 /root/cert/log.areasong.top
2026-05-02 17:24 /root/.config/ookla
2026-05-04 19:13 /root/sub2api-deploy
2026-05-16 09:07 /root/JadeAI
2026-05-16 09:07 /root/JadeAI/.git
2026-05-16 09:07 /root/JadeAI/.github
2026-05-16 09:07 /root/JadeAI/docs
2026-05-16 09:07 /root/JadeAI/drizzle
2026-05-16 09:07 /root/JadeAI/images
2026-05-16 09:07 /root/JadeAI/messages
2026-05-16 09:07 /root/JadeAI/public
2026-05-16 09:07 /root/JadeAI/scripts
2026-05-16 09:07 /root/JadeAI/src
2026-06-19 23:13 /root/.acme.sh
2026-06-19 23:13 /root/.acme.sh/deploy
2026-06-19 23:13 /root/.acme.sh/dnsapi
2026-06-19 23:13 /root/.acme.sh/notify
2026-07-01 08:32 /root/sorryiosSearch/backend
2026-07-01 08:32 /root/sorryiosSearch/nginx
2026-07-01 08:38 /root/.docker
2026-07-01 09:06 /root/sorryiosSearch/frontend
2026-07-01 09:06 /root/sorryiosSearch/scripts
2026-07-01 09:10 /root/sorryiosSearch
2026-07-01 09:10 /root/sorryiosSearch/.git
2026-07-02 02:40 /root/service-governance-backup-20260702-031245/sites-enabled
2026-07-02 02:55 /root/service-naming-refactor-backup-20260702-034215/nginx
2026-07-02 02:56 /root/service-governance-backup-20260702-031245/sites-available
2026-07-02 03:00 /root/.config/warp-terminal
2026-07-02 03:00 /root/.local
2026-07-02 03:00 /root/.local/share
2026-07-02 03:00 /root/.local/state
2026-07-02 03:00 /root/.warp
2026-07-02 03:00 /root/.warp/remote-server
2026-07-02 03:13 /root/service-governance-backup-20260702-031245
2026-07-02 03:42 /root/service-naming-refactor-backup-20260702-034215
2026-07-02 03:42 /root/service-naming-refactor-backup-20260702-034215/compose
2026-07-02 03:42 /root/service-naming-refactor-backup-20260702-034215/docker-inspect
2026-07-02 03:46 /root/.docker/buildx
2026-07-02 17:44 /root/.ssh
2026-07-03 08:36 /root/.config
2026-07-03 08:36 /root/.config/rclone
2026-07-03 09:10 /root/sub2api-deploy/postgres_data
2026-07-03 11:58 /root/sub2api-deploy/data
2026-07-03 12:06 /root/sub2api-deploy/redis_data
```

## 5. /root 候选部署文件

```text
2026-05-04 18:51 18522 /root/sub2api-deploy/.env.example
2026-05-16 09:07 2369 /root/JadeAI/package.json
2026-05-16 09:07 456 /root/JadeAI/.env.example
2026-07-01 08:32 1145 /root/sorryiosSearch/backend/package.json
2026-07-01 08:32 399 /root/sorryiosSearch/backend/.env.example
2026-07-01 08:32 502 /root/sorryiosSearch/.env.example
2026-07-01 08:32 669 /root/sorryiosSearch/frontend/package.json
2026-07-02 03:13 1000 /root/sorryiosSearch/docker-compose.yml
2026-07-02 03:13 10076 /root/sub2api-deploy/docker-compose.yml
```

## 6. /opt/services 目录快照

```text
2026-07-02 03:45 /opt/services
2026-07-02 03:45 /opt/services/account-vault
2026-07-02 03:45 /opt/services/sub2api
2026-07-02 03:46 /opt/services/resume-jadeai
```

## 7. /opt/services compose 文件

```text
2026-07-02 03:45 10167 /opt/services/sub2api/compose.yml
2026-07-02 03:45 1130 /opt/services/account-vault/compose.yml
2026-07-02 03:46 401 /opt/services/resume-jadeai/compose.yml
```

## 8. Nginx 相关引用快照

```text
/etc/nginx/sites-available/cdn.sorryiossearch.areasong.top.conf:1:# sorryiossearch.areasong.top — CDN 访问，源站使用 Cloudflare Origin Certificate
/etc/nginx/sites-available/cdn.sorryiossearch.areasong.top.conf:5:    server_name sorryiossearch.areasong.top;
/etc/nginx/sites-available/cdn.sorryiossearch.areasong.top.conf:12:    server_name sorryiossearch.areasong.top;
/etc/nginx/sites-available/cdn.resume.areasong.top.conf:1:# resume.areasong.top — CDN 访问，源站使用 Cloudflare Origin Certificate
/etc/nginx/sites-available/cdn.resume.areasong.top.conf:5:    server_name resume.areasong.top;
/etc/nginx/sites-available/cdn.resume.areasong.top.conf:12:    server_name resume.areasong.top;
```

## 9. Cron 相关引用快照

```text
13 23 * * * "/root/.acme.sh"/acme.sh --cron --home "/root/.acme.sh" > /dev/null

# BEGIN ops local backups
10 2 * * * /opt/ops/scripts/backup/backup-postgres.sh >> /var/log/backup/postgres.log 2>&1
30 2 * * * /opt/ops/scripts/backup/backup-redis.sh >> /var/log/backup/redis.log 2>&1
0 3 * * * /opt/ops/scripts/backup/backup-configs.sh >> /var/log/backup/configs.log 2>&1
30 3 * * * /opt/ops/scripts/backup/backup-volumes.sh >> /var/log/backup/volumes.log 2>&1
# END ops local backups

# BEGIN ops offsite backups
15 4 * * * /opt/ops/scripts/backup/sync-r2.sh >> /var/log/backup/r2.log 2>&1
# END ops offsite backups

# BEGIN ops observability metrics
*/5 * * * * /opt/ops/observability/scripts/write-docker-metrics.sh >> /var/log/backup/docker-metrics.log 2>&1
45 3 * * * /opt/ops/observability/scripts/write-backup-metrics.sh >> /var/log/backup/backup-metrics.log 2>&1
# END ops observability metrics
/etc/cron.d:
certbot
e2scrub_all
sysstat

/etc/cron.daily:
apport
apt-compat
dpkg
logrotate
man-db
sysstat

/etc/cron.hourly:

/etc/cron.monthly:

/etc/cron.weekly:
man-db
```

## 10. Systemd 相关服务名快照

```text
docker.service
kmod-static-nodes.service
nginx.service
systemd-hibernate-resume.service
x-ui.service
```

## 11. 运行进程 cwd 引用快照

```text
(none)
```

## 12. 目录大小快照

| 路径 | 大小 |
| --- | --- |
| `/root/sub2api-deploy` | 775M |
| `/root/sub2api-deploy/data` | 94M |
| `/root/sub2api-deploy/postgres_data` | 681M |
| `/root/sub2api-deploy/redis_data` | 172K |
| `/root/JadeAI` | 64M |
| `/root/sorryiosSearch` | 1.5M |

## 13. 推荐后续处理

1. 先处理 `sub2api`：把仍在 `/root/sub2api-deploy` 下的数据 bind mount 迁移到规范路径，建议优先考虑 `/opt/services/sub2api/data` 或未来独立数据盘 `/data/sub2api`。
2. 对 `sub2api` 迁移制定维护窗口、停机影响、数据复制校验、compose 修改、启动验证和回滚方案。
3. `/root/JadeAI`、`/root/sorryiosSearch` 可先打包归档到 root-only 目录，确认本机备份和 R2 覆盖后，再决定是否删除。
4. 若后续发现 Nginx、systemd、cron、Docker compose labels 仍引用某历史目录，则先做迁移方案和回滚方案，再改路径。

## 14. 未覆盖项

- 未读取 `.env`、私钥、数据库内容、Redis key/value。
- 未登录业务应用验证历史目录是否被人工脚本依赖。
- 未执行目录归档、迁移或删除。
