# LosAngeles 生产服务器加固与规范化核查进度

更新时间：2026-07-03 09:20 BST
服务器：LosAngeles  
公网 IP：23.185.200.12  
系统：Ubuntu 24.04  
运维仓库：/opt/ops  
远端仓库：git@github.com:AreaSong/ops.git

## 1. 核查结论

原进度报告的核心结论大体成立：LosAngeles 已完成第一轮生产加固、本机备份、基础可观测能力建设、Nginx 统一入口和 Grafana 基础入口。

本次核查后需要修正和补充的重点如下：

- `/opt/as_password` 明文密码文件已删除；仍建议用户尽快修改 `as` 密码。后续临时提权通过共享终端里的 `sudo -v` 授权，不再保留明文密码文件。
- 系统更新与重启维护窗口已完成；当前内核为 `6.8.0-134-generic`，`/var/run/reboot-required` 不存在；`apt` 待升级仅剩 `fwupd` 分阶段发布项。
- Alertmanager 已接入 QQ 邮箱通知；SMTP 授权码保存在 `/etc/observability/alertmanager-smtp-password`，不进入 Git。
- 备份恢复演练已完成：Postgres 临时容器导入、Redis RDB 校验、configs/volumes 解包验证均通过；记录见 `runbooks/losangeles-backup-restore-drill-20260703.md`。
- Cloudflare R2 异地对象存储备份已接入；初次同步完成并验证远端 `losangeles/` 前缀下有 22 个对象、总大小约 86.178 MiB。
- 服务目录规范化已部分推进到 `/opt/services`，但 `/root` 下仍有历史服务目录和 compose 文件。
- Cloudflare DNS/WAF/橙云灰云等控制台级台账未发现完整记录。
- Postgres / Redis exporter、SSH/Fail2ban/UFW/Nginx 安全日志告警、业务级健康检查仍未完成。

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
| 备份与 Docker textfile metrics | 完成 | `/var/lib/node_exporter/textfile_collector/backup.prom`、`docker.prom`、`r2-backup.prom` 存在并持续更新。 |
| 监控栈 | 完成 | Prometheus、Grafana、Alertmanager、Loki、Promtail、Node Exporter、Blackbox Exporter 容器均 running。 |
| Prometheus targets | 完成 | `blackbox_https` 的 `monitor.areasong.top`、`log.areasong.top`，以及 `node`、`prometheus` targets 均为 up。 |
| Prometheus 基础告警规则 | 完成 | 已加载 BackupStale、R2BackupStale、HttpProbeFailed、SslCertExpiring、DockerContainerDown、HostDown、Disk/Memory/CPU 告警；Alertmanager 已通过 QQ 邮箱通知验证。 |
| Grafana 基础 Dashboard | 完成 | 存在 `losangeles-host-overview.json` 和 `losangeles-services-backups.json`。 |
| Loki / Promtail 基础采集 | 完成 | Promtail 配置采集 `/var/log/nginx/*.log`、`/var/log/backup/*.log`、`/var/log/syslog`；Loki `/ready` 返回 200。 |

## 3. 部分完成或需要修正

| 项目 | 当前状态 | 说明 |
| --- | --- | --- |
| Git 使用模型 | 部分完成 | 仓库由 root 拥有，`sudo git` 可正常查看且干净；`as` 直接运行 git 会触发 Git safe.directory 保护。后续可决定是保持 root 管理，还是配置受限的 safe.directory / 权限模型。 |
| 服务目录规范化 | 部分完成 | 已存在 `/opt/services/account-vault`、`/opt/services/resume-jadeai`、`/opt/services/sub2api`；但仍发现 `/root/sub2api-deploy`、`/root/JadeAI`、`/root/sorryiosSearch`。需要逐服务确认哪些仍在用、哪些可归档。 |
| 证书策略统一 | 部分完成 | `monitor/resume/sorryiossearch` 使用 Cloudflare Origin Certificate；`log/cpa` 使用 Let's Encrypt。当前可用，但策略尚未统一成台账。 |
| Docker / 服务健康检查 | 部分完成 | Docker running 指标和部分容器 health 存在；业务 HTTP health、数据库连接、Redis ping、错误率指标仍未系统化。 |
| Grafana Dashboard | 基础完成 | 主机、HTTPS、TLS、Docker、Backup 已覆盖；Nginx 4xx/5xx、Postgres、Redis、应用错误率等深度面板未完成。 |
| Cloudflare 配置台账 | 不完整 | 仓库里只发现通用 DNS/恢复文档线索，未发现 LosAngeles 专属 DNS、橙云/灰云、SSL/TLS、WAF、Origin Certificate 使用清单。 |

## 4. 未完成事项

### P0

1. 修改 `as` 密码，并继续禁用明文密码文件。  
   `/opt/as_password` 已删除。后续临时提权通过共享终端里的 `sudo -v` 授权；任务结束后可执行 `sudo -k` 清除 sudo 缓存。

### P1

1. R2 拉回恢复演练。
   当前已完成 R2 上传、远端列表/大小核验和 dry-run；还需要从 R2 拉回到临时目录，再抽样做 Postgres、Redis、configs、volumes 的恢复验证。

2. Cloudflare R2 生命周期保留策略。
   当前未记录 bucket 生命周期策略；建议设置按天/周/月分层保留，避免长期无限增长，同时保留足够恢复窗口。

### P2

1. SSH 来源 IP 限制。  
   当前 UFW 的 `22/tcp` 仍为 Anywhere。如果有固定出口 IP，应改为仅允许固定来源。

2. 服务目录从 `/root` 清理/迁移到 `/opt/services`。  
   需要确认 `/root` 下历史目录是否仍承载运行服务，再逐服务迁移或归档。

3. Postgres / Redis exporter。  
   当前未发现 `postgres_exporter` 或 `redis_exporter` 容器，也未发现对应 Prometheus job。

4. 安全日志告警增强。  
   当前未发现 SSH 失败登录、Fail2ban ban 数量、UFW 状态异常、Nginx 异常访问相关 Prometheus 规则。

5. Nginx 与业务级监控深化。  
   后续应补 Nginx 请求量、4xx/5xx、应用健康、数据库连接、Redis ping、业务错误率等指标。

6. Cloudflare 配置台账。  
   补全 DNS 记录、代理状态、SSL/TLS 模式、WAF/安全规则、Origin Certificate 使用范围。

7. Git 操作权限模型。  
   决定 `/opt/ops` 后续是 root-only 管理，还是给 `as` 配置明确的 safe.directory / 写权限流程。

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
- 未登录 Cloudflare 控制台，因此 DNS、WAF、橙云/灰云状态只能标记为台账未发现，不能直接证明控制台未配置。
- 未做从 R2 拉回后的完整恢复演练；当前已完成 R2 上传、远端列表/大小核验和 R2 同步告警接入。
- 未读取或打印任何 `.env`、私钥、Grafana 密码文件或 `/opt/as_password` 内容。

## 6. 推荐下一步

1. 由用户修改 `as` 密码；继续使用 `sudo -v` 临时授权，不再保存明文密码文件。
2. 做一次 R2 拉回恢复演练，并配置 Cloudflare R2 生命周期保留策略。
3. 清点 `/root` 历史服务目录，确认迁移/归档计划。
4. 补齐 Cloudflare、证书策略、云厂商/owner 台账。
5. 优化 Alertmanager 告警模板、分级路由和通知抑制策略。
6. 做一次应用级恢复演练，验证恢复数据可被业务容器启动读取。
