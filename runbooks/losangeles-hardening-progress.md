# LosAngeles 生产服务器加固与规范化核查进度

更新时间：2026-07-04 20:55 BST
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
- Cloudflare R2 异地对象存储备份已接入；初次同步完成并验证远端 `losangeles/` 前缀下有 22 个对象、总大小约 86.178 MiB；R2 拉回恢复演练已通过；生命周期策略已配置为 `losangeles/` 前缀 90 天后删除对象。
- 服务目录规范化继续推进；`sub2api` 已完成迁移和旧目录清理；`account-vault` 已完成 build context 与 env_file 迁移；旧 `/root/JadeAI` 与 `/root/sorryiosSearch` 已确认无运行时依赖、归档并删除。
- Cloudflare / 证书策略台账已补齐控制台只读核对结果；DNS 代理状态、TTL、SSL/TLS 模式、WAF/安全规则、DDoS、缓存/重定向/转换/Workers 路由均已记录；Origin Certificate 创建人/轮换负责人已补齐；旧 `www.areasong.top` / Tunnel `hWin` 入口已由用户删除并记录为预留门户网站；LosAngeles provider/region/owner 台账已基于 RDAP、ASN、ipinfo、本机网络和虚拟化信息补齐；`/opt/ops` root-only 变更流程已固化到标准文档。
- Postgres / Redis exporter 已接入；SSH/Fail2ban/UFW/Nginx 安全日志指标、告警和 Grafana 面板已接入；Fail2ban Ban/Unban 明细日志已接入 Loki 并展示在 Grafana 安全面板；Fail2ban IP 归属增强日志已接入，可在 Grafana 查看封禁 IP 的国家代码、ASN、BGP 前缀和网络组织名；Alertmanager 邮件已增加 Grafana 入口、Loki 查询提示和更多诊断标签，Fail2ban 当前封禁告警已降噪；应用级 HTTP 健康检查已覆盖 resume-jadeai、account-vault、sub2api；第一批业务关键路径 Blackbox 探针已覆盖公开首页、登录页、认证状态 API 和健康 JSON；基于增强 Nginx 访问日志的业务服务级 4xx/5xx、慢请求和采集新鲜度指标已接入 Prometheus、Alertmanager 与 Grafana；Cloudflare Origin Certificate 本地文件级过期监控和 180/90/30/7 天分级提醒已接入；Alertmanager 邮件模板和分级路由已优化。

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
| 跨机器恢复演练预案 | 完成，实机演练待做 | 已新增 `runbooks/losangeles-cross-machine-restore-drill.md`，覆盖临时机器要求、R2 拉回、恢复点选择、configs/Postgres/Redis/volumes 恢复、隔离应用启动、完整接管、DNS 切换、回滚和验收清单；当前未开新机器执行实机演练。 |
| R2 生命周期策略 | 完成 | Cloudflare 控制台已配置 `losangeles-expire-after-90-days`，对 `losangeles/` 前缀对象 90 天后删除；默认 7 天中止未完成分片上传规则保留；记录见 `runbooks/losangeles-r2-lifecycle-policy-20260703.md`。 |
| Cloudflare / 证书策略台账 | 完成 | 已更新 `inventory/cloudflare-areasong-top.md`，记录 `areasong.top` NS、DNS 代理状态、TTL、源站证书、公网证书表现、SSL/TLS、WAF、安全规则、DDoS、缓存/重定向/转换/Workers 路由核对结果，并补齐 Origin Certificate 创建人/轮换负责人、180/90/30/7 天提醒策略；旧 `www.areasong.top` / Tunnel `hWin` 入口已由用户删除并记录为门户网站预留域名。 |
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

## 4. 未完成事项与未来增强项

本轮主线已完成；以下项目为未来增强项或需要外部条件的工作。

### P0

当前无 P0 未完成事项。

### P1

当前无 P1 未完成事项。

### P2

1. SSH 来源 IP 限制。
   当前 UFW 的 `22/tcp` 仍为 Anywhere。如果有固定出口 IP，应改为仅允许固定来源。

2. 应用级监控深化。
   第一批公开、只读关键路径 Blackbox 探针已完成；基于增强 Nginx 访问日志的业务服务级错误率和慢请求基础监控已完成；后续应继续补登录后任务指标、关键接口分位延迟和更细的数据库连接健康。

3. Cloudflare 治理元数据深化。
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
- 跨机器恢复演练预案已完成，但未开临时机器执行实机恢复；当前 R2 拉回恢复演练是在当前主机上完成。
- 未执行跨机器完整应用级接管验证；当前应用级恢复演练已在当前主机的隔离临时 Docker 网络内验证业务容器可读取恢复数据。
- 未读取或打印任何 `.env`、私钥、Grafana 密码文件或 `/opt/as_password` 内容；本次验证仅输出 Fail2ban 日志中的封禁 IP 及其公开网络归属信息。
- 业务关键路径探针仅覆盖公开、只读、无副作用端点；未访问登录后的订单、导出、任务等敏感业务路径。

## 6. 后续增强项

1. 有测试账号或应用配合后，继续补登录后任务指标、关键接口分位延迟和更细的应用内部业务指标。
2. 视告警噪声情况继续细化 Alertmanager 抑制策略和通知周期。
3. 规划并接入 `www.areasong.top` 门户网站，包括部署位置、DNS/证书策略、Nginx 配置、Cloudflare 代理/WAF/缓存策略和回滚方案。
4. 后续可按 `runbooks/losangeles-cross-machine-restore-drill.md` 在新机器或临时云主机上执行一次跨机器实机恢复演练。

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
