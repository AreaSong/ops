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

当前：

- Time zone: `Europe/London`
- NTP synchronized: yes

标准：

- `standards/09` 建议主机统一 UTC。

判断：

- 不是安全事故，但属于标准化偏离。

建议：

- 低风险维护变更：`timedatectl set-timezone UTC`。

### 3.2 `/etc/fstab` 使用 LABEL，不是 UUID

当前：

- `/` 使用 `LABEL=cloudimg-rootfs`
- `/boot` 使用 `LABEL=BOOT`
- `/boot/efi` 使用 `LABEL=UEFI`

标准：

- fstab 建议 UUID，云盘建议 `nofail`。

判断：

- 云镜像常见写法，当前可用；严格标准下偏离。

建议：

- 放到维护窗口，改前备份 `/etc/fstab`，改后执行 `mount -a` 验证。

### 3.3 无独立数据盘

当前：

- 业务、Docker、备份均在系统盘 `/dev/sda1`。
- 当前磁盘空间充足，inode 充足。

判断：

- 当前体量可接受；严格企业级数据隔离未达标。

建议：

- 后续数据增长或预算允许时规划独立 `/data`。

### 3.4 Docker daemon 缺少全局日志上限

当前：

- `/etc/docker/daemon.json` 不存在。
- 容器 `logType=json-file`，`logConfig={}`。

风险：

- 容器 stdout/stderr 日志理论上可无限增长，占满磁盘。

建议：

- 维护窗口配置 Docker `log-opts`，例如 `max-size=50m`、`max-file=5`。
- 需要重启 Docker，会短暂影响所有容器。

### 3.5 部分容器镜像使用 `latest`

当前运行中：

- `weishaw/sub2api:latest`
- `twwch/jadeai:latest`
- 本地 build 镜像 `account-vault-web:latest`

风险：

- 可追溯性弱，后续重拉可能不是同一版本。

建议：

- 对第三方镜像 pin 到 digest 或稳定 tag。
- 对本地 build 镜像建立明确版本/tag 策略。

### 3.6 多数容器没有内存限制

当前：

- Docker inspect 显示多数组件 `mem=0`。

风险：

- 应用异常时可能吃满宿主机内存。

建议：

- 按服务逐个加 `mem_limit` 或 compose `deploy.resources`。
- 需要先观察当前资源曲线，不建议拍脑袋统一限制。

### 3.7 多数容器仍以默认用户运行

当前：

- 部分监控组件已有 `nobody` 或固定 UID。
- 多个业务/数据库容器 `user=` 为空，即使用镜像默认用户。

判断：

- 对官方 Postgres/Redis 不一定等于宿主 root 风险，但严格标准下应逐项确认。

建议：

- 逐服务确认镜像内默认用户和文件权限，再决定是否改。

### 3.8 SSH X11Forwarding 仍开启

当前：

- `x11forwarding yes`

判断：

- 生产服务器一般不需要 X11 转发。

建议：

- 低风险收敛：新增 hardening conf 设置 `X11Forwarding no`，`sshd -t` 后 reload，并保留当前会话验证新连接。

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

### 3.11 journald 未显式设置 SystemMaxUse

当前：

- 未发现 `SystemMaxUse`。
- journal 当前占用约 1.4G。

判断：

- 不是事故，但严格日志上限标准下不完整。

建议：

- 配置 journald 上限，例如 `SystemMaxUse=1G` 或按磁盘比例决策。

### 3.12 登录/审计类日志留存不足 180 天

当前：

- `rsyslog` 默认 `rotate 4 weekly`。
- `fail2ban` 默认 `rotate 4 weekly`。
- `auth.log` 这类登录日志按当前配置约 4 周。

判断：

- 不满足 `standards/09` 对审计类日志 180 天留存的严格要求。

建议：

- 增加本地留存或通过 Loki/R2 做长期归档。

### 3.13 Redis 内部实例未设置密码和 maxmemory

当前：

- Redis 仅 Docker 网络内，不暴露公网。
- `maxmemory=0`
- `maxmemory-policy=noeviction`
- ACL 显示：`user default on nopass ... +@all`

判断：

- 外部暴露风险低。
- 但严格 Redis P0 要求下不达标：应有密码、maxmemory、禁用高危命令或至少明确风险接受。

建议：

- 维护窗口处理，因为改 Redis 认证会影响 sub2api 和 exporter。
- 同时配置 `maxmemory` 与策略，避免 OOM。

### 3.14 Postgres 角色权限未成功只读核验

当前：

- 通过容器内 `postgres` 系统用户查询失败。
- 未打印任何数据库密码，也未进入业务数据。

判断：

- 不是确认不达标，而是“未验证”。

建议：

- 后续用受控方式读取角色权限，不打印密码，只输出角色是否 superuser/createdb/createrole。

### 3.15 Nginx 安全头未完整核验/可能不足

当前：

- 审计只抓到 server/listen/cert/proxy header 等行。
- 未发现明显统一安全头基线输出。

判断：

- 需要对 Nginx 站点逐项确认 `HSTS/nosniff/frame/referrer/server_tokens`。

建议：

- 先只读 `curl -I` 检查公网响应头，再决定是否补。

### 3.16 `/opt/services/account-vault/app/docker-compose.yml` 是旧 compose

当前：

- 当前实际 compose 是 `/opt/services/account-vault/compose.yml`。
- 旧文件 `/opt/services/account-vault/app/docker-compose.yml` 仍存在。

风险：

- 后续人员可能误在 app 子目录执行旧 compose。

建议：

- 改名为 `docker-compose.legacy.yml` 或迁入归档目录，并写说明。

### 3.17 SMTP 密码文件权限可再收紧

当前：

- `/etc/observability/alertmanager-smtp-password mode=640 owner=root:nogroup`

建议：

- 收紧为 `600 root:root`。

### 3.18 sysctl 基线不是 `/etc/sysctl.d/99-ops-baseline.conf`

当前：

- 存在 Ubuntu 默认 sysctl、`99-bbr-x-ui.conf`、cloud image IPv6 配置。
- 未见统一的 `/etc/sysctl.d/99-ops-baseline.conf`。

判断：

- 当前并非缺少所有安全 sysctl，但标准化集中管理不足。

建议：

- 后续整理为 ops baseline 文件，避免散落。

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

1. 收紧 SMTP 密码文件权限到 `600 root:root`。
2. 旧 account-vault compose 改名为 legacy，避免误操作。
3. 主机时区改 UTC。
4. 关闭 SSH X11Forwarding，`sshd -t` 后 reload 并验证新连接。
5. 补充 Nginx 响应头只读检查结果到报告。

### 批次 B：低到中风险，需要短维护窗口

1. 配置 journald 上限。
2. 调整 logrotate，使 auth/fail2ban/ufw/nginx 关键日志满足更长留存或明确转存 Loki/R2。
3. 建立 `/etc/sysctl.d/99-ops-baseline.conf`，整理当前散落项。
4. `fstab` 从 LABEL 改 UUID，并验证 `mount -a`。

### 批次 C：会影响容器，需要维护窗口

1. Docker daemon 配置 `log-opts`，重启 Docker。
2. 为业务容器逐步加内存限制。
3. Redis 增加密码、maxmemory、禁用高危命令或明确风险接受。
4. 第三方镜像 pin tag/digest。
5. Postgres 角色权限只读核验并按需收敛。

### 批次 D：云侧治理

1. 云账号 MFA / AK：已核对，MFA 已开启，当前无 API Key。
2. 安全组 / 云防火墙 / 网络规则：厂商不支持，已记录风险接受和主机侧补偿控制。
3. 云快照、云审计、安全通知：厂商不支持或当前无，已记录风险接受和备份/监控补偿控制。
4. 账单、到期、维护事件通知：用户本轮暂缓，后续单独处理。
5. 云标签和台账一致性：实例名 `LosAngeles`、owner/provider/region/control_plane 已入台账。

## 6. 当前不建议马上做

- 不建议现在限制 SSH 来源 IP，除非先确认固定出口 IP 和逃生通道。
- 不建议立即重启 Docker，因为会短暂影响所有容器。
- 不建议直接给 Redis 加密码，必须同步 sub2api 和 exporter 配置。
- 不建议直接改 fstab 后不验证启动链路。
- 不建议把所有容器一次性改非 root 或加统一内存限制，应逐服务验证。

## 7. 检查阶段完成判定

本次已经完成服务器内可见范围的全量只读检查。

云侧控制台确认已在 D2 完成并入台账。仍未完成的是“需要业务凭据/数据库密码配合的深层权限核验”和用户本轮暂缓的账单/到期治理，这些不能从主机内无凭据地可靠完成，已在本报告列为后续项。

下一步应先由用户确认是否按批次 A 开始低风险优化；在此之前不做运行配置变更。
