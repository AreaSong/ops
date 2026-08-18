# LosAngeles standards/09 阶段 9 当前验收记录

日期：2026-08-19 UTC
服务器：LosAngeles（23.185.200.12）
状态：**进行中：基线复核已完成，生产变更项按分类处理**
依据：`standards/09-server-ops-handbook.md` 31 章与附录 A  以及 [历史验收矩阵](losangeles-standards-09-audit.md)

## 1. 本轮目标与证据边界

本记录用于把阶段 9 的每个标准项明确归类为：

- **已完成**：有当前生产证据，且无需额外条件；
- **风险接受**：当前单机模型下有明确边界和补偿控制；
- **维护窗口**：需要停机、重启、迁移或其他生产变更；
- **应用待配合**：需要上游应用提供配置或迁移能力；
- **厂商限制 / 用户暂缓**：服务器或云控制面无法提供，或用户明确暂缓；
- **待补证据**：实现可能存在，但当前证据不足，不能宣称完成。

本轮先执行只读核对，不改变 SSH、主机名、数据盘、数据库权限、Redis 用户、Docker daemon 或云资源。

## 2. 2026-08-19 当前生产事实

| 检查项 | 当前事实 | 证据结论 |
|---|---|---|
| 主机与系统 | `LosAngeles`；Ubuntu 24.04.4 LTS；内核 `6.8.0-136-generic` | 非 EOL，运行正常 |
| 时间 | `Etc/UTC`；NTP synchronized；`unattended-upgrades` active/enabled | 已完成 |
| 存储 | 仅 `/dev/sda1`，约 58G；已用约 41G（71%） | 独立数据盘仍是结构性缺口 |
| 网络监听 | 公网主要为 TCP 22/80/443；业务、数据库、监控服务为本机或容器网络 | 端口收敛仍有效；SSH 来源仍为 Anywhere |
| 安全服务 | `fail2ban` active；`auditd` active/enabled | auditd 服务状态已确认，规则与事件管道仍需 root 级抽查 |
| 容器运行 | 当前 SSH 用户无 Docker socket 权限，未绕过 root-only 边界 | 容器镜像/重启/OOM 需 root 级证据 |
| Prometheus | readiness 200；active targets 23 个且全部 `up`；firing alerts 0 | 当前观测栈健康 |
| Alertmanager / Loki | readiness 分别为 200 / 200 | 当前通知与日志入口健康 |
| Grafana | HTTP API `/api/health` 200；版本 11.6.16 | 当前 Grafana 健康 |
| Sub2API | `http://127.0.0.1:8080/health` 返回 200、`{"status":"ok"}` | 业务健康；运行镜像 digest 尚需 root 级核对 |
| AreaForge / Ops | AreaForge Web 3020 返回 200；Ops 3200 返回 200 | 入口可达；页面级 Access 仍按专门验收记录判断 |
| 代码基线 | GitHub `origin/main=8961caf`，PR #336/#337/#338 已进入主分支；生产 `/opt/services/areasong-ops/.env` 与 `/healthz` 均为 `9bc1806` / `phase6-prebuild` | **生产控制面部署漂移**：代码已合并但 Web/Runner 尚未切到最新主线 |

## 3. 附录 A 逐项分类

| # | 标准项 | 当前分类 | 当前证据 / 边界 | 下一步 |
|---:|---|---|---|---|
| 1 | 多 AZ、到期计费告警、抢占式实例治理 | 风险接受 + 用户暂缓 | 当前单机；账单/到期按用户要求暂缓；未发现第二台实例 | 有 HA 或账单需求时单独立项 |
| 2 | UTC、时间同步、系统/数据/日志/应用分离 | 已完成（数据盘除外） | UTC、NTP、应用与日志路径已确认；无独立 `/data` | 数据增长后维护窗口迁移 |
| 3 | 独立数据盘、UUID+nofail、磁盘/inode 监控 | 部分完成 / 风险接受 | UUID 与容量/inode 监控已有；只有系统盘 | 规划独立 `/data` 并做迁移演练 |
| 4 | 软件源、GPG、禁止 curl|bash | 已完成 | 受控仓库已有检查，未发现明显违规安装链 | 月度审计持续执行 |
| 5 | 命名、台账、到期/欠费双告警 | 部分完成 / 厂商限制 | 台账已补 owner/provider/region；主机名保留 `LosAngeles`；账单能力未启用 | 维护窗口改名；云侧能力变化后补告警 |
| 6 | VPC/安全组/公网入口/IPv6 | 部分完成 / 厂商限制 | 主机无 RFC1918 私网；UFW 与 22/80/443 收敛有效；云厂商无安全组能力 | 保持主机补偿控制 |
| 7 | SSH、账号、MFA、最小权限 | 已完成基础项 / SSH 来源风险接受 | root/password 禁用、密钥登录、云主账号 MFA；22 尚未限源 | 固定出口 IP 后收敛来源 |
| 8 | 防火墙默认拒绝、数据组件不公网 | 已完成 | 当前公网监听核对符合 22/80/443；数据组件本机/容器网络 | 持续做端口漂移审计 |
| 9 | 域名、证书、Nginx 流程 | 已完成 | 证书监控、Nginx 配置检查和 Cloudflare 台账已有 | 持续观察续期 |
| 10 | AppArmor / auditd | 基本完成 / 待补证据 | AppArmor 与 auditd 服务 active/enabled；规则 key、lost、Loki 管道未做本轮 root 级抽查 | 只读 auditd playbook 验收 |
| 11 | 凭据不入 Git、secret 600 | 已完成 | 既有备份/SMTP/Access 边界与 root-only 约束已记录 | Token 到期日需重新读取并更新台账 |
| 12 | 入侵响应与凭证全轮换 | 文档完成 / 演练待做 | runbook 已存在，未做桌面演练 | 无破坏桌面演练 |
| 13 | 服务路径、自启、健康检查 | 已完成基础项 / 容器证据待补 | systemd、健康端点、Prometheus targets 正常；容器清单需 root 权限 | root 级运行态快照 |
| 14 | Compose、日志上限、镜像可追溯 | 基本完成 / digest 待补证据 | daemon/Compose 日志策略已有记录；当前镜像 digest 未从 root 运行态重读 | root 级 `docker inspect` |
| 15 | 数据库/Redis 专属账号与无 DDL | 部分完成 / 应用待配合 | Redis 阶段 1 ACL 已持久化；Sub2API 仍需启动 migration，不能直接切 runtime 低权限 | 上游提供 migration-only 或关闭自动 migration 后再切换 |
| 16 | 配置 Git 化与 IaC 审阅 | 已完成 | `/opt/ops` Git 化，主线 8961caf 已同步 | 保持提交/回滚纪律 |
| 17 | 制品追溯与 CI 最小权限 | 部分完成 | digest 与控制面发布记录已有；通用 CI/CD 制品链仍不完整 | 另立 CI/CD 供应链专项 |
| 18 | 变更五要素、回滚、一次一变更 | 已完成 | runbook、fresh backup、回滚点和阶段门禁已使用 | 持续执行 |
| 19 | 备份失败告警与脚本入 Git | 已完成 | 本机/R2/应用级恢复演练及告警链已有 | 持续检查 RPO |
| 20 | 日志上限与 180 天审计留存 | 基本完成 / 防篡改归档待增强 | journald、logrotate、Docker、Loki 基线已有；同机 Loki 不等于 WORM | 有合规要求时接入独立归档 |
| 21 | node exporter、探活、证书、告警可达 | 已完成当前运行态 | 23 targets up、firing 0、Grafana/Alertmanager/Loki readiness 200 | 持续噪声复盘 |
| 22 | OOM 归因 | 未触发 / 流程待验证 | 当前无已知 OOM；已有 runbook | 发生事件时按模板复盘 |
| 23 | 核心服务无单点 / 主从监控 | 单机风险接受 | 当前只有一台主机；备份与恢复补偿有效 | 有 SLA/预算后做 HA |
| 24 | 异云备份与恢复演练 | 已完成 | 本机、R2、隔离 Postgres、应用级演练均有记录 | 有第二台机器时做跨机实演 |
| 25 | 自动安全更新与 CVE 流程 | 已完成 | `unattended-upgrades` active/enabled；CVE 流程已记录 | 持续补丁日 |
| 26 | 止血优先与复盘 | 文档完成 / 真实事件待触发 | standards 与 postmortem 模板已存在 | 真实 P0/P1 后 48h 复盘 |
| 27 | 环境隔离、灰度、回切点 | 部分完成 / 单机风险接受 | 控制面有灰度和回滚；当前无第二生产/测试环境 | 多环境时补云侧隔离 |
| 28 | 离职回收、退役、AI 变更批准 | 文档完成 | 流程、root-only 和共享终端授权已记录 | 按事件执行 |

## 4. 当前仍不能宣称完成的证据

1. root-only Docker 运行态（镜像 digest、异常重启、OOM、所有容器健康）尚未在本轮读取。
2. auditd 的规则 key、`lost=0`、审计管道到 Loki 的新鲜度尚未在本轮读取。
3. GitHub Token 实际到期日与生产凭据路径尚未从服务器重新读取；台账中的 `2026-08-19` 视为疑似滞后，不能据此判断轮换失败。
4. Sub2API 当前实际版本与 `/opt/services`、`/opt/ops` 两份 Compose 的 digest 一致性尚未在 root 级读取。
5. 生产 AreaSong Ops Web/Runner 仍报告 `phase6-prebuild@9bc1806`，与 `origin/main=8961caf` 不一致；需单独走部署门禁。
6. 独立数据盘、SSH 来源限制、主机名、单机 HA、Redis 分用户 ACL、Sub2API runtime 低权限均不是本轮无确认可直接完成的项目。

## 5. 阶段 9 完成门禁

阶段 9 只有在以下条件满足后才能关闭：

- [ ] 完成一次 root 级只读运行态快照，并与本矩阵逐项对账；
- [ ] 将 Token 到期日、Sub2API digest、容器清单和 auditd 规则写回台账；
- [ ] AreaSong Ops Web/Runner 运行身份与 `origin/main` 一致，并完成生产灰度验收；
- [ ] 对维护窗口、应用配合、厂商限制和用户暂缓项保留明确决策记录；
- [ ] 做一次无破坏入侵响应桌面演练；
- [ ] 运行全量健康、告警、备份新鲜度和恢复证据检查；
- [ ] 生产仓库、文档、台账与 `origin/main` 一致，工作树干净。

当前结论：**阶段 9 尚未关闭，当前完成度为“基线审计完成，证据补齐和最终门禁待完成”。**
