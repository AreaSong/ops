# LosAngeles standards/09 整本文档覆盖检查

检查日期：2026-07-05  
服务器：LosAngeles  
标准文档：`standards/09-server-ops-handbook.md`  
标准规模：2057 行，31 章，4 个附录  
本报告定位：整本文档覆盖视图，不替代实机原始审计。

## 1. 结论

按“整份 `09-server-ops-handbook.md`”这个口径，前两份报告还不能算完全检查完：

- `losangeles-standards-09-audit.md`：主要覆盖附录 A 的 P0 验收矩阵。
- `losangeles-standards-09-full-audit-20260705.md`：覆盖主机内可见范围的全量只读审计。
- 本报告：补齐 31 章 + 附录级别的整本文档覆盖检查。

当前结论：

- 主机内技术项：已完成较完整检查。
- 云侧/Cloudflare 控制台项：D2 已完成人工核对；云主账号 MFA、无 API Key、无安全组/云防火墙/网络规则、无快照、无云审计/安全通知均已记录；账单/到期按用户要求暂缓，云标签和组织节律后续治理。
- 组织流程项：多数已有文档框架，但值班、离职回收、故障演练、月度/季度例行节律尚未形成真实执行记录。
- 架构增强项：HA、多 AZ、独立数据盘、CI/CD、IaC、APM、漏洞扫描、对象锁/PITR 等尚未完整落地。

所以，严格说：整本文档已经完成“覆盖检查”，但不是“所有条目都达标”。后续应按本报告的缺口分批优化。

## 2. 逐章覆盖矩阵

| 章节 | 标准主题 | 当前覆盖结论 | 主要证据 | 缺口 / 下一步 |
|---|---|---|---|---|
| 1 | 文档定位与 AI 协作 | 基本达标 | `standards/09` 已有 1.4；`/opt/ops` root-only 流程已记录 | 后续继续按“先检查、再确认、再变更”执行 |
| 2 | 生命周期总览 | 部分达标 | 已处于日常运行阶段；有 runbook、台账、备份、监控 | 退役、迁移、故障复盘、例行日历仍需真实执行记录 |
| 3 | 硬件与实例层 | 部分验证 | KVM 云主机、provider/region/owner 已入台账；D2 已完成控制台人工核对 | 账单/到期治理按用户要求暂缓；厂商无快照能力；无 HA |
| 4 | 操作系统基线 | 基本达标 | Ubuntu 24.04、UTC/NTP 同步、unattended-upgrades、无 reboot-required；B1 已完成 journald 持久化/容量上限与 `99-ops-baseline.conf` | core dump 上限、auditd/HIDS 可作为后续增强 |
| 5 | 存储与文件系统 | 部分达标 | 磁盘和 inode 健康；备份存在；D2 已确认厂商当前无快照能力；B3 已完成 `fstab` UUID 收敛并通过 `mount -a` | 无独立数据盘；以本机备份、R2 和恢复演练补偿无云快照；下次重启后补启动链路复核 |
| 6 | 软件源与依赖 | 基本达标 | Ubuntu/Docker 源独立，Docker GPG keyring 存在 | 关键组件版本锁定、季度源审计、运行时版本台账需补 |
| 7 | 资产与标识 | 部分达标 | `servers.yaml` 补齐 provider/region/owner；服务和端口台账存在；控制台实例名为 `LosAngeles` | 主机名不规范；到期/计费/欠费告警按用户要求暂缓；云标签后续可补 |
| 8 | 网络架构 | 部分达标 | 公网端口收敛；主机级无私网 IP；IPv6 仅 link-local；D2 已确认厂商无安全组/云防火墙/网络规则能力 | 以 UFW、Fail2ban、端口收敛和监控告警补偿；拓扑图可后续补 |
| 9 | 账号与访问 | 基本达标 | SSH 禁 root/password；`as` sudo；authorized_keys 指纹已查；X11Forwarding 已关闭；云账号 MFA 已开启且当前无 API Key | AllowGroups、auditd、逃生通道可作为后续增强 |
| 10 | 网络暴露面 | 基本达标 | UFW active；仅 22/80/443；DB/cache/exporter 不公网暴露 | SSH 来源未限制，因无固定出口 IP 风险接受；月度端口审计需例行化 |
| 11 | 入口层安全 | 基本达标 | Nginx `-t` 通过；TLS 1.2/1.3；Cloudflare/证书台账存在；A1 已完成源站安全响应头基线 | 回源鉴权、真实 IP 链、WAF/Rate Limit、源站隐藏仍可继续增强 |
| 12 | 深度加固 | 部分达标 | AppArmor loaded；Docker 默认 profile enforce | auditd 未安装；HIDS/AIDE/lynis/OpenSCAP 未落地 |
| 13 | 凭证与密钥 | 基本达标 | secret 文件 root-only；`.gitignore` 覆盖敏感模式；SMTP 密码文件已收紧到 `600 root:root` | gitleaks、凭证台账、轮换记录、RAM role 评估可继续增强 |
| 14 | 安全事件响应 | 文档框架达标 | `standards/09` 有流程；postmortem 模板存在 | 入侵响应桌面演练、云安全告警渠道、取证命令块待落地 |
| 15 | 服务部署规范 | 部分达标 | 服务主要在 `/opt/services`；restart 策略；健康检查和监控 | 部分容器无 healthcheck；非 root、内存上限、systemd 加固未全覆盖；旧 compose 需清理 |
| 16 | 容器与编排 | 部分达标 | Compose 路线明确；端口绑定明确；B2 已完成 Docker daemon 日志默认值和 `live-restore`；生产镜像已固定 digest | 业务容器无内存限制；无镜像扫描 |
| 17 | 数据库与中间件 | 部分达标 | DB/Redis 不公网暴露；备份/exporter 已接入；Postgres 角色权限已只读复核 | Redis 无密码/maxmemory/高危命令限制；`sub2api` 运行用户仍为 superuser，需应用侧拆分 migration/runtime；无 PITR/WAL 归档 |
| 18 | 配置管理与 IaC | 部分达标 | `/opt/ops` Git 化；变更提交存在 | Ansible/Terraform 未落地；漂移检测未例行 |
| 19 | CI/CD 与制品 | 未系统落地 | 业务当前主要手工/Compose 管理 | 制品 tag、部署凭证、制品保留、回滚链路需要后续建设 |
| 20 | 变更管理与发布 | 基本有框架 | `standards/05`、runbook、提交记录存在 | 还没有正式变更单系统、封网日历、重大变更评审记录 |
| 21 | 定时任务治理 | 部分达标 | root crontab 有备份/指标任务并有日志重定向 | 新任务未统一 systemd timer；缺 flock；定时任务台账与季度核对未例行 |
| 22 | 日志管理 | 基本达标 | B1 已完成 journald 上限、rsyslog/fail2ban/ufw 26 周保留；B2/C4 已完成 Docker daemon 与容器日志轮转；Loki/Promtail 已接入 | 不可篡改审计、跨机长期归档可继续增强 |
| 23 | 监控与告警 | 基本达标 | Targets 全 up；无 firing alerts；邮件告警已接入；Dashboard 完整 | 电话级通知、月度告警回顾、异地外部探测仍可增强 |
| 24 | 链路追踪与 APM | 未落地 / 暂不急 | 当前业务规模较小 | 请求 ID、OpenTelemetry、APM 采样策略后续按业务复杂度建设 |
| 25 | 性能调优与容量规划 | 部分达标 | 磁盘/内存/CPU 监控；BBR 已存在 | nofile 全局基线、OOMScoreAdjust、容量 review、压测基线未系统化 |
| 26 | 高可用架构 | 风险接受 | 单机 + 本机/R2/恢复演练 | 应用多副本、LB、多 AZ、DB 主从、故障转移演练未做 |
| 27 | 备份与容灾 | 基本达标 | 本机备份、R2、恢复演练、跨机器预案存在 | 对象锁/无删除权凭证、RTO/RPO 实测、PITR、跨机器实机演练待做 |
| 28 | 补丁与漏洞管理 | 部分达标 | 自动安全更新开启；近期已升级重启 | 镜像/机器漏洞扫描、月度补丁日、EOL 时间表、安全公告订阅待落地 |
| 29 | 故障应急响应 | 文档框架达标 | disk-full/host-unreachable/service-5xx/mysql runbook 和复盘模板存在 | 故障通报节奏、top3 高频故障演练、真实复盘记录待积累 |
| 30 | 环境隔离、多机管理与迁移 | 部分达标 | 服务迁移 runbook、跨机器恢复预案存在 | prod/test 网络与凭证隔离、stage、漂移检测、依赖盘点工具化未完整 |
| 31 | 成本容量治理、协作与退役 | 部分达标 | AI 变更需批准、root-only、文档三层结构已建立 | 月度账单 review、值班表、离职回收演练、退役九步执行记录、季度 runbook 抽查待建立 |

## 3. 附录覆盖

| 附录 | 标准内容 | 当前结论 |
|---|---|---|
| A | P0 全量验收清单 | 已有 `losangeles-standards-09-audit.md` 初步矩阵；本报告补充整本文档视角 |
| B | 常用命令速查 | 工具性内容，不需要达标验收 |
| C | 新机上线时序表 | LosAngeles 是存量机器；已按存量治理补齐多数 Day-0/Day-1/Day-7 能力，但不是按新机流程原生上线 |
| D | 运维日历 | 尚未形成“每日/每周/每月/每季/每年”的真实执行记录；应作为后续治理任务 |

## 4. 不适用或暂不适用项

- 物理机带外、SMART/RAID/ECC：当前是云 VM，不适用。
- GPU/CUDA/nvidia-smi：当前无 GPU，不适用。
- K8s 生产基线：当前选择 Docker Compose 路线，不适用。
- MySQL 专项：当前业务主要为 PostgreSQL/Redis，不适用。
- MQ 专项：当前未发现 RabbitMQ/Kafka/RocketMQ，不适用。
- NFS/共享存储：当前未使用，不适用。
- 中国大陆 ICP/公安备案：当前源站在美国；是否需要取决于后续面向大陆访问和域名备案策略。

## 5. 真正还没检查完的内容

这部分不是我遗漏命令，而是必须依赖控制台或业务决策；其中 D2 已完成一轮人工核对：

### 云厂商控制台

- 已确认：云主账号 MFA 已开启，账号邮箱/手机号可用，主账号未共用，当前无 API Key。
- 已确认厂商能力限制：无安全组/云防火墙/网络规则、无快照、无云审计与安全通知能力。
- 已暂缓：账单、余额、到期、欠费、实例维护事件通知。
- 后续可补：实例是否抢占式/竞价、云标签。

### Cloudflare 控制台

- DNS 托管账号 MFA。
- 域名自动续费。
- WAF/Rate Limit 是否要创建规则。
- `www.areasong.top` 门户上线策略。
- HSTS 是否继续不启用。

### 组织流程

- 值班表和升级路径。
- 入职/离职权限回收演练。
- 月度账单 review。
- 季度 runbook 抽查。
- 年度凭证轮换。
- 入侵响应桌面演练。

### 业务侧

- 登录后业务探针。
- 请求 ID / APM。
- 压测基线。
- 业务级 RTO/RPO 目标。
- CI/CD 发布链路。

## 6. 后续优化总分组

### A. 不重启业务的低风险收敛

- 主机时区改 UTC。
- 关闭 SSH X11Forwarding。
- SMTP 密码文件权限收紧。
- 旧 account-vault compose 改名归档。
- Nginx 安全响应头只读检查并补台账。

### B. 需要维护窗口的主机收敛

- 业务容器内存限制。
- auditd/HIDS 或不可篡改审计归档。

### C. 需要应用配合的服务收敛

- Redis 密码/maxmemory/高危命令。
- sub2api migration/runtime 拆分后再切换低权限运行用户。
- 登录后任务指标、关键接口分位延迟等应用指标。
- CI/CD 制品与回滚链路。

### D. 云侧与组织治理

- 账单/到期暂缓项、云标签和成本治理节律。
- Cloudflare MFA、续费、WAF/Rate Limit。
- 值班、离职回收、故障演练、月度/季度/年度运维日历。

## 7. 本阶段完成判定

到本报告为止：

- `standards/09` 的主机内技术项已经完成全量只读检查。
- `standards/09` 的 31 章和 4 附录已经完成整本文档级覆盖检查。
- 云侧控制台核验已完成；尚未完成的是组织流程落地、业务决策项和剩余维护窗口优化执行。

下一步不应直接说“全部达标”，而应说：

> standards/09 已完成整本文档覆盖检查；LosAngeles 已达生产基础可用，企业级严格标准仍有流程、架构、账单暂缓项和剩余维护窗口优化项待推进。
