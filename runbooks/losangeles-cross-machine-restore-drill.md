# LosAngeles 跨机器恢复演练预案

状态：预案完成，实机演练待维护窗口  
适用服务器：LosAngeles  
备份来源：Cloudflare R2 `losangeles-ops-backups` / `losangeles/`  
目标：在一台全新临时机器上，从 R2 备份恢复 LosAngeles 的配置、数据库、Redis、业务数据目录，并完成隔离验证或接管验证。

## 1. 目标与边界

本 Runbook 用于两类场景：

- 灾难恢复：LosAngeles 不可用，需要在新机器上恢复核心服务。
- 定期演练：开一台临时机器，验证 R2 备份能跨机器恢复。

当前状态：

- 本机备份恢复演练已通过。
- R2 拉回恢复演练已通过；2026-07-06 已完成本机隔离 R2 拉回恢复演练，详见 `runbooks/losangeles-standards-09-c1g-r2-isolated-restore-drill-20260706.md`。
- 应用级恢复演练已通过。
- 跨机器实机恢复尚未执行。

安全边界：

- 演练默认不切公网 DNS。
- 演练默认不复用生产公网 IP。
- 演练默认不向公网暴露临时数据库、Redis、Prometheus、Grafana。
- 不在日志、截图、Git 或聊天中输出 `.env`、私钥、R2 Access Key、SMTP 授权码、数据库内容。
- 真正接管生产前必须有明确维护窗口、回滚方案和 DNS 切换确认。

## 2. 临时机器要求

建议规格：

| 项目 | 建议 |
| --- | --- |
| OS | Ubuntu 24.04 LTS |
| CPU | 2 vCPU 起 |
| Memory | 4 GiB 起，建议 8 GiB |
| Disk | 至少 60 GiB，建议 80 GiB 以上 |
| Network | 可访问 Cloudflare R2、GitHub、Docker Registry |
| Inbound | 演练期仅开放 SSH；接管期再按需开放 80/443 |

基础软件：

```bash
sudo apt-get update
sudo apt-get install -y git curl ca-certificates gnupg lsb-release rclone jq tar gzip openssl
```

Docker：

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"
```

演练时建议重新登录 shell，让 docker 组生效。

## 3. 凭据准备

需要从现有安全渠道准备以下 root-only 文件，禁止提交到 Git：

| 文件 | 用途 | 权限 |
| --- | --- | --- |
| `/etc/ops/r2-backup.env` | R2 拉取备份 | `0600 root:root` |
| `/etc/observability/grafana.env` | Grafana 启动 | `0600 root:root` |
| `/etc/observability/alertmanager-smtp-password` | Alertmanager 邮件通知 | `0600 root:root` |
| `/etc/observability/postgres-exporter-*.env` | Postgres exporter | `0600 root:root` |
| `/etc/observability/redis-exporter-*.env` | Redis exporter | `0600 root:root` |
| 业务 `.env` 文件 | 应用启动 | `0600 root:root` |
| 证书私钥 | Nginx TLS | `0600 root:root` |

如果只是隔离恢复演练，不需要立即配置 SMTP 或对外 TLS；如果要完整接管，需要完整准备。

## 4. 拉取运维仓库

```bash
sudo install -d -m 0755 -o root -g root /opt/ops
sudo git clone git@github.com:AreaSong/ops.git /opt/ops
sudo git -C /opt/ops status --short
```

如果临时机器没有 GitHub deploy key，可以先用只读方式下载仓库包，或临时安装新的 deploy key。不要复用不受控的个人密钥。

## 5. 配置 R2 只读拉回环境

创建 `/etc/ops/r2-backup.env`：

```bash
sudo install -d -m 0750 -o root -g root /etc/ops
sudo install -m 0600 -o root -g root /dev/null /etc/ops/r2-backup.env
sudoedit /etc/ops/r2-backup.env
```

文件格式：

```bash
R2_BUCKET=losangeles-ops-backups
R2_ENDPOINT=https://<account-id>.r2.cloudflarestorage.com
R2_PREFIX=losangeles/
R2_ACCESS_KEY_ID=<redacted>
R2_SECRET_ACCESS_KEY=<redacted>
```

不要将该文件复制进 `/opt/ops`。

## 6. 从 R2 拉回备份

准备临时目录：

```bash
DRILL_ID="cross-restore-$(date +%Y%m%d-%H%M%S)"
RESTORE_ROOT="/tmp/${DRILL_ID}"
sudo install -d -m 0700 -o root -g root "$RESTORE_ROOT"
```

加载 R2 环境并拉回：

```bash
set -a
. /etc/ops/r2-backup.env
set +a

export RCLONE_CONFIG_R2_TYPE=s3
export RCLONE_CONFIG_R2_PROVIDER=Cloudflare
export RCLONE_CONFIG_R2_ACCESS_KEY_ID="$R2_ACCESS_KEY_ID"
export RCLONE_CONFIG_R2_SECRET_ACCESS_KEY="$R2_SECRET_ACCESS_KEY"
export RCLONE_CONFIG_R2_ENDPOINT="$R2_ENDPOINT"
export RCLONE_CONFIG_R2_REGION=auto

case "$R2_PREFIX" in
  ""|*/) ;;
  *) R2_PREFIX="${R2_PREFIX}/" ;;
esac

sudo -E rclone --config /dev/null copy \
  "r2:${R2_BUCKET}/${R2_PREFIX}" \
  "$RESTORE_ROOT/r2" \
  --s3-no-check-bucket \
  --fast-list \
  --log-level INFO
```

核验数量和大小：

```bash
sudo -E rclone --config /dev/null check \
  "r2:${R2_BUCKET}/${R2_PREFIX}" \
  "$RESTORE_ROOT/r2" \
  --size-only \
  --one-way

sudo find "$RESTORE_ROOT/r2" -type f | wc -l
sudo du -sh "$RESTORE_ROOT/r2"
```

停止条件：

- R2 拉回失败。
- `rclone check` 不通过。
- 备份文件明显缺少 Postgres、Redis、configs 或 volumes 目录。

## 7. 选择恢复点

列出最新备份：

```bash
sudo find "$RESTORE_ROOT/r2" -maxdepth 2 -type f | sort
```

建议选择同一日期的一组文件：

- `postgres/account-vault-postgres-*.sql.gz`
- `postgres/sub2api-postgres-*.sql.gz`
- `redis/redis-*.tar.gz`
- `configs/configs-*.tar.gz`
- `volumes/jadeai-data-*.tar.gz`
- `volumes/sub2api-data-*.tar.gz`

记录 RPO：

| 类型 | 备份时间 | 文件 |
| --- | --- | --- |
| Postgres | 待填 | 待填 |
| Redis | 待填 | 待填 |
| Configs | 待填 | 待填 |
| Volumes | 待填 | 待填 |

## 8. 解包配置与数据

创建解包目录：

```bash
sudo install -d -m 0700 -o root -g root "$RESTORE_ROOT/extract"
```

配置：

```bash
CONFIG_TAR="$(sudo find "$RESTORE_ROOT/r2/configs" -type f -name 'configs-*.tar.gz' | sort | tail -1)"
sudo tar -tzf "$CONFIG_TAR" >/dev/null
sudo tar -xzf "$CONFIG_TAR" -C "$RESTORE_ROOT/extract"
```

Volumes：

```bash
JADEAI_TAR="$(sudo find "$RESTORE_ROOT/r2/volumes" -type f -name 'jadeai-data-*.tar.gz' | sort | tail -1)"
SUB2API_TAR="$(sudo find "$RESTORE_ROOT/r2/volumes" -type f -name 'sub2api-data-*.tar.gz' | sort | tail -1)"

sudo tar -tzf "$JADEAI_TAR" >/dev/null
sudo tar -tzf "$SUB2API_TAR" >/dev/null
sudo tar -xzf "$JADEAI_TAR" -C "$RESTORE_ROOT/extract"
sudo tar -xzf "$SUB2API_TAR" -C "$RESTORE_ROOT/extract"
```

Redis：

```bash
REDIS_TAR="$(sudo find "$RESTORE_ROOT/r2/redis" -type f -name 'redis-*.tar.gz' | sort | tail -1)"
sudo tar -tzf "$REDIS_TAR" >/dev/null
sudo tar -xzf "$REDIS_TAR" -C "$RESTORE_ROOT/extract"
sudo find "$RESTORE_ROOT/extract" -name dump.rdb -type f -print
```

## 9. Postgres 恢复验证

account-vault：

```bash
ACCOUNT_SQL="$(sudo find "$RESTORE_ROOT/r2/postgres" -type f -name 'account-vault-postgres-*.sql.gz' | sort | tail -1)"
sudo gzip -t "$ACCOUNT_SQL"

docker run -d --rm \
  --name restore-account-vault-cross \
  -e POSTGRES_PASSWORD=restore \
  postgres:15-alpine

until docker exec restore-account-vault-cross pg_isready -U postgres >/dev/null 2>&1; do
  sleep 2
done
sleep 5

sudo gzip -dc "$ACCOUNT_SQL" | docker exec -i restore-account-vault-cross \
  psql -U postgres -v ON_ERROR_STOP=1
```

sub2api：

```bash
SUB2API_SQL="$(sudo find "$RESTORE_ROOT/r2/postgres" -type f -name 'sub2api-postgres-*.sql.gz' | sort | tail -1)"
sudo gzip -t "$SUB2API_SQL"

docker run -d --rm \
  --name restore-sub2api-cross \
  -e POSTGRES_PASSWORD=restore \
  postgres:18-alpine

until docker exec restore-sub2api-cross pg_isready -U postgres >/dev/null 2>&1; do
  sleep 2
done
sleep 5

sudo gzip -dc "$SUB2API_SQL" | docker exec -i restore-sub2api-cross \
  psql -U postgres -v ON_ERROR_STOP=1
```

验证：

```bash
docker exec restore-account-vault-cross psql -U postgres -l
docker exec restore-sub2api-cross psql -U postgres -l
```

停止条件：

- SQL gzip 完整性失败。
- `psql -v ON_ERROR_STOP=1` 返回非 0。
- 临时数据库无法启动。

## 10. Redis RDB 校验

```bash
RDB_FILE="$(sudo find "$RESTORE_ROOT/extract" -name dump.rdb -type f | head -1)"
docker run --rm -v "$(dirname "$RDB_FILE"):/data:ro" redis:8-alpine \
  redis-check-rdb /data/dump.rdb
```

停止条件：

- 找不到 `dump.rdb`。
- `redis-check-rdb` 未返回 `Checksum OK`。

## 11. 隔离应用启动验证

这一阶段只在临时 Docker 网络内启动应用，不发布宿主机端口。

建议顺序：

1. 创建临时 Docker network。
2. 使用恢复后的 Postgres / Redis 临时容器。
3. 按服务准备临时 `.env`，指向临时数据库和 Redis。
4. 启动应用容器。
5. 使用 `docker exec` 或临时 curl 容器访问 `/health` 或首页。

验证目标：

| 服务 | 验证 |
| --- | --- |
| resume-jadeai | 首页返回 200/30x |
| account-vault-web | `/health` 返回 200 |
| sub2api | `/health` 返回 200 |

禁止事项：

- 不连接生产数据库。
- 不发布临时容器到 `0.0.0.0`。
- 不使用生产域名指向临时容器。
- 不执行写入类业务操作，除非有明确测试账号和回滚方案。

## 12. 完整接管步骤

仅在真实灾难恢复或维护窗口中执行。

### 12.1 安装配置与目录

从 configs 备份中恢复或重建以下路径：

| 路径 | 说明 |
| --- | --- |
| `/opt/ops` | 运维仓库 |
| `/opt/services` | 业务 compose 与应用目录 |
| `/etc/nginx` | Nginx 配置 |
| `/etc/ssl/cf/top` | Cloudflare Origin Certificate |
| `/etc/letsencrypt` | DNS-only 域名公开证书 |
| `/etc/observability` | Grafana、Alertmanager、exporter 环境文件 |
| `/etc/ops` | R2 备份环境文件 |

恢复前先备份临时机器现有配置：

```bash
sudo tar -czf "$RESTORE_ROOT/pre-restore-etc-nginx.tar.gz" /etc/nginx
```

### 12.2 启动服务

建议顺序：

1. Docker daemon。
2. Postgres / Redis。
3. 业务应用。
4. Nginx。
5. Observability stack。
6. Backup cron / R2 sync。

验证：

```bash
docker ps
sudo nginx -t
curl -fsS http://127.0.0.1:3000/api/health
curl -fsS http://127.0.0.1:9090/-/healthy
```

### 12.3 公网接管前验证

在 DNS 切换前，使用 `--resolve` 指向新机器 IP 验证：

```bash
NEW_IP="<new-server-public-ip>"

curl -k --resolve monitor.areasong.top:443:${NEW_IP} https://monitor.areasong.top/
curl -k --resolve resume.areasong.top:443:${NEW_IP} https://resume.areasong.top/
curl -k --resolve sorryiossearch.areasong.top:443:${NEW_IP} https://sorryiossearch.areasong.top/health
curl -k --resolve cpa.areasong.top:443:${NEW_IP} https://cpa.areasong.top/health
curl -k --resolve log.areasong.top:443:${NEW_IP} https://log.areasong.top/
```

停止条件：

- `nginx -t` 不通过。
- 核心业务健康检查失败。
- 证书与 Cloudflare Full strict 不匹配。
- 数据库或 Redis 容器异常重启。

## 13. DNS 切换

仅在接管时执行。

### Cloudflare 代理域名

涉及：

- `resume.areasong.top`
- `sorryiossearch.areasong.top`
- `monitor.areasong.top`

检查：

- 新机器 Nginx 使用 `/etc/ssl/cf/top/origin.pem` 和对应私钥。
- Cloudflare SSL/TLS 模式保持 Full strict。
- 源站 443 可访问。

切换：

- 将 A 记录源站 IP 改为新机器公网 IP。
- 保持代理开启。

### DNS-only 域名

涉及：

- `cpa.areasong.top`
- `log.areasong.top`

检查：

- 新机器上有对应 Let's Encrypt 证书，或先用维护窗口申请/恢复证书。
- 443 直连证书链有效。

切换：

- 将 A 记录改为新机器公网 IP。
- 保持 DNS-only，除非另有 Cloudflare 代理方案。

## 14. 回滚策略

如果原 LosAngeles 仍可用：

1. 将 DNS A 记录切回 `23.185.200.12`。
2. 停止新机器 Nginx 或移除公网 80/443。
3. 保留新机器恢复现场，禁止二次写入。
4. 对比日志和数据差异。

如果原 LosAngeles 不可用：

1. 保持新机器接管。
2. 标记原机器为故障状态。
3. 禁止同一服务双写。
4. 记录接管时间、RPO、RTO、缺失数据范围。

## 15. 验收清单

| 检查项 | 状态 |
| --- | --- |
| R2 对象可拉回 | 待演练 |
| `rclone check --size-only --one-way` 通过 | 待演练 |
| account-vault Postgres 恢复成功 | 待演练 |
| sub2api Postgres 恢复成功 | 待演练 |
| Redis RDB 校验成功 | 待演练 |
| configs 解包成功 | 待演练 |
| JadeAI volume 解包成功 | 待演练 |
| sub2api volume 解包成功 | 待演练 |
| resume-jadeai 隔离启动验证 | 待演练 |
| account-vault 隔离启动验证 | 待演练 |
| sub2api 隔离启动验证 | 待演练 |
| Nginx `--resolve` 验证 | 待演练 |
| Prometheus / Grafana / Alertmanager 启动 | 待演练 |
| DNS 切换方案确认 | 待演练 |
| 回滚方案确认 | 待演练 |

## 16. 演练记录模板

```markdown
## 跨机器恢复演练记录

- 日期：
- 操作人：
- 临时机器：
- 源备份前缀：
- 恢复点：
- 是否切公网 DNS：否 / 是

### 结果

- RPO：
- RTO：
- 总结：PASS / FAIL

### 验证项

| 检查项 | 结果 | 备注 |
| --- | --- | --- |
| R2 拉回 |  |  |
| Postgres 恢复 |  |  |
| Redis 校验 |  |  |
| 应用启动 |  |  |
| Nginx 验证 |  |  |
| 监控启动 |  |  |

### 问题与改进

- 
```
