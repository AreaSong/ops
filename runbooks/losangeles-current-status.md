# LosAngeles 当前运维状态快照

更新时间：2026-07-06 01:10 UTC
服务器：LosAngeles
公网 IP：23.185.200.12
系统：Ubuntu 24.04
运维仓库：/opt/ops
远端仓库：git@github.com:AreaSong/ops.git

## 1. 本轮结论

LosAngeles 本轮生产服务器加固、规范化、备份、恢复、监控、告警和 Cloudflare 治理主线已完成。

当前状态：

- P0 未完成项：无
- P1 未完成项：无
- 生产安全基线：完成
- 备份与异地备份：完成
- 本机恢复、R2 拉回恢复、应用级恢复：完成
- 跨机器恢复：预案完成，实机演练待临时机器
- 监控、告警、Dashboard：完成
- Cloudflare / 证书治理：完成
- 云厂商控制台治理：已核对，账号安全完成；部分能力为厂商不提供，已记录风险接受
- 运维仓库、台账、变更流程：完成
- 旧 `www.areasong.top` / `hWin` 入口：已下线，预留门户网站

## 2. 关键入口

| 项目 | 入口 / 路径 | 状态 |
| --- | --- | --- |
| SSH | `ssh -i ~/.ssh/id_ed25519_losangeles -o IdentitiesOnly=yes as@23.185.200.12` | 密钥登录 |
| 运维仓库 | `/opt/ops` | root-only 管理 |
| Grafana | `https://monitor.areasong.top/` | Cloudflare 代理 |
| x-ui / xray 入口 | `https://log.areasong.top/` | DNS-only，经 Nginx |
| resume-jadeai | `https://resume.areasong.top/` | Cloudflare 代理 |
| account-vault | `https://sorryiossearch.areasong.top/` | Cloudflare 代理 |
| sub2api | `https://cpa.areasong.top/` | DNS-only |
| www 门户 | `https://www.areasong.top/` | 旧入口已下线，当前预留 |

## 3. 安全基线

已完成：

- root SSH 登录禁用。
- SSH 密码登录禁用。
- 仅允许密钥登录。
- UFW 默认拒绝入站，仅放行 22/80/443。
- Fail2ban sshd jail 已启用。
- 公网服务端口收敛到 22/80/443。
- x-ui、xray、Grafana、Prometheus、Alertmanager、Loki 等均为本机或 Docker 网络内监听。
- `/opt/as_password` 明文密码文件已删除。
- 敏感凭据不进入 Git。

风险接受：

- SSH 22 当前仍为 Anywhere，因为当前没有固定出口 IP。没有固定出口 IP 前不建议限制来源，避免锁在服务器外。

## 4. 备份与恢复

备份：

- 本机备份目录：`/var/backups/ops`
- 本机备份脚本：`/opt/ops/scripts/backup/`
- R2 Bucket：`losangeles-ops-backups`
- R2 Prefix：`losangeles/`
- R2 配置：`/etc/ops/r2-backup.env`，root-only，不进 Git
- R2 同步脚本：`/opt/ops/scripts/backup/sync-r2.sh`
- R2 生命周期：`losangeles/` 前缀 90 天后删除对象

已完成演练：

- 本机备份恢复演练：`runbooks/losangeles-backup-restore-drill-20260703.md`
- R2 拉回恢复演练：`runbooks/losangeles-r2-restore-drill-20260703.md`
- 应用级恢复演练：`runbooks/losangeles-app-restore-drill-20260704.md`
- 跨机器恢复预案：`runbooks/losangeles-cross-machine-restore-drill.md`

仍未做：

- 跨机器实机恢复演练。需要临时新机器或维护窗口。

## 5. 监控与告警

监控栈：

- Prometheus
- Grafana
- Alertmanager
- Loki
- Promtail
- Node Exporter
- Blackbox Exporter
- Postgres exporter
- Redis exporter

告警通知：

- Alertmanager 已接入 QQ 邮箱。
- SMTP 授权码位于 `/etc/observability/alertmanager-smtp-password`，不进 Git。
- 告警邮件已包含 Grafana 入口、Loki 查询提示和更多诊断标签。

Grafana Dashboard：

- `LosAngeles Host Overview`
- `LosAngeles Services and Backups`
- `LosAngeles Datastores`
- `LosAngeles Security Overview`
- `LosAngeles App and Business Health`
- `LosAngeles Certificates and Cloudflare`

已覆盖：

- 主机 CPU / 内存 / 磁盘 / 网络
- Docker 容器运行状态
- 本机备份与 R2 备份新鲜度
- HTTPS 探测与公网证书临期
- Cloudflare Origin Certificate 本地证书临期
- Nginx 源站安全响应头与 `server_tokens off`
- Postgres / Redis 基础指标
- SSH / Fail2ban / UFW / Nginx 安全日志指标
- Fail2ban 封禁明细与 IP 归属增强
- 应用 HTTP 健康检查
- 业务关键路径 Blackbox 探针
- 业务访问日志 4xx / 5xx / 慢请求

## 6. Cloudflare 与证书

台账：

- `inventory/cloudflare-areasong-top.md`

当前策略：

- `resume.areasong.top`、`sorryiossearch.areasong.top`、`monitor.areasong.top` 使用 Cloudflare 代理和 Origin Certificate。
- `cpa.areasong.top`、`log.areasong.top` 为 DNS-only，使用 Let's Encrypt。
- `www.areasong.top` 旧 Access / Tunnel 入口已下线，预留后续门户网站。

证书监控：

- Origin Certificate 本地文件监控已接入。
- 180 / 90 / 30 / 7 天分级提醒已落地到 Prometheus / Alertmanager / Grafana。

## 7. 台账与变更流程

已完成：

- `inventory/servers.yaml`、`servers.md`、`services.yaml`、`ports.md` 已同步。
- LosAngeles provider / region / owner 已按可核验证据补齐。
- 云厂商控制台 `https://server.zgocloud.cc/` 已人工核对，实例名称为 `LosAngeles`。
- 云主账号 MFA 已开启，绑定邮箱和手机号可用，主账号未共用，当前无 API Key。
- 云厂商无安全组/云防火墙/网络规则、快照、审计与安全通知能力；已作为厂商能力限制记录。
- 主机级私网 IP 明确为无。
- `/opt/ops` root-only 变更流程已写入 `standards/05-change-management.md`。
- 与 AI 运维助手配合使用的指令速查已写入 `standards/09-server-ops-handbook.md`。

执行约束：

- 不放宽 `/opt/ops` 权限。
- 不把 `/opt/ops` 加入普通用户全局 `safe.directory`。
- 需要 sudo 时通过共享终端临时授权。
- 变更后提交并推送到 GitHub。

## 8. 当前风险接受项

| 项目 | 当前状态 | 说明 |
| --- | --- | --- |
| SSH 来源 IP 限制 | 暂不启用 | 没有固定出口 IP，强行限制可能锁定自己 |
| 云厂商安全组 / 云防火墙 | 厂商不支持 | 已由控制台人工确认；以 UFW、Fail2ban、端口收敛和监控告警补偿 |
| 云厂商快照 | 当前无 | 以本机备份、R2 异地备份、本机/R2/应用级恢复演练补偿 |
| 云厂商审计 / 安全通知 | 厂商不支持 | 以主机日志、Loki、Grafana、Alertmanager 和 Cloudflare 侧能力补偿 |
| 账单 / 到期治理 | 暂缓 | 用户本轮明确先不处理 |
| 登录后业务指标 | 暂未做 | 需要测试账号或应用侧指标配合 |
| Redis 高危命令 / ACL | 方案完成，待维护窗口实施 | C1b 已确认不能 `-@dangerous` 一刀切；建议第一阶段精确禁用破坏性命令，分用户 ACL 需应用侧 Redis username 支持 |
| p95 / p99 分位延迟 | 暂未做 | 当前有 Nginx request_time 最大值和慢请求数量；分位需要更细日志管道或应用 metrics |
| sub2api 数据库运行用户 | 风险接受 / 应用侧待配合 | 当前仍使用 superuser `sub2api`；C2f 已确认直接切换失败原因是启动时执行 `CREATE TABLE IF NOT EXISTS schema_migrations` 需要 `public` schema `CREATE` 权限；C2g 已确认当前上游未发现独立 migration-only 命令或关闭启动自动 migration 的开关 |
| 门户网站 | 暂未接入 | `www.areasong.top` 已预留，用户暂不急 |
| 主机名规范化 | 暂不改 | `LosAngeles` 可用，改名有运维影响 |
| 独立数据盘 | 暂无 | 当前数据量可接受；后续增长后再规划 |
| 跨机器实机恢复 | 暂未执行 | 预案已完成，需要临时机器 |

## 9. 后续增强项

按推荐优先级：

1. 后续单独处理账单、到期、欠费提醒。
2. 有固定出口 IP 后，限制 SSH 来源。
3. 有测试账号或应用配合后，补登录后业务指标和关键接口分位延迟。
4. 准备门户网站时，接入 `www.areasong.top`。
5. 有临时机器时，按跨机器恢复 Runbook 做一次实机演练。
6. 业务数据增长后，规划独立数据盘和数据迁移。
7. 有维护窗口时，再评估主机名规范化。

## 10. 当前不要做

- 不要在没有固定出口 IP 时限制 SSH 来源。
- 不要把 R2、SMTP、数据库、Grafana、证书私钥等凭据提交到 Git。
- 不要随手删除旧备份或 R2 对象。
- 不要把“云厂商无安全组/快照/审计能力”误判为服务器内配置漏项；当前补偿控制以主机侧和备份恢复体系为主。
- 不要在没有维护窗口时改主机名、迁移数据盘或切 DNS。
- 不要将临时恢复容器发布到公网。

## 11. 本轮完成判定

本轮 LosAngeles 服务器加固、规范化、备份恢复、可观测、告警、Cloudflare/证书治理和运维流程主线完成。

后续工作均按增强项、业务新需求或维护窗口另行启动。

## 12. standards/09 严格验收口径

已新增 `runbooks/losangeles-standards-09-audit.md`，按 `standards/09-server-ops-handbook.md` 附录 A 对 LosAngeles 做逐项验收。

严格口径下，本轮主线完成不等于 `standards/09` 所有理想企业架构项均 100% 完成。当前仍有三类项目需要单独标记：

- 风险接受：单机无 HA、SSH 来源 IP 暂不限制、无独立数据盘、主机名暂不规范化。
- 维护窗口 / 应用配合优化：Redis 高危命令 / ACL 收紧实施、sub2api migration/runtime 拆分实施；sub2api 失败原因已在 C2f 只读分析中定位，C2g 已确认当前上游未发现独立 migration-only 命令或关闭启动自动 migration 的开关；Redis 密码、maxmemory、AOF 和内网隔离已在 C1 复核完成，Redis ACL / 高危命令兼容性已在 C1b 分析完成，journald/logrotate/sysctl 与 Docker daemon 日志基线已在 B1/B2 完成，`fstab` UUID 已在 B3 完成。
- 云侧能力限制 / 暂缓：云厂商无安全组/云防火墙、快照、云审计/安全通知；账单/到期治理用户本轮暂缓。

后续优化以该矩阵为准，按低风险文档修正、低风险系统收敛、维护窗口变更、云侧治理四类分批推进。

## 2026-07-05 D2 云侧控制面治理

状态：控制台人工确认已完成；账号安全完成；厂商能力限制和账单暂缓项已记录。

已更新 `runbooks/losangeles-standards-09-d2-cloud-control-plane-governance-20260705.md`，将 Cloudflare 已完成项、服务器侧已确认事实、云厂商控制台确认结果、厂商能力限制和补偿控制分开管理。

已确认：控制台实例名为 `LosAngeles`；云主账号 MFA 已开启；绑定邮箱和手机号可用；主账号未共用；当前无 API Key。云厂商无安全组/云防火墙/网络规则、快照、审计与安全通知能力。账单/到期治理按用户要求暂缓。

## 2026-07-05 A1 Nginx 安全响应头

状态：完成。

已完成 `runbooks/losangeles-standards-09-a1-nginx-security-headers-20260705.md`：

- 全局启用 `server_tokens off`。
- 5 个 HTTPS 入口完成源站安全响应头基线核对与补齐。
- 本次未加全局 CSP，避免误伤应用资源；CSP 后续按应用单独治理。
- `nginx -t` 通过，Nginx reload 成功，公网入口快速检查通过。

## 2026-07-05 C2e Postgres 角色权限只读复核

状态：完成。

## 2026-07-06 B3 fstab UUID 收敛

状态：完成；启动链路重启级验证待下次维护窗口或自然重启后补记。

已完成：

- `/`、`/boot`、`/boot/efi` 三个静态挂载项已从 `LABEL=` 切换为 `UUID=`。
- `findmnt --verify --verbose` 通过，结果为 `0 parse errors, 0 errors`。
- `mount -a` 通过。
- 已执行 `systemctl daemon-reload`。

留痕：

- `runbooks/losangeles-standards-09-b3-fstab-uuid-20260706.md`

## 2026-07-06 C3c 容器资源限制运行态复核

状态：完成。

已完成：

- 只读复核当前 16 个运行容器的 `HostConfig.Memory`、CPU、swap 和 compose 来源。
- 确认当前无 `Memory=0` 的运行容器。
- 确认 C3a 业务容器资源限制和 C3b 监控栈资源限制在运行态均已生效。

留痕：

- `runbooks/losangeles-standards-09-c3c-container-runtime-resource-limit-audit-20260706.md`

## 2026-07-06 C1 Redis 策略只读复核

状态：密码、maxmemory、持久化、内网隔离完成；高危命令 / ACL 策略待决策。

已完成：

- Redis 未发布公网端口，宿主机无公网 `6379` 监听。
- Redis `requirepass` 已设置，密码内容未打印。
- Redis `maxmemory=512MiB`，`maxmemory-policy=noeviction`。
- Redis `appendonly=yes`。
- `sub2api` 和 `redis-exporter-sub2api` 均已配置 Redis 认证。

后续：

- 是否限制 `FLUSHALL`、`FLUSHDB`、`CONFIG`、`SHUTDOWN` 等高危命令，需要单独维护窗口和应用兼容性验证。

留痕：

- `runbooks/losangeles-standards-09-c1-redis-policy-readonly-audit-20260706.md`

已完成 `runbooks/losangeles-standards-09-c2e-postgres-role-readonly-audit-20260705.md`：

- `account-vault` 已确认使用低权限 `account_vault_app`。
- `sub2api_app` 低权限角色已存在，且具备当前业务表 DML 权限。
- `sub2api` 业务容器当前仍使用 superuser `sub2api`，原因是前序 C2b 低权限切换尝试因启动 migration / DDL 权限需求失败。
- 后续需应用侧配合拆分 migration 与 runtime，不能直接再次强切。

## 2026-07-06 C1b Redis ACL / 高危命令兼容性分析

状态：分析完成；运行态不变；ACL 收紧待维护窗口确认后实施。

已完成 `runbooks/losangeles-standards-09-c1b-redis-acl-compatibility-analysis-20260706.md`：

- 确认不能直接 `-@dangerous`，因为会误伤 `INFO`、`CONFIG GET`、`SLOWLOG`、`LATENCY` 等监控/诊断命令。
- 确认 `sub2api` 未发现直接使用 `FLUSHALL`、`FLUSHDB`、`CONFIG`、`SHUTDOWN`、`KEYS`。
- 确认 `sub2api` 依赖 Lua、`SCAN`、`PUB/SUB`、hash/set/zset、pipeline 等能力。
- 确认当前 `sub2api` Redis 配置未发现 username 字段，短期先不做分用户 ACL。
- 本次未修改 Redis ACL、配置或容器状态。

结论：第一阶段建议维护窗口内精确禁用破坏性命令；更严格的分用户 ACL 需要应用侧 Redis username 支持。

## 2026-07-06 C2f sub2api migration/runtime 只读分析

状态：只读分析完成；运行态不变；风险接受继续有效。

已完成：

- 确认 `sub2api` 容器当前仍为 `DATABASE_USER=sub2api`，容器健康。
- 确认 `sub2api_app` 具备业务表 DML 和 sequence 权限，但没有 `public` schema `CREATE`。
- 确认 `public.schema_migrations` 已存在，owner 为 `sub2api`。
- 结合 C2b 失败日志，定位直接切换失败的精确 SQL：应用启动时执行 `CREATE TABLE IF NOT EXISTS schema_migrations`，低权限用户因缺少 schema `CREATE` 被拒绝。
- 本次未修改数据库权限、compose、容器或业务数据。

后续：

- 先确认应用是否支持独立 migration 命令或关闭启动自动 migration。
- 确认前不要再次直接强切 `DATABASE_USER=sub2api_app`。
- 不建议为 `sub2api_app` 直接授予 broad `public` schema `CREATE`，除非在维护窗口内明确接受运行用户 DDL 风险。
- 旁支发现：`postgres-exporter-sub2api` 持续出现 `checkpoints_timed` 字段查询错误；已在 C7 修复 PostgreSQL 18 / exporter collector 兼容性。

留痕：

- `runbooks/losangeles-standards-09-c2f-sub2api-migration-runtime-analysis-20260706.md`

## 2026-07-06 C2g sub2api migration 能力分析

状态：完成；运行态不变；低权限 runtime 切换继续等待应用侧能力。

已完成 `runbooks/losangeles-standards-09-c2g-sub2api-migration-capability-analysis-20260706.md`：

- 只读核对上游源码 `b650bdd68d25bad3e502b2e34efe775555da2eba` 和当前生产镜像受控副本。
- 确认当前 CLI 仅有 `--setup`、`--version`，未发现 migration-only 命令。
- 确认启动链路会在 `InitEnt()` 中硬执行 `applyMigrationsFS()`。
- 确认未发现关闭启动自动 migration 的环境变量、配置项或命令行参数。
- 本次未修改运行配置、数据库权限、容器或业务数据。

结论：`sub2api` runtime 低权限切换不是简单权限补漏；需要应用侧支持独立 migration 和关闭启动自动 migration 后再进入维护窗口实施。确认前继续作为风险接受项。

## 2026-07-06 C7 Postgres exporter PostgreSQL 18 兼容性修复

状态：完成。

已完成：

- 将两个 Postgres exporter 升级到 `v0.19.1` 并固定 digest。
- 对 `postgres-exporter-sub2api` 禁用旧 `stat_bgwriter` collector，启用 PostgreSQL 18 对应的 `stat_checkpointer` collector。
- `account-vault` 仍连接 PostgreSQL 15.18，保留默认 collector。
- 仅重建两个 exporter 监控辅助容器，未重启业务容器或数据库。

验证：

- 两个 Postgres exporter 均 running。
- `up{job="postgres"}` 两个实例均为 `1`。
- `pg_exporter_last_scrape_error{job="postgres"}` 两个实例均为 `0`。
- 新日志不再出现 `checkpoints_timed` / `stat_bgwriter` 错误。

留痕：

- `runbooks/losangeles-standards-09-c7-postgres-exporter-pg18-compatibility-20260706.md`
