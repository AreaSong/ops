# LosAngeles standards/09 阶段 9 最终验收记录

日期：2026-08-19 UTC
服务器：LosAngeles（23.185.200.12）
状态：**已完成：PR #350 已合并，生产仓库已同步，源码与运行时身份已分离核对，最终验收通过**
依据：`standards/09-server-ops-handbook.md` 第 14、31 章与附录 A，以及 [历史验收矩阵](losangeles-standards-09-audit.md)

## 1. 范围与边界

本阶段完成 standards/09 全量复核、root-only 证据补齐、AreaSong Ops 主线部署、生产灰度验收和无破坏入侵响应桌面演练。

本轮明确没有执行以下操作：

- 未恢复或改写任何业务数据库；
- 未修改 SSH 来源、主机名、数据盘、数据库权限、Redis 用户、Docker daemon 或云资源；
- 未实际隔离主机、轮换凭据、重装主机或触发业务更新动作；
- 未为消除告警而静默或修改现有告警规则。

分类口径：

- **已完成**：存在当前生产证据；
- **风险接受**：单机模型或业务约束下有明确补偿控制；
- **维护窗口 / 应用待配合**：需另行批准或上游能力；
- **厂商限制 / 用户暂缓**：当前控制面无法提供，或用户已明确暂缓。

## 2. fresh backup 与回滚点

部署前于 2026-08-19 07:43-07:44 UTC 完成 fresh backup：

| 证据 | 结果 |
|---|---|
| 完整备份集 | `manifests/backup-set-20260819-074357.json` |
| 必需角色 | 10/10，本机完整性验证通过 |
| R2 异地校验 | 上传后重新下载并逐件 SHA-256 校验，10/10 通过 |
| AreaSong Ops SQLite 一致性快照 | `/var/lib/areasong-ops/snapshots/ops-20260819T074334Z.db` |
| 部署回滚点 | `/var/backups/ops/deployments/areasong-ops-20260819T074334Z-9bc1806ef0d0276f7efc7e8663cb3dc364773d40` |

fresh backup 后 `backup_set_last_success_timestamp` 与
`backup_set_r2_verify_last_success_timestamp` 均在本轮验收窗口内，原先接近 24 小时 RPO 的四条备份 Warning 已清除。

## 3. AreaSong Ops 生产部署

运行时目标 revision：`8961cafcea1ab013bd974825a3554b076bff7249`。

部署前门禁：

- 12 项适配器测试通过；
- 生产 revision 对应适配器 ShellCheck 通过；
- `CGO_ENABLED=0 go test ./...` 通过；
- Web `npm ci`、lint、typecheck、build 通过；
- Runner/Web 均从固定 revision 构建；
- 部署前回滚制品 10 项 SHA-256 复核通过。

运行态：

| 组件 | 当前身份 | 运行结论 |
|---|---|---|
| `/opt/ops` | `main` 包含 `c98c3fb` 与本次收口提交，工作树干净 | PR #350 及阶段 9 文档收口后的 GitHub 主线 |
| Runner | `phase9@8961caf...` | active/enabled，`NRestarts=0`，退出码 0 |
| Web | `areasong-ops-web:8961caf...`；image ID `sha256:272f05de...` | healthy，非 root `65532:65532`，只读 rootfs，非 privileged，无 Docker Socket、OOM 或重启 |
| 其他业务容器 | AreaForge、Sub2API 及其数据库/Redis 的容器 ID、启动时间未变化 | 本轮只重启 Runner、只重建 Web |

`services/areasong-ops/deploy/preflight.sh runtime` 通过；自动回滚未触发。本轮 PR 合并后只同步 `/opt/ops` 文档和治理代码，未重建 Runner/Web，故源码 revision 与已部署制品 revision 有意不同。

## 4. 生产灰度验收

### 4.1 控制面 API 与状态恢复

- `/v1/services`：2 个服务，AreaForge `1.1.2`、Sub2API `0.1.173`，状态检查均成功，无活动任务；
- `/v1/objects`：4 个受管对象，状态检查均成功；
- `/v1/automatic-tasks`：`docker-metrics`、`runtime-snapshot` 均启用、健康且按每分钟 cron 运行；
- `/v1/alerts`：控制面映射返回正常；
- 凭据摘要：GitHub 告警同步 Token 已配置，到期日 `2027-08-12`，响应不含 secret；
- tasks、audit、plans 与 task-events 的分页、offset、`hasMore` 契约通过；
- SSE 事件流返回 `text/event-stream`；
- 缺失内部 actor 的 Runner 请求与缺失 Access 身份的 Web API 请求均返回 401。

API 响应、Runner 日志、Web 日志和 SQLite 全表文本值的 Token/凭据模式扫描均为 0。Web 源码未使用 `localStorage`、`sessionStorage`、IndexedDB 或 cookie 写入，CSRF 令牌仅保存在前端运行时内存；按浏览器控制边界未读取任何浏览器存储值。

### 4.2 容器、可观测和入口

- 19 个运行容器全部 healthy，无异常重启、OOM 或 restarting；
- Prometheus active targets `23/23 up`；
- Prometheus、Alertmanager、Loki、Grafana、Blackbox、Nginx 均通过 readiness/health；
- `nginx -t` 通过；
- `ops.areasong.top` 与 `monitor.areasong.top` 的未登录请求均 302 到 Cloudflare Access；
- Runner/Web 部署后无 warning、error、fatal 或 panic 日志；
- auditd active/enabled，`lost=0`，7 个规则 key 齐全；审计日志到 Loki 的探针成功。

### 4.3 备份与恢复证据

- fresh manifest 与 R2 回读验证均新鲜；
- `areasong_ops_restore_drill_last_success_timestamp_seconds` 存在，最近一次隔离恢复演练仍在 35 天门限内；
- 本轮没有恢复生产数据库，也没有用备份覆盖运行态。

### 4.4 当前一条真实告警

验收时存在一条 `SyntheticSloBudgetExhausted`：

- 对象：`resume-jadeai / public-home`；
- 当前合成 journey 值为 `1`，服务和容器当前健康；
- 30 天可用性约 `99.8111%`，覆盖率约 `95.2778%`，历史错误预算确已耗尽；
- 告警从 2026-08-19 05:39 UTC 起在 Prometheus firing，早于 07:59 UTC 的阶段 9 部署；
- Alertmanager 中为 active，邮件通知失败计数为 0；GitHub Issue 同步最近一次成功，`active=1`、`updated=1`。

结论：这是被正确揭示的历史 SLO 债务，不是阶段 9 回归。保留告警和通知闭环，随 30 天滚动窗口自然恢复或按业务事件单独复盘，不静默、不篡改 SLO。

### 4.5 Cloudflare Access 与控制面页面

- 未登录访问 `ops.areasong.top` 返回 302 并进入 Cloudflare Access；
- 通过邮箱 OTP 登录后回到 `https://ops.areasong.top/`，页面身份明确为 `song80184@gmail.com`；
- 页面标题为 `AreaSong Ops`，实时连接正常，操作总览、服务操作、自动任务、凭据轮换、执行记录和变更审计入口可见；
- AreaForge `1.1.2`、Sub2API `0.1.173` 与两个执行门禁正常展示；
- Web 源码全量检索未发现浏览器持久化 API，浏览器控制过程未读取 cookie、local/session storage 或其值。

页面保留 4 条阶段 9 部署前的 Sub2API `restore-drill` `failed_recoverable` 历史记录。逐条可见证据均为：在 `preflight` 因旧适配器“契约身份不匹配”终止、后续阶段未开始、生产变更为“无变更证据”。这些记录作为审计轨迹保留，不删除、不篡改；当前 Sub2API inspect、版本检查和服务状态成功。因本轮明确不恢复业务数据库，也不为清空历史状态重跑恢复。

### 4.6 PR 合并与生产仓库同步

- PR #350 已在 GitHub 合并并关闭，合并提交为 `c98c3fb12b2cb38bd16eb7ee6827d1df4ad427eb`。
- 生产 `/opt/ops` 已从 `8961caf` 快进到 `c98c3fb`，随后同步本次文档收口提交，`main...origin/main`，工作树无改动。
- 同步范围仅为 Git 文档和治理代码；没有重启业务容器、没有重建业务服务，也没有恢复数据库。
- 最终验收脚本已修正为分别校验源码 `c98c3fb` 与运行时 Runner/Web `8961caf`，避免把文档同步误报为运行身份漂移。

### 4.7 最终验收结果

2026-08-19 10:23:28 UTC 以源码与运行时身份分离后的验收口径重跑：

- `phase9_acceptance=PASS`，`failures=0`，`warnings=1`；
- 源码 `c98c3fb...` clean，Runner/Web `8961caf...` 身份、systemd、只读 rootfs、非 root、无 Docker Socket 均通过；
- 19 个容器 healthy、无重启/OOM，Prometheus `23/23 up`，控制面 API、分页、SSE、Access、审计和敏感信息扫描全部通过；
- 唯一 Warning 为部署前已存在的 `SyntheticSloBudgetExhausted`，保持 firing 以揭示历史 SLO 债务，未被静默或改写。

## 5. 无破坏入侵响应桌面演练

演练场景：假设同时出现陌生 root 进程、异常外连和未知 `authorized_keys`。

本演练只走决策与门禁，没有实际隔离、快照、轮换、重装或恢复。

### 5.1 发现与定性

1. 以 Prometheus/Alertmanager、Loki、auditd、Fail2ban、Nginx 和主机进程/连接快照作为交叉信号；不以“重启后消失”作为排障方式。
2. 立即建立 UTC 时间线，区分入侵入口、权限级别、数据访问、持久化、横向移动和备份污染范围。
3. 证据只读导出到机器外，记录 SHA-256；不从可疑主机复制可执行文件到新环境运行。

### 5.2 隔离决策

标准动作是保留管理来源后切断其他入出站，不重启、不关机。当前厂商不提供安全组或云防火墙，因此真实事件优先级为：

1. 联系厂商在宿主/网络侧停止路由并保留管理通道；
2. 厂商无法及时处理时，评估主机侧 UFW 临时隔离，但不把已失陷主机上的防火墙视为可信边界；
3. 启动干净替代机和 DNS/入口切换预案，不继续依赖受影响主机承载业务。

### 5.3 证据保全

厂商没有快照能力，不能声称已具备云盘快照取证。补偿流程：

- 外部收集进程、连接、登录、timer/cron、审计和近期文件元数据；
- 每份证据生成 SHA-256、记录 UTC 时间和采集人；
- 写入独立受控存储，禁止覆盖；
- 业务备份与取证副本分开，避免把可疑可执行内容带入恢复集。

### 5.4 凭据全轮换计划

一旦确认主机失陷，以下类别全部按已泄露处理：SSH key/信任关系、Cloudflare Access/API/R2、GitHub deploy key/PAT、SMTP、PostgreSQL、Redis、Grafana/应用管理员、x-ui/xray、TLS 私钥和所有应用 secret。

轮换顺序为：先建立干净控制面和逃生通道，再轮换外部控制面与备份凭据，然后数据组件与应用凭据，最后撤销旧凭据并验证旧凭据不可用。任何 secret 均不进入聊天、Git、命令行历史或演练记录。

### 5.5 重建与恢复门禁

- 默认重装，不在已获 root 权限的主机上“清理后继续用”；
- 新机按 standards/09 基线初始化，从受控 Git 和固定 digest 重建；
- 只恢复经过完整性验证的数据与配置，不恢复来自受侵主机的可执行文件；
- 所有凭据完成轮换并验证旧凭据失效；
- 业务 health、关键路径、19 个容器、23 个 Prometheus target、日志/告警、fresh backup 与 R2 回读全部通过后才能切流；
- 留存事件时间线、根因、影响范围、恢复点和 48 小时复盘。

演练结论：流程可执行；厂商缺少网络侧隔离与快照仍是结构性限制，当前以厂商人工协作、外部取证、默认重装和异地备份补偿，不能表述为等价于安全组与云快照。

## 6. 附录 A 最终分类

| # | 标准项 | 最终分类 | 当前证据 / 边界 | 后续触发条件 |
|---:|---|---|---|---|
| 1 | 多 AZ、到期计费、抢占式治理 | 风险接受 + 用户暂缓 | 当前单机；账单/到期暂缓 | 有 SLA、预算或第二台机器时立项 |
| 2 | UTC、时间同步、目录分离 | 已完成（数据盘除外） | UTC、NTP、应用与日志路径正常 | 数据增长后迁移独立数据盘 |
| 3 | 独立数据盘、UUID、容量/inode | 部分完成 / 风险接受 | UUID 与监控已完成；仅系统盘 | 有数据盘与维护窗口时迁移 |
| 4 | 软件源、GPG、禁止 curl\|bash | 已完成当前核对 | 受控仓库未发现违规安装链 | 月度审计 |
| 5 | 命名、台账、到期/欠费 | 部分完成 / 用户暂缓 | 台账完整；主机名保留；账单暂缓 | 维护窗口或用户重启该专项 |
| 6 | VPC/安全组/公网入口/IPv6 | 厂商限制 + 补偿控制 | 无私网/安全组；UFW、端口收敛有效 | 更换厂商或新增网络控制面 |
| 7 | SSH、账号、MFA、最小权限 | 已完成基础项 / 风险接受 | 禁 root/password；密钥登录；22 未限源 | 固定出口 IP 后限源 |
| 8 | 防火墙默认拒绝、数据组件不公网 | 已完成 | 公网仅主要 22/80/443；数据组件内网 | 持续端口漂移审计 |
| 9 | 域名、证书、Nginx | 已完成 | 证书监控、Cloudflare 台账、`nginx -t` 通过 | 持续续期观察 |
| 10 | AppArmor / auditd | 已完成当前单机基线 | auditd active/enabled、`lost=0`、7 keys、Loki 探针成功 | 持续月度审计 |
| 11 | secret 与凭据生命周期 | 已完成当前闭环 | root-only 0600；GitHub Token 到期 2027-08-12；网页类型化轮换 | 到期前轮换并撤销旧 Token |
| 12 | 入侵响应与全量轮换 | 已完成文档与桌面演练 | 无破坏演练完成；厂商隔离/快照能力有限 | 年度重演；真实事件按流程执行 |
| 13 | 服务路径、自启、健康 | 已完成 | Runner/Web 与 19 个容器健康；23 targets up | 持续巡检 |
| 14 | Compose、日志、镜像追溯 | 已完成当前运行态 | digest、日志上限、端口、加固均有 root 证据 | 每次升级同样复核 |
| 15 | 数据库/Redis 专属账号与无 DDL | 部分完成 / 应用待配合 | Redis ACL 已持久化；Sub2API 启动 migration 仍需管理权限 | 上游提供 migration-only/关闭自动 migration |
| 16 | 配置 Git 化与 IaC | 已完成 / IaC 不适用 | `/opt/ops` Git 化且与主线一致 | 多机后评估 Ansible/Terraform |
| 17 | 制品追溯与 CI 最小权限 | 部分完成 | 当前 digest 与发布记录可追溯；通用供应链未全量建设 | 新 CI/CD 项目单独立项 |
| 18 | 变更五要素、回滚、一次一变更 | 已完成 | fresh backup、回滚点、单服务灰度均执行 | 持续执行 |
| 19 | 备份失败告警与脚本入 Git | 已完成 | 十角色 manifest、R2 回读、指标和告警在线 | 持续 RPO 观察 |
| 20 | 日志上限与长期审计 | 基本完成 / 防篡改待增强 | journald/logrotate/Docker/Loki 完成；同机 Loki 非 WORM | 有合规要求时做独立对象锁归档 |
| 21 | exporter、探活、证书、通知 | 已完成当前运行态 | 23/23 up；通知失败 0；GitHub Issue 同步成功 | 继续处理真实 SLO 告警 |
| 22 | OOM 归因 | 未触发 / 流程已具备 | 当前 OOM 0 | 真实事件按模板复盘 |
| 23 | 核心服务无单点 | 单机风险接受 | 异地备份与恢复演练补偿 | 有 SLA/预算时做 HA |
| 24 | 异云备份与恢复演练 | 已完成单机模型 | 本机/R2/隔离恢复均有证据 | 有第二台机器时做跨机实演 |
| 25 | 安全更新与 CVE | 已完成 | 自动安全更新和周镜像扫描在线 | 持续补丁日 |
| 26 | 止血优先与复盘 | 文档完成 / 事件触发 | 流程与模板具备 | 真实 P0/P1 后 48 小时复盘 |
| 27 | 环境隔离、灰度、回切 | 部分完成 / 单机风险接受 | 控制面灰度和回滚完成；无第二生产环境 | 多环境时补云侧隔离 |
| 28 | 离职回收、退役、AI 批准 | 文档完成 | root-only 与共享终端授权执行有效 | 按事件执行 |

## 7. 完成门禁

- [x] root 级运行态快照与逐项对账；
- [x] Token 到期日、Sub2API digest、容器与 auditd 证据回写；
- [x] AreaSong Ops Runner/Web 与 `origin/main@8961caf` 一致；
- [x] fresh backup、本机校验、R2 回读和回滚点；
- [x] API、分页、自动任务、凭据摘要与敏感信息非持久化检查；
- [x] 容器、目标、入口、日志、告警与通知链灰度；
- [x] 无破坏入侵响应桌面演练；
- [x] Cloudflare Access 已登录邮箱、主页面与前端非持久化契约终验；
- [x] PR #350 合并、生产 `/opt/ops` 同步且工作树干净；源码 `c98c3fb` 与运行时制品 `8961caf` 分离核对。

当前结论：**阶段 9 已完成。** 生产运行态、控制面、可观测性、Access、备份、审计、演练与文档同步均有证据；唯一保留的是部署前已存在且未被静默的 `SyntheticSloBudgetExhausted` 历史 SLO 告警，不构成阶段 9 回归。
