# LosAngeles standards/09 全量只读检查报告

检查日期：2026-07-05  
服务器：LosAngeles  
公网 IP：23.185.200.12  
依据：`standards/09-server-ops-handbook.md` 31 章清单 + 附录 A P0 汇总  
检查方式：只读实机审计 + `/opt/ops` 台账/Runbook 核对  
原始证据：

- `/tmp/losangeles-09-full-audit.out`
- `/tmp/losangeles-09-db-acl-audit.out`

## 1. 结论

这次才算完成“先完整检查，再开始优化”的检查阶段。

上一份 `runbooks/losangeles-standards-09-audit.md` 主要是附录 A 的 P0 初步验收矩阵；本报告扩展到了 `standards/09` 31 章清单里的 P0/P1/P2 方向，覆盖系统、存储、软件源、账号、SSH、sudo、授权 key 指纹、UFW、Fail2ban、Nginx、TLS、Docker、Compose、数据库/Redis、备份恢复、监控告警、日志轮转、定时任务、凭据权限、台账与云侧控制面项。

总体判断：

- 生产基础盘：可用。
- 安全入口：基本达标。
- 备份与恢复：基本达标。
- 监控与告警：基本达标。
- 文档/台账/runbook：基本达标。
- 严格企业级标准：仍有若干待优化项、云侧厂商能力限制和账单暂缓项。

当前没有发现“已经暴露数据库/Redis 到公网”或“备份/监控完全不可用”这类 P0 级事故隐患。

## 2. 已核实达标项

### 2.1 系统与补丁

- Ubuntu 24.04.4 LTS，非 EOL。
- 当前内核 `6.8.0-134-generic`。
- `/var/run/reboot-required` 不存在。
- `unattended-upgrades` active/enabled。
- `apt-get -s upgrade` 显示仅 `fwupd` 因 phased update 延迟，非紧急未升级。

### 2.2 SSH 与账号

- `sshd -T` 显示：
  - `permitrootlogin no`
  - `passwordauthentication no`
  - `pubkeyauthentication yes`
  - `kbdinteractiveauthentication no`
- 机器上可登录的人类账号只看到 `as`。
- `as` 属于 `sudo` 组。
- `/home/as/.ssh/authorized_keys` 权限为 `600 as:as`。
- root authorized_keys 文件存在但本次只记录元数据和指纹，不打印 key 内容。

### 2.3 网络暴露面

- UFW active，默认 deny incoming、allow outgoing、deny routed。
- 公网监听主要为：
  - `22`
  - `80`
  - `443`
- 业务后端、Grafana、Prometheus、Alertmanager、Loki、Postgres、Redis、exporter 均为 `127.0.0.1` 或 Docker 网络内访问。
- Fail2ban active/enabled，`sshd` jail active；累计封禁记录存在。

### 2.4 Nginx 与 TLS

- `nginx -t` 通过。
- 已启用站点：
  - `resume.areasong.top`
  - `sorryiossearch.areasong.top`
  - `cpa.areasong.top`
  - `log.areasong.top`
  - `monitor.areasong.top`
- Nginx 站点均配置 TLS 1.2 / 1.3。
- 公网证书探测均能读到有效证书。
- `cpa.areasong.top`、`log.areasong.top` 使用 Let's Encrypt。
- Cloudflare 代理域名公网展示 Cloudflare 边缘证书。

### 2.5 Docker 与服务运行

- Docker Engine 正常。
- 运行容器均 `restart=unless-stopped`。
- 关键端口显式绑定到 `127.0.0.1` 或仅容器网络。
- sub2api、sub2api-postgres、sub2api-redis、account-vault-postgres 有容器 healthcheck。
- compose 文件主要位于：
  - `/opt/services/sub2api/compose.yml`
  - `/opt/services/account-vault/compose.yml`
  - `/opt/services/resume-jadeai/compose.yml`
  - `/opt/ops/observability/docker-compose.yml`

### 2.6 备份与恢复

- 本机备份脚本位于 `/opt/ops/scripts/backup`，权限为 root-owned。
- 本机备份产物持续存在于 `/var/backups/ops`。
- R2 配置文件 `/etc/ops/r2-backup.env` 为 `600 root:root`。
- R2 同步日志存在。
- 已有恢复类 runbook：
  - `losangeles-backup-restore-drill-20260703.md`
  - `losangeles-r2-restore-drill-20260703.md`
  - `losangeles-app-restore-drill-20260704.md`
  - `losangeles-cross-machine-restore-drill.md`

### 2.7 监控与告警

- Prometheus / Grafana / Alertmanager / Loki / Promtail / Node Exporter / Blackbox Exporter / Postgres Exporter / Redis Exporter 均运行。
- Prometheus targets 全部 up。
- Prometheus 当前无 firing alerts。
- Alertmanager 正常，邮件通知链路前序已验证。
- textfile metrics 正常更新：
  - backup
  - R2 backup
  - Docker
  - security
  - business log
  - Cloudflare Origin Certificate
- Grafana dashboards 已存在：
  - Host
  - Services and Backups
  - Datastores
  - Security
  - App Health
  - Certificates and Cloudflare

### 2.8 凭据文件权限

- `/etc/ops`、`/etc/observability`、`/etc/account-vault` 均为 root-only 目录。
- 主要 secret 文件为 `600 root:root`。
- `.gitignore` 已覆盖 `.env`、`*.key`、`*.pem`、数据库备份、日志等。
- 本次未打印任何 `.env`、私钥、SMTP、R2、数据库密码内容。

## 3. 已确认不达标或待优化项

### 3.1 主机时区不是 UTC

初查时：

- Time zone: `Europe/London`
- NTP synchronized: yes

后续状态：

- 2026-07-05 复核时区已为 `UTC`，NTP synchronized。

标准：

- `standards/09` 建议主机统一 UTC。

判断：

- 初查偏离已完成收敛。

建议：

- 持续保持 UTC，不再作为待办项。

### 3.2 `/etc/fstab` 已切换为 UUID

初查时：

- `/` 使用 `LABEL=cloudimg-rootfs`
- `/boot` 使用 `LABEL=BOOT`
- `/boot/efi` 使用 `LABEL=UEFI`

后续状态：

- 已通过 `runbooks/losangeles-standards-09-b3-fstab-uuid-20260706.md` 将 `/`、`/boot`、`/boot/efi` 切换为 UUID。
- `findmnt --verify --verbose` 通过，结果为 `0 parse errors, 0 errors`。
- `mount -a` 通过。
- 已执行 `systemctl daemon-reload`。

判断：

- 初查缺口已关闭；当前运行态验证通过。
- 重启级启动链路验证尚未执行，建议下次维护窗口或自然重启后补记。

建议：

- 后续如新增独立数据盘，再为数据盘使用 UUID 与 `nofail` 策略。

### 3.3 无独立数据盘

当前：

- 业务、Docker、备份均在系统盘 `/dev/sda1`。
- 当前磁盘空间充足，inode 充足。

判断：

- 当前体量可接受；严格企业级数据隔离未达标。

建议：

- 后续数据增长或预算允许时规划独立 `/data`。

### 3.4 Docker daemon 日志基线已补齐

初查时：

- `/etc/docker/daemon.json` 不存在。
- 容器 `logType=json-file`，`logConfig={}`。

后续状态：

- 已通过 `runbooks/losangeles-standards-09-batch-b2-20260705.md` 配置 `/etc/docker/daemon.json`：`live-restore=true`、`log-driver=json-file`、`max-size=50m`、`max-file=5`。
- 已通过 `runbooks/losangeles-standards-09-c4-container-logging-limits-20260705.md` 完成业务与监控容器 Compose 显式日志轮转。
- 当前运行容器已验证为 `max-size=50m`、`max-file=5`。

判断：

- 初查风险已收敛，Docker daemon 默认日志策略与现有容器显式日志轮转均已落地。

建议：

- 后续新增 compose 时继续显式写入 `logging` 策略，避免依赖隐式默认值。

### 3.5 初查：部分容器镜像使用 `latest`，后续已固定 digest

初查时运行中：

- `weishaw/sub2api:latest`
- `twwch/jadeai:latest`
- 本地 build 镜像 `account-vault-web:latest`

后续状态：

- 已通过 `runbooks/losangeles-standards-09-c5-image-digest-pinning-20260705.md` 固定当前生产镜像 digest。
- 当前生产 compose 已去除第三方镜像 `latest` 引用。

风险：

- 可追溯性弱，后续重拉可能不是同一版本。

建议：

- 后续升级镜像时继续走 digest / 明确版本记录。

### 3.6 容器资源限制已落地并完成运行态复核

初查时：

- Docker inspect 显示多数组件当时没有显式内存边界。

后续状态：

- C3a 已为业务容器增加 `mem_limit`、`memswap_limit`、`cpus` 和 `pids_limit`。
- C3b 已为监控栈容器增加 `mem_limit`、`memswap_limit`、`cpus` 和 `pids_limit`。
- C3c 运行态复核确认当前 16 个运行容器均已有明确 `HostConfig.Memory`，无 `Memory=0` 容器。

判断：

- 初查缺口已关闭；后续只需按实际资源曲线做单服务调优。

### 3.7 多数容器仍以默认用户运行

当前：

- 部分监控组件已有 `nobody` 或固定 UID。
- 多个业务/数据库容器 `user=` 为空，即使用镜像默认用户。

判断：

- 对官方 Postgres/Redis 不一定等于宿主 root 风险，但严格标准下应逐项确认。

建议：

- 逐服务确认镜像内默认用户和文件权限，再决定是否改。

### 3.8 SSH X11Forwarding 仍开启

初查时：

- `x11forwarding yes`

后续状态：

- 2026-07-05 复核 `sshd -T` 显示 `x11forwarding no`。

判断：

- 已完成收敛。

建议：

- 持续保持 `X11Forwarding no`。

### 3.9 SSH 来源 IP 未限制

当前：

- UFW `22/tcp` 为 Anywhere。

已知原因：

- 用户当前无固定出口 IP，强行限源可能锁定自己。

判断：

- 风险接受项，不建议现在硬改。

建议：

- 有固定出口 IP 后再收紧。

### 3.10 auditd 未安装/未启用

当前：

- `auditd inactive`
- `auditd not-found`

判断：

- AppArmor 已启用；auditd 属于更深一层操作审计。

建议：

- P1 增强项：安装 auditd，至少审计 root execve 和关键文件。

### 3.11 journald 持久化与容量上限已配置

初查时：

- 未发现 `SystemMaxUse`。
- journal 当前占用约 1.4G。

后续状态：

- 已通过 `runbooks/losangeles-standards-09-batch-b1-20260705.md` 新增 `/etc/systemd/journald.conf.d/90-ops-limits.conf`。
- 当前配置为 `Storage=persistent`、`SystemMaxUse=1G`、`RuntimeMaxUse=256M`。
- 已重启 `systemd-journald` 生效。

判断：

- 初查缺口已关闭，journald 具备持久化和容量上限。

建议：

- 后续按磁盘容量和告警噪声观察是否需要微调上限。

### 3.12 登录/审计类日志留存已延长

初查时：

- `rsyslog` 默认 `rotate 4 weekly`。
- `fail2ban` 默认 `rotate 4 weekly`。
- `auth.log` 这类登录日志按当前配置约 4 周。

后续状态：

- 已通过 `runbooks/losangeles-standards-09-batch-b1-20260705.md` 将 `/etc/logrotate.d/rsyslog`、`/etc/logrotate.d/fail2ban`、`/etc/logrotate.d/ufw` 调整为 26 周保留。
- `logrotate -d /etc/logrotate.conf` 已通过。

判断：

- 本机登录、安全和 UFW 类日志已达到约半年保留，满足当前单机场景的基础审计追溯要求。

建议：

- 如后续要求不可篡改或跨机长期审计，可再接 R2/对象锁类归档。

### 3.13 Redis 内部实例未设置密码和 maxmemory

当前：

- Redis 仅 Docker 网络内，不暴露公网。
- C1 复核确认 `requirepass` 已设置。
- C1 复核确认 `maxmemory=512MiB`。
- `maxmemory-policy=noeviction`
- AOF 已启用。
- 默认 ACL 仍允许 `+@all`。

判断：

- 外部暴露风险低。
- Redis 密码、maxmemory、持久化和内网隔离已完成。
- 严格 Redis P0 下剩余缺口是高危命令 / ACL 策略：是否限制 `FLUSHALL`、`FLUSHDB`、`CONFIG`、`SHUTDOWN` 等命令仍需决策。

建议：

- 如需限制高危命令，应单独开维护窗口，先验证 `sub2api`、healthcheck 和 redis exporter 兼容性。

### 3.14 Postgres 角色权限复核结果

初查时：

- 通过容器内 `postgres` 系统用户查询失败。
- 未打印任何数据库密码，也未进入业务数据。

后续状态：

- 已通过 `runbooks/losangeles-standards-09-c2e-postgres-role-readonly-audit-20260705.md` 完成只读复核。
- 已通过 `runbooks/losangeles-standards-09-c2f-sub2api-migration-runtime-analysis-20260706.md` 完成 `sub2api` migration/runtime 失败点只读定位。
- `account-vault` 已使用低权限 `account_vault_app`，该角色无 superuser / createdb / createrole / replication / bypassrls。
- `sub2api_app` 低权限角色已存在，且具备现有业务表 DML 权限。
- `sub2api` 业务容器当前仍使用 superuser `sub2api`。
- C2f 确认 `sub2api_app` 对 `public` schema 有 `USAGE` 但没有 `CREATE`；`schema_migrations` 表已存在且 `sub2api_app` 有 DML 权限。
- C2b 直接切换失败的精确 SQL 是应用启动时执行 `CREATE TABLE IF NOT EXISTS schema_migrations`，低权限用户因缺少 schema `CREATE` 被拒绝。

判断：

- `account-vault` 达标。
- `sub2api` 为明确风险接受项，治理需要应用侧配合拆分 migration 与 runtime；当前不是继续盲查，而是等待应用能力确认或维护窗口方案设计。

建议：

- 不再直接强切 `sub2api` 到低权限用户。
- 先确认应用是否支持关闭启动 migration，或将 migration 改为独立维护命令：migration 阶段用管理用户，runtime 阶段用低权限用户。
- 不建议为 `sub2api_app` 直接授予 broad `public` schema `CREATE`，除非在维护窗口内明确接受运行用户 DDL 风险并准备回滚。

### 3.15 初查：Nginx 安全头未完整核验，后续已补齐

初查时：

- 审计只抓到 server/listen/cert/proxy header 等行。
- 未发现明显统一安全头基线输出。

后续状态：

- 已通过 `runbooks/losangeles-standards-09-a1-nginx-security-headers-20260705.md` 完成源站安全响应头核对与补齐。
- 全局启用 `server_tokens off`。
- `resume`、`sorryiossearch`、`log` 已补齐 HSTS / nosniff / frame / referrer。
- `cpa` 已补 HSTS，保留应用侧 CSP / nosniff / frame / referrer。
- `monitor` 已补 HSTS / referrer，保留 Grafana nosniff / frame。

判断：

- 已完成。

建议：

- 后续 CSP 继续按应用单独治理，不做全局 CSP。

### 3.16 `/opt/services/account-vault/app/docker-compose.yml` 是旧 compose

初查时：

- 当前实际 compose 是 `/opt/services/account-vault/compose.yml`。
- 旧文件 `/opt/services/account-vault/app/docker-compose.yml` 仍存在。

后续状态：

- 2026-07-05 复核旧文件已改名为 `/opt/services/account-vault/app/docker-compose.legacy.yml`。
- 运行容器 compose label 指向 `/opt/services/account-vault/compose.yml`。

风险：

- 已降低误操作风险。

建议：

- 保持 legacy 命名；后续如彻底不需要，再归档删除。

### 3.17 SMTP 密码文件权限可再收紧

初查时：

- `/etc/observability/alertmanager-smtp-password mode=640 owner=root:nogroup`

后续状态：

- 2026-07-05 复核已为 `600 root:root`。

建议：

- 已完成。

### 3.18 sysctl 基线已整理到 `/etc/sysctl.d/99-ops-baseline.conf`

初查时：

- 存在 Ubuntu 默认 sysctl、`99-bbr-x-ui.conf`、cloud image IPv6 配置。
- 未见统一的 `/etc/sysctl.d/99-ops-baseline.conf`。

后续状态：

- 已通过 `runbooks/losangeles-standards-09-batch-b1-20260705.md` 新增 `/etc/sysctl.d/99-ops-baseline.conf`。
- 基线覆盖 TCP syncookies、rp_filter、Docker 需要的 ip_forward、BBR/fq、kptr_restrict、ptrace_scope、unprivileged_bpf 等。
- 已执行 `sysctl --system`。

判断：

- 初查缺口已关闭，基础内核参数已有 ops 可追踪基线。

建议：

- 后续只在明确业务影响后再增加更激进的内核参数。

## 4. 云侧确认结果与能力限制

2026-07-05 用户已在云厂商控制台人工核对，并回填到 `runbooks/losangeles-standards-09-d2-cloud-control-plane-governance-20260705.md` 与 `inventory/servers.yaml`。

已确认：

- 云厂商控制台：`https://server.zgocloud.cc/`。
- 控制台实例名称：`LosAngeles`。
- 云主账号 MFA / 两步验证已开启。
- 账号绑定邮箱和手机号可用。
- 没有其他人共用主账号。
- 当前无 API Key。

厂商能力限制：

- 云服务厂商没有安全组 / 云防火墙 / 网络规则概念或页面。
- 当前无快照。
- 当前无云审计与安全通知能力。

暂缓项：

- 账单、余额、到期、欠费、实例维护事件通知按用户要求本轮先不处理。

补偿控制：

- 网络入口以主机侧 UFW 默认拒绝入站、仅放行 22/80/443、Fail2ban、端口收敛和监控告警补偿。
- 灾难恢复以本机备份、R2 异地备份、本机/R2/应用级恢复演练补偿。
- 审计与安全通知以主机日志、Fail2ban、UFW/Nginx 指标、Loki/Grafana/Alertmanager 和 Cloudflare 侧能力补偿。
- 域名和 Cloudflare 账号治理见 `inventory/cloudflare-areasong-top.md`。

## 5. 推荐优化批次

### 批次 A：低风险，不重启业务

状态：已完成或已复核完成。

1. SMTP 密码文件权限已为 `600 root:root`。
2. 旧 account-vault compose 已改名为 legacy，运行容器未引用旧文件。
3. 主机时区已为 UTC。
4. SSH X11Forwarding 已为 `no`。
5. Nginx 响应头已完成源站核对与补齐。

### 批次 B：低到中风险，需要短维护窗口

状态：B1/B2/B3 已完成；剩余项主要是服务策略和应用配合。

1. journald 上限、logrotate 26 周保留、`99-ops-baseline.conf` 已在 B1 完成。
2. Docker daemon `log-opts` 与 `live-restore` 已在 B2 完成。
3. `fstab` 从 LABEL 改 UUID 已在 B3 完成；重启级验证待下次维护窗口或自然重启后补记。

### 批次 C：会影响容器，需要维护窗口

1. 为业务容器逐步加内存限制。
2. Redis 高危命令 / ACL 策略：禁用或明确风险接受。
3. sub2api migration/runtime 拆分实施：C2f 已定位失败点，后续需确认应用是否支持独立 migration 或关闭启动自动 migration。

### 批次 D：云侧治理

1. 云账号 MFA / AK：已核对，MFA 已开启，当前无 API Key。
2. 安全组 / 云防火墙 / 网络规则：厂商不支持，已记录风险接受和主机侧补偿控制。
3. 云快照、云审计、安全通知：厂商不支持或当前无，已记录风险接受和备份/监控补偿控制。
4. 账单、到期、维护事件通知：用户本轮暂缓，后续单独处理。
5. 云标签和台账一致性：实例名 `LosAngeles`、owner/provider/region/control_plane 已入台账。

## 6. 当前不建议马上做

- 不建议现在限制 SSH 来源 IP，除非先确认固定出口 IP 和逃生通道。
- 不建议无明确变更目的再次重启 Docker，因为会短暂影响所有容器。
- 不建议直接修改 Redis ACL / 高危命令策略，必须先验证 sub2api、healthcheck 和 exporter 兼容性。
- 不建议为 `sub2api_app` 直接授予 `public` schema `CREATE` 或再次强切运行用户；应先完成应用 migration 能力确认。
- 不建议再次无目的修改 fstab；B3 已完成运行态验证，重启级验证留到下次维护窗口或自然重启后补记。
- 不建议把所有容器一次性改非 root；资源限制已落地，后续如需调整应按单服务验证。

## 7. 检查阶段完成判定

本次已经完成服务器内可见范围的全量只读检查。

云侧控制台确认已在 D2 完成并入台账。B1/B2 日志、Docker daemon 与 sysctl 基线收敛已完成，B3 `fstab` UUID 收敛已完成，C1 Redis 密码/maxmemory/持久化/内网隔离复核已完成，C2f sub2api migration/runtime 失败点定位已完成，C3a/C3b/C3c 容器资源限制已完成，C7 Postgres exporter PostgreSQL 18 兼容性修复已完成。仍未完成的是 Redis 高危命令 / ACL 策略、sub2api migration/runtime 拆分实施，以及用户本轮暂缓的账单/到期治理，这些已在本报告列为后续项。

批次 A、B1、B2、B3、C1、C2f、C3a、C3b、C3c、C7 已完成。下一步应从剩余 C 项或业务配合项中挑选，不做一刀切运行配置变更。
