# LosAngeles 生产服务器加固与规范化核查进度

更新时间：2026-07-04 08:14 BST
服务器：LosAngeles
公网 IP：23.185.200.12
系统：Ubuntu 24.04
运维仓库：/opt/ops
远端仓库：git@github.com:AreaSong/ops.git

## 1. 核查结论

原进度报告的核心结论大体成立：LosAngeles 已完成第一轮生产加固、本机备份、基础可观测能力建设、Nginx 统一入口和 Grafana 基础入口。

本次核查后需要修正和补充的重点如下：

- `/opt/as_password` 明文密码文件已删除；用户已确认修改 `as` 密码。后续临时提权通过共享终端里的 `sudo -v` 授权，不再保留明文密码文件。
- 系统更新与重启维护窗口已完成；当前内核为 `6.8.0-134-generic`，`/var/run/reboot-required` 不存在；`apt` 待升级仅剩 `fwupd` 分阶段发布项。
- Alertmanager 已接入 QQ 邮箱通知；SMTP 授权码保存在 `/etc/observability/alertmanager-smtp-password`，不进入 Git。
- 备份恢复演练已完成：Postgres 临时容器导入、Redis RDB 校验、configs/volumes 解包验证均通过；记录见 `runbooks/losangeles-backup-restore-drill-20260703.md`。
- Cloudflare R2 异地对象存储备份已接入；初次同步完成并验证远端 `losangeles/` 前缀下有 22 个对象、总大小约 86.178 MiB；R2 拉回恢复演练已通过；生命周期策略已配置为 `losangeles/` 前缀 90 天后删除对象。
- 服务目录规范化继续推进；`sub2api` 已完成迁移和旧目录清理；`account-vault` 已完成 build context 与 env_file 迁移；旧 `/root/JadeAI` 与 `/root/sorryiosSearch` 已确认无运行时依赖、归档并删除。
- Cloudflare / 证书策略台账已补齐控制台只读核对结果；DNS 代理状态、TTL、SSL/TLS 模式、WAF/安全规则、DDoS、缓存/重定向/转换/Workers 路由均已记录；Origin Certificate 创建人/轮换负责人和 `www.areasong.top` / Tunnel `hWin` 用途负责人已补齐。
- Postgres / Redis exporter 已接入；SSH/Fail2ban/UFW/Nginx 安全日志指标、告警和 Grafana 面板已接入；应用级 HTTP 健康检查已覆盖 resume-jadeai、account-vault、sub2api；第一批业务关键路径 Blackbox 探针已覆盖公开首页、登录页、认证状态 API 和健康 JSON；Alertmanager 邮件模板和分级路由已优化。

## 2. 已核实完成

| 项目 | 状态 | 核查证据 |
| --- | --- | --- |
| 主机基础信息 | 完成 | hostname 为 `LosAngeles`；系统为 Ubuntu 24.04；`ens3` 地址为 `23.185.200.12/24`。 |
| inventory 台账 | 完成但仍有 unknown 字段 | `/opt/ops/inventory/servers.yaml`、`servers.md`、`services.yaml`、`ports.md` 均已有 LosAngeles 记录。 |
| `/opt/ops` 仓库 | 完成 | remote 为 `git@github.com:AreaSong/ops.git`；前序加固、备份、监控提交已存在，当前提交状态以 `git log -1 --oneline` 和 `git status --short` 为准。 |
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
| Cloudflare R2 异地备份 | 完成 | `sync-r2.sh` 已接入；`/etc/ops/r2-backup.env` 为 root-only；root crontab 每日 04:15 同步；远端已验证 22 个对象、86.178 MiB。 |
| R2 拉回恢复演练 | 完成 | 2026-07-03 完成非破坏性演练；从 R2 拉回 22 个对象，`rclone check --size-only --one-way` 通过；Postgres、Redis、configs、volumes 抽样恢复验证通过；记录见 `runbooks/losangeles-r2-restore-drill-20260703.md`。 |
| R2 生命周期策略 | 完成 | Cloudflare 控制台已配置 `losangeles-expire-after-90-days`，对 `losangeles/` 前缀对象 90 天后删除；默认 7 天中止未完成分片上传规则保留；记录见 `runbooks/losangeles-r2-lifecycle-policy-20260703.md`。 |
| Cloudflare / 证书策略台账 | 完成 | 已更新 `inventory/cloudflare-areasong-top.md`，记录 `areasong.top` NS、DNS 代理状态、TTL、源站证书、公网证书表现、SSL/TLS、WAF、安全规则、DDoS、缓存/重定向/转换/Workers 路由核对结果，并补齐 Origin Certificate 创建人/轮换负责人、180/90/30 天提醒策略、`www.areasong.top` / Tunnel `hWin` 用途负责人和保留状态。 |
| 备份与 Docker textfile metrics | 完成 | `/var/lib/node_exporter/textfile_collector/backup.prom`、`docker.prom`、`r2-backup.prom` 存在并持续更新。 |
| 监控栈 | 完成 | Prometheus、Grafana、Alertmanager、Loki、Promtail、Node Exporter、Blackbox Exporter 容器均 running。 |
| Prometheus targets | 完成 | `blackbox_https` 的 `monitor.areasong.top`、`log.areasong.top`，以及 `node`、`prometheus` targets 均为 up。 |
| Prometheus 基础告警规则 | 完成 | 已加载 BackupStale、R2BackupStale、HttpProbeFailed、SslCertExpiring、DockerContainerDown、HostDown、Disk/Memory/CPU 告警；Alertmanager 已通过 QQ 邮箱通知验证，并已补充模板化邮件与按业务/入口/备份/数据库/安全/严重级别分组的路由。 |
| Postgres / Redis exporter | 完成 | 已新增 sub2api PostgreSQL、account-vault PostgreSQL、sub2api Redis exporter；Prometheus 新增 `postgres`、`redis` jobs；Grafana 新增 `LosAngeles Datastores`。 |
| 安全日志指标与告警 | 完成 | 已新增 `write-security-metrics.sh`、`security.prom`、`security_log_alerts` 和 `LosAngeles Security Overview`，覆盖 SSH 失败/无效用户/成功登录、Fail2ban sshd、UFW 状态、Nginx 4xx/5xx。 |
| 应用级 HTTP 健康检查 | 完成 | 已新增 `blackbox_app_https`，覆盖 `resume.areasong.top/`、`sorryiossearch.areasong.top/health`、`cpa.areasong.top/health`；新增 `app_health_alerts` 和 `LosAngeles App Health`。 |
| 业务关键路径 Blackbox 探针 | 第一批完成 | 已新增公开、只读、无副作用探针：`resume-jadeai` 简历首页、`account-vault` 登录页和认证状态 API、`sub2api` 登录页和健康 JSON；新增 `business_probe_alerts`，并扩展 `LosAngeles App and Business Health`。 |
| JadeAI fingerprint 事件处置 | 完成 | 数据未丢失，根因为浏览器 fingerprint 匿名身份错位；已归属修正并记录 `runbooks/losangeles-jadeai-fingerprint-incident-20260703.md`。 |
| Grafana 基础 Dashboard | 完成 | 存在 `losangeles-host-overview.json` 和 `losangeles-services-backups.json`。 |
| Loki / Promtail 基础采集 | 完成 | Promtail 配置采集 `/var/log/nginx/*.log`、`/var/log/backup/*.log`、`/var/log/syslog`；Loki `/ready` 返回 200。 |

## 3. 部分完成或需要修正

| 项目 | 当前状态 | 说明 |
| --- | --- | --- |
| Git 使用模型 | 完成 | `/opt/ops` 保持 `root:root` 管理；需要变更时由用户在共享终端 `sudo -v` 授权，统一使用 `sudo git -C /opt/ops ...` 操作，完成后 `sudo -k`；不为 `as` 配置全局 `safe.directory`，也不放宽 root-only 备份脚本目录权限。 |
| 服务目录规范化 | 完成主要清理 | `sub2api` 已完成迁移和旧目录清理；`account-vault` 已迁移 build context 到 `/opt/services/account-vault/app`，env_file 到 `/etc/account-vault/account-vault.env`；旧 `/root/JadeAI` 和 `/root/sorryiosSearch` 已归档到本机备份并同步 R2，2026-07-04 确认无运行时依赖后删除。 |
| 证书策略统一 | 基础完成 | `monitor/resume/sorryiossearch` 使用 Cloudflare Origin Certificate；`log/cpa` 使用 Let's Encrypt；策略已记录在 `inventory/cloudflare-areasong-top.md`。 |
| Docker / 服务健康检查 | 部分深化 | Docker running 指标、部分容器 health、应用 HTTP 黑盒探测、第一批业务关键路径 Blackbox 探针、Postgres / Redis exporter 已存在；应用原生业务错误率仍未系统化。 |
| Grafana Dashboard | 部分深化 | 主机、HTTPS、TLS、Docker、Backup、Postgres、Redis、安全日志、Nginx 4xx/5xx、应用 HTTP 健康和业务关键路径探针已覆盖；应用原生业务错误率视图仍未完成。 |
| Cloudflare 配置台账 | 治理元数据基础完成，仍可深化 | 控制台只读核对已完成；Cloudflare Origin Certificate 创建人、用途、180/90/30 天提醒策略、轮换负责人，以及 `www.areasong.top` / Tunnel `hWin` 的用途、负责人和保留状态已补齐；仍可继续补实际提醒落地渠道和 Tunnel 后端应用细节。 |

## 4. 未完成事项

### P0

当前无 P0 未完成事项。

### P1

当前无 P1 未完成事项。

### P2

1. SSH 来源 IP 限制。
   当前 UFW 的 `22/tcp` 仍为 Anywhere。如果有固定出口 IP，应改为仅允许固定来源。

2. 应用级监控深化。
   第一批公开、只读关键路径 Blackbox 探针已完成；后续应继续补应用原生业务错误率、登录后任务指标、关键接口分位延迟和更细的数据库连接健康。

3. Cloudflare 治理元数据深化。
   Origin Certificate 创建人/用途/轮换负责人、180/90/30 天提醒策略，以及 `www.areasong.top` / Tunnel `hWin` 的用途、负责人和保留状态已补齐；后续可补实际提醒落地渠道和 Tunnel 后端应用细节。

### P3

1. 主机名规范化。
   当前主机名 `LosAngeles` 可用，但不完全符合 inventory 命名规范。

2. 独立数据盘。
   当前只有系统盘 `/dev/sda1`，无独立 `/data` 数据盘。

3. 云厂商 / region / private IP / owner 补齐。
   inventory 中 LosAngeles 仍有 provider、region、private_ip、owner 等 unknown 或空字段。

## 5. 本次未验证项

- 未实际执行 root/as 错误登录测试；SSH 结论基于 `sshd -T` 有效配置。
- 未执行 `git fetch` 或远端网络同步写入；Git 同步结论基于本地 `origin/main` 与 HEAD 一致。
- Cloudflare 控制台基础配置已由用户侧只读核对；未修改 Cloudflare 配置，未核查更细粒度的历史事件、审计日志和 Origin Certificate 控制台创建记录。
- 未测试跨机器恢复；当前 R2 拉回恢复演练是在当前主机上完成。
- 未执行完整应用级接管验证；当前恢复演练验证到数据导入、RDB 校验和文件解包层面。
- 未读取或打印任何 `.env`、私钥、Grafana 密码文件或 `/opt/as_password` 内容。
- 业务关键路径探针仅覆盖公开、只读、无副作用端点；未访问登录后的订单、导出、任务等敏感业务路径。

## 6. 推荐下一步

1. 继续补应用原生业务错误率、登录后任务指标和关键接口分位延迟。
2. 做一次应用级恢复演练，验证恢复数据可被业务容器启动读取。
3. 视告警噪声情况继续细化 Alertmanager 抑制策略和通知周期。
4. 补齐 Cloudflare Origin Certificate 提醒落地渠道和 Tunnel `hWin` 后端应用细节。
5. 复核 `/opt/ops` root-only Git 操作流程是否需要固化到更多标准文档。
