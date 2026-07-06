# LosAngeles 生产服务器加固与规范化核查进度

更新时间：2026-07-06 03:35 UTC
服务器：LosAngeles
公网 IP：23.185.200.12
系统：Ubuntu 24.04
运维仓库：/opt/ops
远端仓库：git@github.com:AreaSong/ops.git

## 1. 核查结论

原进度报告的核心结论大体成立：LosAngeles 已完成本轮生产加固、规范化、备份恢复、可观测、告警、Cloudflare/证书治理和运维流程主线；当前状态快照见 。

本次核查后需要修正和补充的重点如下：

- 当前状态快照已完成：`runbooks/losangeles-current-status.md` 记录完成项、入口、Dashboard、备份恢复位置、风险接受项、未来增强项和当前不要做的操作。

- `/opt/as_password` 明文密码文件已删除；用户已确认修改 `as` 密码。后续临时提权通过共享终端里的 `sudo -v` 授权，不再保留明文密码文件。
- 系统更新与重启维护窗口已完成；当前内核为 `6.8.0-134-generic`，`/var/run/reboot-required` 不存在；`apt` 待升级仅剩 `fwupd` 分阶段发布项。
- Alertmanager 已接入 QQ 邮箱通知；SMTP 授权码保存在 `/etc/observability/alertmanager-smtp-password`，不进入 Git。
- 备份恢复演练已完成：Postgres 临时容器导入、Redis RDB 校验、configs/volumes 解包验证均通过；记录见 `runbooks/losangeles-backup-restore-drill-20260703.md`。
- 应用级恢复演练已完成：JadeAI、account-vault、sub2api 均已用隔离临时容器验证恢复数据可被业务容器启动读取；记录见 `runbooks/losangeles-app-restore-drill-20260704.md`。
- Cloudflare R2 异地对象存储备份已接入；初次同步完成并验证远端 `losangeles/` 前缀下有 22 个对象、总大小约 86.178 MiB；R2 拉回恢复演练已通过；2026-07-06 已补做 R2 Postgres 隔离恢复演练，确认 `sub2api-postgres` 与 `account-vault-postgres-1` dump 可导入无网络临时 Postgres；生命周期策略已配置为 `losangeles/` 前缀 90 天后删除对象。
- 服务目录规范化继续推进；`sub2api` 已完成迁移和旧目录清理；`account-vault` 已完成 build context 与 env_file 迁移；旧 `/root/JadeAI` 与 `/root/sorryiosSearch` 已确认无运行时依赖、归档并删除。
- Cloudflare / 证书策略台账已补齐控制台只读核对结果；DNS 代理状态、TTL、SSL/TLS 模式、WAF/安全规则、DDoS、缓存/重定向/转换/Workers 路由均已记录；Origin Certificate 创建人/轮换负责人已补齐；旧 `www.areasong.top` / Tunnel `hWin` 入口已由用户删除并记录为预留门户网站；LosAngeles provider/region/owner 台账已基于 RDAP、ASN、ipinfo、本机网络和虚拟化信息补齐；`/opt/ops` root-only 变更流程已固化到标准文档。
- 云厂商控制台 D2 已完成用户侧人工核对：控制台为 `https://server.zgocloud.cc/`，实例名为 `LosAngeles`；主账号 MFA 已开启，绑定邮箱/手机号可用，主账号未共用，当前无 API Key；该厂商无安全组/云防火墙/网络规则、快照、审计与安全通知能力，已作为厂商能力限制记录；账单/到期治理按用户要求暂缓。
- Postgres / Redis exporter 已接入；SSH/Fail2ban/UFW/Nginx 安全日志指标、告警和 Grafana 面板已接入；Fail2ban Ban/Unban 明细日志已接入 Loki 并展示在 Grafana 安全面板；Fail2ban IP 归属增强日志已接入，可在 Grafana 查看封禁 IP 的国家代码、ASN、BGP 前缀和网络组织名；Alertmanager 邮件已增加 Grafana 入口、Loki 查询提示和更多诊断标签，Fail2ban 当前封禁告警已降噪；应用级 HTTP 健康检查已覆盖 resume-jadeai、account-vault、sub2api；第一批业务关键路径 Blackbox 探针已覆盖公开首页、登录页、认证状态 API 和健康 JSON；基于增强 Nginx 访问日志的业务服务级 4xx/5xx、慢请求和采集新鲜度指标已接入 Prometheus、Alertmanager 与 Grafana；Cloudflare Origin Certificate 本地文件级过期监控和 180/90/30/7 天分级提醒已接入；Alertmanager 邮件模板和分级路由已优化。`postgres-exporter-sub2api` 对 PostgreSQL 18 的 `checkpoints_timed` 查询日志噪声已在 C7 修复；C2g 已确认当前 `sub2api` 上游未发现独立 migration-only 命令或关闭启动自动 migration 的开关；C1c 已完成 Redis ACL 阶段 1 运行态收紧并修复 Redis 备份稳定性；C1d 已完成 Redis ACL `aclfile` 持久化。

## 2. 已核实完成

| 项目 | 状态 | 核查证据 |
| --- | --- | --- |
| 主机基础信息 | 完成 | hostname 为 `LosAngeles`；系统为 Ubuntu 24.04；`ens3` 地址为 `23.185.200.12/24`。 |
| inventory 台账 | 完成 | `/opt/ops/inventory/servers.yaml`、`servers.md`、`services.yaml`、`ports.md` 均已有 LosAngeles 记录；provider/region/owner 已按可核验证据补齐，主机级私网 IP 明确为无。 |
| `/opt/ops` 仓库 | 完成 | remote 为 `git@github.com:AreaSong/ops.git`；前序加固、备份、监控提交已存在，当前提交状态以 `git log -1 --oneline` 和 `git status --short` 为准。 |
| 当前状态快照 | 完成 | 已新增 `runbooks/losangeles-current-status.md`，作为本轮任务收尾锚点，记录完成项、关键入口、监控面板、备份恢复、风险接受项和未来增强项。 |
| 系统更新与重启维护 | 完成 | 2026-07-03 已执行 `apt-get dist-upgrade` 并重启；升级日志为 `/var/log/ops/maintenance-20260703-apt-upgrade.log`；重启后内核为 `6.8.0-134-generic`，核心入口、监控、Docker、R2 dry-run 均通过。 |
| 非 root sudo 用户 | 完成 | `as` 属于 `sudo` 组，当前 sudo 缓存可用。 |
| SSH 加固 | 完成 | `sshd -T` 显示 `permitrootlogin no`、`passwordauthentication no`、`pubkeyauthentication yes`、`kbdinteractiveauthentication no`。 |
| Fail2ban | 完成 | `fail2ban` enabled/active；`sshd` jail active，当前有封禁记录。 |
| UFW | 完成 | UFW active；默认 deny incoming、allow outgoing；放行 22/80/443。 |
| 公网端口收敛 | 完成 | `ss` 显示公网监听为 22/80/443；x-ui、xray、Grafana、Prometheus、Alertmanager、Loki 均为本机或 Docker 网络内监听。 |
| Nginx 配置 | 完成 | `nginx -t` 通过；`log.areasong.top`、`monitor.areasong.top` 均存在 server 配置和反代。 |
| `log.areasong.top` 入口 | 完成 | `/` 返回 200；`/sub/` 无 token 返回 404；`/as` 无 WebSocket upgrade 返回 400。 |
| `monitor.areasong.top` 入口 | 完成 | HTTPS 入口返回 302 到 Grafana 登录；本地 Grafana `/api/health` 返回 200。 |
| Cloudflare Origin Certificate | 完成 | `/etc/ssl/cf/top/origin.pem` 和 `.key` 存在；证书 SAN 为 `*.areasong.top`、`areasong.top`。 |
| x-ui / xray 本机化 | 完成 | x-ui active/enabled；`127.0.0.1:46585`、`127.0.0.1:2096`、`127.0.0.1:10000` 均仅本机监听。 |
| 本机备份 | 完成 | root crontab 已配置 Postgres、Redis、configs、volumes 定时备份；`/var/backups/ops` 有 2026-07-03 最新产物。 |
| 备份完整性抽检 | 完成 | 最新 `.sql.gz` 通过 `gzip -t`；最新 `.tar.gz` 通过 `tar -tzf`。 |
| 备份恢复演练 | 完成 | 2026-07-03 完成非破坏性演练；Postgres 临时导入、Redis RDB 校验、configs/volumes 解包均通过；记录见 `runbooks/losangeles-backup-restore-drill-20260703.md`。 |
| 应用级恢复演练 | 完成 | 2026-07-04 完成非破坏性演练；JadeAI 临时容器读取恢复数据返回 307；account-vault 临时 Postgres 恢复后 web `/health` 返回 200；sub2api 临时 Postgres、Redis、数据目录恢复后 `/health` 返回 200；记录见 `runbooks/losangeles-app-restore-drill-20260704.md`。 |
| Cloudflare R2 异地备份 | 完成 | `sync-r2.sh` 已接入；`/etc/ops/r2-backup.env` 为 root-only；root crontab 每日 04:15 同步；远端已验证 22 个对象、86.178 MiB。 |
| R2 拉回恢复演练 | 完成 | 2026-07-03 完成非破坏性演练；从 R2 拉回 22 个对象，`rclone check --size-only --one-way` 通过；Postgres、Redis、configs、volumes 抽样恢复验证通过；记录见 `runbooks/losangeles-r2-restore-drill-20260703.md`。 |
| R2 Postgres 隔离恢复演练 | 完成 | 2026-07-06 完成非破坏性演练；从 R2 拉回 `sub2api-postgres` 与 `account-vault-postgres-1` dump，导入 `--network none` 临时 Postgres 容器并完成元数据级验证；记录见 `runbooks/losangeles-standards-09-c1h-postgres-isolated-restore-drill-20260706.md`。 |
| 跨机器恢复演练预案 | 完成，实机演练待做 | 已新增 `runbooks/losangeles-cross-machine-restore-drill.md`，覆盖临时机器要求、R2 拉回、恢复点选择、configs/Postgres/Redis/volumes 恢复、隔离应用启动、完整接管、DNS 切换、回滚和验收清单；当前未开新机器执行实机演练。 |
| R2 生命周期策略 | 完成 | Cloudflare 控制台已配置 `losangeles-expire-after-90-days`，对 `losangeles/` 前缀对象 90 天后删除；默认 7 天中止未完成分片上传规则保留；记录见 `runbooks/losangeles-r2-lifecycle-policy-20260703.md`。 |
| Cloudflare / 证书策略台账 | 完成 | 已更新 `inventory/cloudflare-areasong-top.md`，记录 `areasong.top` NS、DNS 代理状态、TTL、源站证书、公网证书表现、SSL/TLS、WAF、安全规则、DDoS、缓存/重定向/转换/Workers 路由核对结果，并补齐 Origin Certificate 创建人/轮换负责人、180/90/30/7 天提醒策略；旧 `www.areasong.top` / Tunnel `hWin` 入口已由用户删除并记录为门户网站预留域名。 |
| 云厂商控制台治理 | 完成核对，部分风险接受 | 用户已在 `https://server.zgocloud.cc/` 人工核对：实例名 `LosAngeles`；MFA 已开启；邮箱/手机号可用；主账号未共用；无 API Key。厂商无安全组/云防火墙/网络规则、快照、审计与安全通知能力，已记录为能力限制并以 UFW、Fail2ban、备份/R2、Loki/Grafana/Alertmanager 补偿；账单/到期治理按用户要求暂缓。 |
| Cloudflare Origin Certificate 本地监控 | 完成 | 已新增 `write-cloudflare-origin-cert-metrics.sh`、`cloudflare-origin-cert.prom`、`ops-cloudflare-origin-cert-metrics` cron、`cloudflare_origin_cert_alerts`、Alertmanager 长周期提醒路由和 `LosAngeles Certificates and Cloudflare` Dashboard；覆盖证书文件读取失败、指标过期、180/90/30/7 天分级过期提醒。 |
| `www.areasong.top` / `hWin` 旧入口下线 | 完成 | 用户已在 Cloudflare 控制台删除旧 Access Application 和 Tunnel/Public Hostname；公网不再跳转 `areasong.cloudflareaccess.com`，当前返回 Cloudflare HTTP 530；LosAngeles 本机未发现 `cloudflared` 进程或服务，`www` 预留给后续门户网站。 |
| 备份与 Docker textfile metrics | 完成 | `/var/lib/node_exporter/textfile_collector/backup.prom`、`docker.prom`、`r2-backup.prom` 存在并持续更新。 |
| 监控栈 | 完成 | Prometheus、Grafana、Alertmanager、Loki、Promtail、Node Exporter、Blackbox Exporter 容器均 running。 |
| Prometheus targets | 完成 | `blackbox_https` 的 `monitor.areasong.top`、`log.areasong.top`，以及 `node`、`prometheus` targets 均为 up。 |
| Prometheus 基础告警规则 | 完成 | 已加载 BackupStale、R2BackupStale、HttpProbeFailed、SslCertExpiring、DockerContainerDown、HostDown、Disk/Memory/CPU 告警；Alertmanager 已通过 QQ 邮箱通知验证，并已补充模板化邮件与按业务/入口/备份/数据库/安全/严重级别分组的路由。 |
| Alertmanager 邮件可读性与降噪 | 完成 | 邮件正文已增加推荐检查区、Grafana 安全/应用/备份/数据库/主机 Dashboard 入口、告警专属 `grafana_url` 与 `loki_query` 展示；告警表格补充 jail/job/container/status class 等诊断列；`Fail2banSshdCurrentlyBanning` 改为持续 15 分钟才触发，并通过专门路由降低重复通知频率。 |
| Postgres / Redis exporter | 完成 | 已新增 sub2api PostgreSQL、account-vault PostgreSQL、sub2api Redis exporter；Prometheus 新增 `postgres`、`redis` jobs；Grafana 新增 `LosAngeles Datastores`。 |
| 安全日志指标与告警 | 完成 | 已新增 `write-security-metrics.sh`、`security.prom`、`security_log_alerts` 和 `LosAngeles Security Overview`，覆盖 SSH 失败/无效用户/成功登录、Fail2ban sshd、UFW 状态、Nginx 4xx/5xx；Promtail 已采集 `/var/log/fail2ban.log`，Grafana 安全面板新增 `Recent Fail2ban Ban / Unban events` 明细日志表，可直接查看封禁/解封 IP；新增 `write-fail2ban-enriched-log.py` 和 `Fail2ban enriched IP intelligence` 面板，可查看国家代码、ASN、BGP 前缀和网络组织名。 |
| 应用级 HTTP 健康检查 | 完成 | 已新增 `blackbox_app_https`，覆盖 `resume.areasong.top/`、`sorryiossearch.areasong.top/health`、`cpa.areasong.top/health`；新增 `app_health_alerts` 和 `LosAngeles App Health`。 |
| 业务关键路径 Blackbox 探针 | 第一批完成 | 已新增公开、只读、无副作用探针：`resume-jadeai` 简历首页、`account-vault` 登录页和认证状态 API、`sub2api` 登录页和健康 JSON；新增 `business_probe_alerts`，并扩展 `LosAngeles App and Business Health`。 |
| 业务访问日志错误率与慢请求监控 | 基础完成 | 已新增 `/etc/nginx/conf.d/00-ops-business-log.conf` 生成增强访问日志 `/var/log/nginx/ops-business-access.log`，保留原默认 access log；新增 `write-business-log-metrics.sh`、`business-log.prom`、`ops-business-log-metrics` cron、`business_log_alerts`，并扩展 `LosAngeles App and Business Health` Dashboard，按 `resume-jadeai`、`account-vault`、`sub2api` 查看真实请求 4xx/5xx、慢请求和采集新鲜度。 |
| JadeAI fingerprint 事件处置 | 完成 | 数据未丢失，根因为浏览器 fingerprint 匿名身份错位；已归属修正并记录 `runbooks/losangeles-jadeai-fingerprint-incident-20260703.md`。 |
| Grafana 基础 Dashboard | 完成 | 存在 `losangeles-host-overview.json` 和 `losangeles-services-backups.json`。 |
| Loki / Promtail 基础采集 | 完成 | Promtail 配置采集 `/var/log/nginx/*.log`、`/var/log/backup/*.log`、`/var/log/syslog`；Loki `/ready` 返回 200。 |

## 3. 部分完成或需要修正

| 项目 | 当前状态 | 说明 |
| --- | --- | --- |
| Git 使用模型 | 完成 | `/opt/ops` 保持 `root:root` 管理；需要变更时由用户在共享终端临时 sudo 授权，统一使用 `sudo git -C /opt/ops ...` 操作，完成后 `sudo -k`；不为 `as` 配置全局 `safe.directory`，也不放宽 root-only 备份脚本目录权限；流程已写入 `standards/05-change-management.md`。 |
| 服务目录规范化 | 完成主要清理 | `sub2api` 已完成迁移和旧目录清理；`account-vault` 已迁移 build context 到 `/opt/services/account-vault/app`，env_file 到 `/etc/account-vault/account-vault.env`；旧 `/root/JadeAI` 和 `/root/sorryiosSearch` 已归档到本机备份并同步 R2，2026-07-04 确认无运行时依赖后删除。 |
| 证书策略统一 | 基础完成 | `monitor/resume/sorryiossearch` 使用 Cloudflare Origin Certificate；`log/cpa` 使用 Let's Encrypt；策略已记录在 `inventory/cloudflare-areasong-top.md`。 |
| Docker / 服务健康检查 | 部分深化 | Docker running 指标、部分容器 health、应用 HTTP 黑盒探测、第一批业务关键路径 Blackbox 探针、Postgres / Redis exporter、基于 Nginx 增强访问日志的业务服务级 4xx/5xx 与慢请求指标已存在；登录后任务指标和关键接口分位延迟仍可继续深化。 |
| Grafana Dashboard | 部分深化 | 主机、HTTPS、TLS、Docker、Backup、Postgres、Redis、安全日志、Nginx 4xx/5xx、应用 HTTP 健康、业务关键路径探针、业务服务级真实请求 4xx/5xx 与慢请求已覆盖；登录后业务任务和关键接口分位延迟视图仍可继续深化。 |
| Cloudflare 配置台账 | 治理元数据基础完成，仍可深化 | 控制台只读核对已完成；Cloudflare Origin Certificate 创建人、用途、180/90/30/7 天提醒策略和轮换负责人已补齐；旧 `www.areasong.top` / Tunnel `hWin` 入口已下线，后续需补门户网站接入方案。 |
| 云厂商账单 / 到期治理 | 暂缓 | 用户本轮明确先不处理；后续应单独补自动续费、到期提醒、余额/扣费失败提醒。 |

## 4. 未完成事项与未来增强项

本轮主线已完成；以下项目为未来增强项或需要外部条件的工作。

### P0

当前无 P0 未完成事项。

### P1

当前无 P1 未完成事项。

### P2

1. SSH 来源 IP 限制。
   当前 UFW 的 `22/tcp` 仍为 Anywhere。如果有固定出口 IP，应改为仅允许固定来源。

2. 云厂商账单 / 到期治理。
   本轮按用户要求暂缓；后续建议单独确认自动续费、到期提醒、余额/扣费失败通知。

3. 应用级监控深化。
   第一批公开、只读关键路径 Blackbox 探针已完成；基于增强 Nginx 访问日志的业务服务级错误率和慢请求基础监控已完成；后续应继续补登录后任务指标、关键接口分位延迟和更细的数据库连接健康。

4. Cloudflare 治理元数据深化。
   Origin Certificate 创建人/用途/轮换负责人、180/90/30/7 天提醒策略已补齐；旧 `www.areasong.top` / Tunnel `hWin` 入口已下线并预留门户网站；后续可补门户网站接入方案。

### P3

1. 主机名规范化。
   当前主机名 `LosAngeles` 可用，但不完全符合 inventory 命名规范。

2. 独立数据盘。
   当前只有系统盘 `/dev/sda1`，无独立 `/data` 数据盘。


## 5. 本次未验证项

- 未实际执行 root/as 错误登录测试；SSH 结论基于 `sshd -T` 有效配置。
- 未执行 `git fetch` 或远端网络同步写入；Git 同步结论基于本地 `origin/main` 与 HEAD 一致。
- Cloudflare 控制台基础配置已由用户侧只读核对；未修改 Cloudflare 配置，未核查更细粒度的历史事件、审计日志和 Origin Certificate 控制台创建记录。
- 云厂商控制台 D2 结论来自用户人工核对反馈；未登录或修改云厂商控制台。账单/到期治理按用户要求暂缓。
- 跨机器恢复演练预案已完成，但未开临时机器执行实机恢复；当前 R2 拉回恢复演练是在当前主机上完成。
- 未执行跨机器完整应用级接管验证；当前应用级恢复演练已在当前主机的隔离临时 Docker 网络内验证业务容器可读取恢复数据。
- 未读取或打印任何 `.env`、私钥、Grafana 密码文件或 `/opt/as_password` 内容；本次验证仅输出 Fail2ban 日志中的封禁 IP 及其公开网络归属信息。
- 业务关键路径探针仅覆盖公开、只读、无副作用端点；未访问登录后的订单、导出、任务等敏感业务路径。

## 6. 后续增强项

1. 单独补云厂商账单、到期、欠费提醒。
2. 有测试账号或应用配合后，继续补登录后任务指标、关键接口分位延迟和更细的应用内部业务指标。
3. 视告警噪声情况继续细化 Alertmanager 抑制策略和通知周期。
4. 规划并接入 `www.areasong.top` 门户网站，包括部署位置、DNS/证书策略、Nginx 配置、Cloudflare 代理/WAF/缓存策略和回滚方案。
5. 后续可按 `runbooks/losangeles-cross-machine-restore-drill.md` 在新机器或临时云主机上执行一次跨机器实机恢复演练。

## 2026-07-05 C4 容器日志上限显式化

状态：完成。

已完成：

- 业务容器与监控容器 compose 显式配置 `json-file` 日志轮转。
- 当前运行容器已滚动重建或复核，运行时均为 `max-size=50m`、`max-file=5`。
- 业务入口、监控 ready、Prometheus targets、运行时 LogConfig 均已验证。

留痕：

- `runbooks/losangeles-standards-09-c4-container-logging-limits-20260705.md`

## 2026-07-05 C5 镜像 digest 固定

状态：完成。

已完成：

- 当前生产 compose 已去除 `latest` 镜像引用。
- 业务依赖镜像与监控栈镜像已固定到当前运行 digest。
- 本次未拉取新镜像、未升级版本、未重启业务容器。

留痕：

- `runbooks/losangeles-standards-09-c5-image-digest-pinning-20260705.md`

后续：

- 将 `/opt/services/*/compose.yml` 纳入受控配置仍建议作为后续治理项。

## 2026-07-05 C6a 容器 no-new-privileges

状态：完成。

已完成：

- 当前业务容器与监控容器已启用 `no-new-privileges:true`。
- 已滚动重建并验证运行时 `SecurityOpt` 生效。
- 业务入口、监控 ready、Prometheus targets 均已验证。

留痕：

- `runbooks/losangeles-standards-09-c6a-no-new-privileges-20260705.md`

后续：

- C6b 逐服务评估 `cap_drop`、`read_only`、非 root 用户，不建议批量一刀切。

## 2026-07-05 C6b 监控辅助容器 cap_drop

状态：完成。

已完成：

- `alertmanager`、`blackbox-exporter`、Postgres Exporter、Redis Exporter 已启用 `cap_drop: [ALL]`。
- 已滚动重建并验证运行时 `CapDrop=ALL`。
- Alertmanager ready 与 Prometheus targets 均已验证。

留痕：

- `runbooks/losangeles-standards-09-c6b-cap-drop-monitoring-helpers-20260705.md`

后续：

- `promtail`、`node-exporter`、Prometheus、Grafana、Loki 与业务容器需逐服务继续评估，不建议批量一刀切。

## 2026-07-05 G1 业务 Compose 受控副本

状态：完成。

已完成：

- 将 `/opt/services/sub2api/compose.yml`、`/opt/services/account-vault/compose.yml`、`/opt/services/resume-jadeai/compose.yml` 同步到 `/opt/ops/services/`。
- 创建 `services/README.md` 说明运行配置与 Git 受控副本关系。
- 已确认未提交 `.env` 或明文密钥。

留痕：

- `services/README.md`
- `services/sub2api/compose.yml`
- `services/account-vault/compose.yml`
- `services/resume-jadeai/compose.yml`
- `runbooks/losangeles-standards-09-g1-service-compose-controlled-copies-20260705.md`

## 2026-07-05 R1 备份恢复演练

状态：完成。

已完成：

- 最新 Postgres dump 已恢复到临时 Postgres 容器验证。
- Redis、configs、业务 volume 备份包已完成 tar 可读性验证。
- 演练后生产入口与监控 ready 快速检查通过。

留痕：

- `runbooks/losangeles-standards-09-r1-backup-restore-drill-20260705.md`

后续：

- 可继续补 Redis 临时容器启动恢复、volume 临时应用回放、完整 RTO/RPO 计时。

## 2026-07-05 D2 云侧控制面治理

状态：控制台人工确认已完成；账号安全完成；厂商能力限制和账单暂缓项已记录。

已完成：

- 复核现有 Cloudflare / 证书 / R2 台账，确认 Cloudflare 基础治理已有留痕。
- 基于 LosAngeles inventory 当前 provider/region/owner 记录，新增云厂商控制台治理清单。
- 记录用户人工核对结果：控制台为 `https://server.zgocloud.cc/`，实例名为 `LosAngeles`；主账号 MFA 已开启，绑定邮箱/手机号可用，主账号未共用，当前无 API Key。
- 记录厂商能力限制：无安全组/云防火墙/网络规则、无快照、无审计与安全通知；补偿控制为 UFW、Fail2ban、端口收敛、备份/R2、Loki/Grafana/Alertmanager。
- 账单/到期治理按用户要求暂缓。

留痕：

- `runbooks/losangeles-standards-09-d2-cloud-control-plane-governance-20260705.md`

## 2026-07-05 A1 Nginx 安全响应头

状态：完成。

已完成：

- 全局启用 `server_tokens off`，源站不再暴露 Nginx 版本号。
- 新增 Nginx HSTS 与基础安全头 snippets。
- 对 `resume.areasong.top`、`sorryiossearch.areasong.top`、`log.areasong.top` 补齐 HSTS / nosniff / frame / referrer。
- 对 `cpa.areasong.top` 补 HSTS，保留应用侧现有 CSP / nosniff / frame / referrer。
- 对 `monitor.areasong.top` 补 HSTS / referrer，保留 Grafana 现有 nosniff / frame。
- `nginx -t` 通过，Nginx reload 成功，5 个 HTTPS 入口源站 header 与公网状态已验证。

留痕：

- `runbooks/losangeles-standards-09-a1-nginx-security-headers-20260705.md`

## 2026-07-05 C2e Postgres 角色权限只读复核

状态：完成。

已完成：

- 只读复核 `account-vault-postgres-1` 与 `sub2api-postgres` 角色权限位、数据库 owner、schema owner、app 角色授权摘要。
- 确认 `account-vault` 运行时使用低权限 `account_vault_app`。
- 确认 `sub2api_app` 低权限角色存在，且具备 74 张业务表 DML 权限，但当前业务容器仍使用 superuser `sub2api`。
- 结合 C2b 失败记录，确认 `sub2api` 不能直接强切到纯 DML 用户，原因是启动 migration / DDL 权限需求。

留痕：

- `runbooks/losangeles-standards-09-c2e-postgres-role-readonly-audit-20260705.md`

后续：

- `sub2api` 数据库权限治理需要应用侧配合拆分 migration 与 runtime；未确认前作为风险接受项。

## 2026-07-06 C1b Redis ACL / 高危命令兼容性分析

状态：分析完成；当时运行态不变；后续 C1c 已完成阶段 1 运行态收紧。

已完成：

- 只读核对当前 Redis ACL：默认用户仍为 `+@all`，key/channel 范围为全量；未记录密码或密码 hash。
- 只读核对 Redis 8 `@dangerous` 类别，确认它包含 `INFO`、`CONFIG GET`、`SLOWLOG GET`、`LATENCY`、`CLIENT LIST` 等监控/诊断命令，不能直接 `-@dangerous`。
- 基于 `sub2api` 上游源码确认未发现直接调用 `FLUSHALL`、`FLUSHDB`、`CONFIG GET/SET`、`SHUTDOWN`、`KEYS`。
- 确认 `sub2api` 依赖 `EVAL/EVALSHA`、`SCRIPT LOAD`、`SCAN`、`PUB/SUB`、hash/set/zset、pipeline 等能力，不能粗暴禁用。
- 核对 `redis_exporter v1.62.0`，确认默认会尝试 `CONFIG GET`、`INFO`、`CLIENT SETNAME`；如要禁 `CONFIG GET`，需先调整 exporter 配置并接受配置类指标减少。
- 确认当前 `sub2api` Redis 配置未发现 username 字段，短期不适合做分用户 ACL。
- 本次仅完成兼容性分析；后续 C1c 已实施阶段 1 运行态 ACL 收紧。

判断：

- C1c 实施时已根据备份依赖校正策略：保留 `SAVE/BGSAVE/BGREWRITEAOF` 和 `ACL SETUSER`；精确禁用 `FLUSHALL`、`FLUSHDB`、`SHUTDOWN`、`DEBUG`、`MONITOR`、`KEYS`、`CONFIG SET/REWRITE`、`REPLICAOF/SLAVEOF`、module load/unload 等。
- 不建议第一阶段禁用 `INFO`、`CONFIG GET`、`EVAL`、`SCAN`、`PUB/SUB`。
- 分用户 ACL 需要应用侧 Redis username 支持后再做。

留痕：

- `runbooks/losangeles-standards-09-c1b-redis-acl-compatibility-analysis-20260706.md`

## 2026-07-06 C2f sub2api migration/runtime 只读分析

状态：完成；无运行态变更。

已完成：

- 只读复核 `sub2api` 容器元数据、运行时数据库用户、Postgres schema / role / table privileges 和 C2b 历史失败信号。
- 确认当前业务容器仍使用 `DATABASE_USER=sub2api`，容器健康。
- 确认 `sub2api_app` 具备业务表 DML 与 sequence 权限，但对 `public` schema 只有 `USAGE`，没有 `CREATE`。
- 确认 `public.schema_migrations` 已存在，owner 为 `sub2api`，且 `sub2api_app` 已具备该表 DML 权限。
- 定位 C2b 直接切换失败的精确原因：应用启动时执行 `CREATE TABLE IF NOT EXISTS schema_migrations`，低权限用户缺少 schema `CREATE` 被拒绝。
- 本次未修改数据库权限、compose、容器或业务数据。

留痕：

- `runbooks/losangeles-standards-09-c2f-sub2api-migration-runtime-analysis-20260706.md`

后续：

- 先确认 `sub2api` 是否支持独立 migration 命令或关闭启动自动 migration。
- 确认前不要再次直接强切 `DATABASE_USER=sub2api_app`。
- 如考虑授予 schema `CREATE`，必须在维护窗口内明确接受运行用户 DDL 风险，并准备回滚。
- 旁支处理：`postgres-exporter-sub2api` 与 PostgreSQL 18 的 `checkpoints_timed` 查询日志噪声已在 C7 修复。

## 2026-07-06 C2g sub2api migration 能力分析

状态：完成；运行态不变；低权限 runtime 切换继续等待应用侧能力。

已完成：

- 只读核对 `sub2api` 上游源码 `b650bdd68d25bad3e502b2e34efe775555da2eba`。
- 确认 `backend/cmd/server/main.go` 仅提供 `--setup` 与 `--version`，未发现独立 migration-only 命令。
- 确认 `Dockerfile` / `deploy/docker-entrypoint.sh` 默认只启动 `/app/sub2api`，未提供 migration 分支。
- 确认正常启动链路为 `runMainServer -> initializeApplication -> repository.ProvideEnt -> InitEnt -> applyMigrationsFS`。
- 确认 `applyMigrationsFS` 每次启动都会执行 `CREATE TABLE IF NOT EXISTS schema_migrations`。
- 确认当前未发现关闭启动自动 migration 的配置项、环境变量或命令行参数。
- 本次未修改数据库权限、compose、容器或业务数据。

判断：

- 不能直接再次强切 `DATABASE_USER=sub2api_app`。
- 不建议长期授予 `sub2api_app` broad `public` schema `CREATE`。
- 如要达成 runtime 无 DDL，应优先在应用侧新增 `--migrate-only` 与关闭启动 migration 的能力，再安排维护窗口切换。

留痕：

- `runbooks/losangeles-standards-09-c2g-sub2api-migration-capability-analysis-20260706.md`

## 2026-07-06 C7 Postgres exporter PostgreSQL 18 兼容性修复

状态：完成。

已完成：

- 将两个 Postgres exporter 从 `v0.15.0` 升级到 `v0.19.1` 并固定 digest。
- 对 PostgreSQL 18.3 的 `postgres-exporter-sub2api` 设置 `--no-collector.stat_bgwriter` 和 `--collector.stat_checkpointer`。
- PostgreSQL 15.18 的 `postgres-exporter-account-vault` 保留默认 collector。
- 只重建两个 exporter 监控辅助容器，未重启业务容器或数据库。

验证：

- `docker compose config` 通过。
- 两个 Postgres exporter 均 running。
- `up{job="postgres"}` 两个实例均为 `1`。
- `pg_exporter_last_scrape_error{job="postgres"}` 两个实例均为 `0`。
- 新日志不再出现 `checkpoints_timed`、`stat_bgwriter` 或 `collector failed` 错误。

留痕：

- `runbooks/losangeles-standards-09-c7-postgres-exporter-pg18-compatibility-20260706.md`

## 2026-07-06 B3 fstab UUID 收敛

状态：完成；启动链路重启级验证待下次维护窗口或自然重启后补记。

已完成：

- `/`、`/boot`、`/boot/efi` 三个静态挂载项已从 `LABEL=` 切换为 `UUID=`。
- `findmnt --verify --verbose` 通过，结果为 `0 parse errors, 0 errors`。
- `mount -a` 通过。
- 已执行 `systemctl daemon-reload`。
- 回滚备份位于 `/root/ops-change-backups/standards09-fstab-uuid-20260705160144`。

留痕：

- `runbooks/losangeles-standards-09-b3-fstab-uuid-20260706.md`

## 2026-07-06 C3c 容器资源限制运行态复核

状态：完成。

已完成：

- 只读复核当前 16 个运行容器的内存、swap、CPU 限制和 compose 来源。
- 确认无 `Memory=0` 的运行容器。
- 确认业务容器、数据库/Redis 容器和监控栈容器均已有明确资源边界。

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


## 2026-07-06 C1c Redis ACL 阶段 1 实施

状态：完成；C1d 已完成持久化 `aclfile`。

已完成：

- Redis `default` 用户已精确禁用破坏性 / 高风险管理命令：`FLUSHALL`、`FLUSHDB`、`SHUTDOWN`、`DEBUG`、`MONITOR`、`KEYS`、`CLIENT KILL/PAUSE`、`CONFIG SET/REWRITE`、`REPLICAOF/SLAVEOF`、`MODULE LOAD/LOADEX/UNLOAD`。
- 保留监控、备份和业务依赖命令：`INFO`、`CONFIG GET`、`CLIENT LIST`、`BGSAVE`、`SAVE`、`BGREWRITEAOF`、`SCAN`、`EVAL`、`SCRIPT LOAD`、`PUB/SUB`。
- 修复 Redis 备份脚本：从在线打包 AOF 目录改为等待 `BGSAVE` 完成后打包稳定 `dump.rdb` 快照，仍输出 `redis-*.tar.gz`。
- 验证：ACL dry-run、Redis 备份、容器健康、`https://cpa.areasong.top/health`、近期日志权限错误检查均通过。

后续更新：C1d 已配置 `/data/users.acl`，Redis 重启后会加载持久化 ACL。

留痕：

- `runbooks/losangeles-standards-09-c1c-redis-acl-stage1-implementation-20260706.md`


## 2026-07-06 C1d Redis ACL 持久化实施

状态：完成。

已完成：

- 生成 root-only `/var/lib/sub2api/redis_data/users.acl`，保存当前 default 用户 ACL 规则和密码 hash；内容不进 Git、不打印。
- Redis compose 启动参数增加 `--aclfile /data/users.acl`。
- 运行副本 `/opt/services/sub2api/compose.yml` 与 Git 受控副本 `/opt/ops/services/sub2api/compose.yml` 已同步。
- 重建 `sub2api-redis` 后 Redis 为 `running healthy`。
- 验证 ACL deny/allow、Redis 备份、`sub2api` health、公开 `/health`、近期权限错误日志均通过。

留痕：

- `runbooks/losangeles-standards-09-c1d-redis-acl-persistence-20260706.md`


## 2026-07-06 C1e Redis ACL 备份覆盖

状态：完成。

已完成：

- Redis 备份脚本在 `users.acl` 存在时将其随 `dump.rdb` 一起纳入 `redis-*.tar.gz`。
- 备份 metadata 记录 `aclfile_included=yes/no`。
- 新生成的 Redis 备份包权限固定为 `0600`。
- 验证新备份包包含 `metadata.txt`、`redis_data/dump.rdb`、`redis_data/users.acl`。
- 验证备份指标刷新成功，Redis / sub2api 运行状态正常。

留痕：

- `runbooks/losangeles-standards-09-c1e-redis-acl-backup-coverage-20260706.md`


## 2026-07-06 C1f R2 异地备份复核

状态：完成。

已完成：

- 执行 R2 同步，确认最新 Redis 备份 `redis/redis-20260706-023215.tar.gz` 已在 R2。
- 验证远端对象大小和时间，不下载、不展开备份内容。
- 修复 `sync-r2.sh` 与 Cloudflare R2 的 rclone 兼容问题，增加 `--s3-no-head`，避免上传后 HEAD 501 误报。
- 清洁同步验证通过，日志无 `NotImplemented`、`status code: 501` 或 `ERROR`。
- 清理本轮诊断探针对象。
- R2 同步指标已刷新，Redis / sub2api 运行状态正常。

留痕：

- `runbooks/losangeles-standards-09-c1f-r2-backup-sync-verification-20260706.md`


## 2026-07-06 C1g R2 隔离恢复演练

状态：完成。

已完成：

- 从 R2 拉回 Redis、Postgres、configs、volumes 选定恢复点到 root-only 临时目录。
- 验证 Postgres `.sql.gz` 完整性。
- 验证 Redis / configs / volumes `.tar.gz` 完整性。
- 验证 Redis 备份包含 `redis_data/dump.rdb` 和 `redis_data/users.acl`。
- 使用临时 `--network none` Redis 容器加载备份数据和 ACL 文件。
- 验证临时 Redis `PING`、`DBSIZE=188`、`aclfile=/data/users.acl`。
- 验证临时 Redis `FLUSHALL` 被 ACL 拒绝。
- 演练后临时容器和临时目录已清理，生产 `sub2api-redis` / `sub2api` 仍为 `running healthy`。

留痕：

- `runbooks/losangeles-standards-09-c1g-r2-isolated-restore-drill-20260706.md`

## 2026-07-06 C1h Postgres 隔离恢复演练

状态：完成。

已完成：

- 从 R2 拉回 `postgres/sub2api-postgres-20260706-021001.sql.gz` 和 `postgres/account-vault-postgres-1-20260706-021001.sql.gz` 到 root-only 临时目录。
- 两个 `.sql.gz` 均通过 `gzip -t`。
- 使用生产同款 Postgres 镜像启动临时 `--network none` 容器。
- `sub2api-postgres` dump 导入成功，元数据计数为 `roles=19`、`databases=2`、`connectable_databases=2`、`total_relations=560`。
- `account-vault-postgres-1` dump 导入成功，元数据计数为 `roles=15`、`databases=2`、`connectable_databases=2`、`total_relations=422`。
- 演练发现官方 Postgres 镜像初始化存在临时 server 到最终 server 的切换窗口；脚本已改为等待初始化完成后再 `select 1`，避免 `pg_isready` 过早通过。
- 演练后临时容器和临时目录已清理，生产 `sub2api-postgres`、`account-vault-postgres-1`、`sub2api` 均为 `running healthy`。

留痕：

- `runbooks/losangeles-standards-09-c1h-postgres-isolated-restore-drill-20260706.md`
