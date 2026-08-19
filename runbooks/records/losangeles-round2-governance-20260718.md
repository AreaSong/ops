# LosAngeles 第二轮服务器治理

日期：2026-07-18
范围：LosAngeles 单机生产服务器
状态：**本轮已批准范围已于 2026-07-29 完成生产发布与回验（100%）**。Account Vault GHCR/migration 发布是独立高风险专项；未来增强见 [losangeles-optimization-roadmap-20260721.md](losangeles-optimization-roadmap-20260721.md)。

## 1. 目标与边界

本轮在现有 Docker Compose 单机架构上补齐可视化资产、日志与流量审计、配置漂移、容器加固、外部控制面、发布供应链和告警闭环。继续使用 Docker Compose，不升级 Kubernetes，不增加第二台服务器。

验收窗口只使用 30、60、120 分钟。24、48、72 小时验收已按用户要求取消，不得重新作为完成门禁。

## 2. 权威状态

### 2026-07-21 刷新（只读回验）

| 项目 | 结论 |
| --- | --- |
| 资产 / 漂移 / cron / 业务日志指标 | 生产 textfile 新鲜；`ops_config_drift > 0` 无序列 |
| Alertmanager → GitHub Issue | 已启用；当时记录的 PAT 到期日为 2026-08-19，已于 2026-08-19 完成轮换，当前到期日 2027-08-12 |
| Loki retention | 720h / 30 天（非原计划 7 天） |
| Cloudflare-only 源站 | `resume`/`sorryiossearch`/`monitor`/`forge` 已生效；台账 `observed_origin_policy` 已晋升 |
| Cloudflare Access（Grafana） | **2026-07-29 已完成**：仅允许指定邮箱 OTP（6 小时），GitHub 专用 service token 正常探针与故障/恢复演练通过 |
| Grafana / Prometheus 收口 | PR #87、#88 与 main CI #67、#69 成功；生产 reload、标签契约、五张修改看板及最终运行门禁均通过 |
| Account Vault GHCR digest 发布 | 独立高风险专项，仍按原门禁单项推进，不属于本轮完成判定 |

### 2026-07-18 原表（历史）

| 项目 | 本地实现 | 本地验证 | 生产部署 | 生产回验 |
| --- | --- | --- | --- | --- |
| 统一资产清单与运行快照 | 已完成 | 已通过 | 待部署 | 待验证 |
| 域名、Nginx、端口、容器映射面板 | 已完成 | 已通过 JSON/规则检查 | 待部署 | 待验证 |
| 四业务 warning/error 脱敏日志 | 已完成 | Observability 套件与专项脱敏测试通过 | 待部署 | 待真实日志抽检 |
| Cloudflare 官方 IP 漂移检查 | 已完成 | 单元测试通过 | 待部署 | 待验证 |
| Observability 定时任务纳管 | 已完成 | Ansible/cron 测试通过 | 待部署 | 待验证新鲜度 |
| Alertmanager critical 告警 GitHub Issue 闭环 | 已完成 | 失败/恢复/去重测试通过 | 待 Token 与 cron | 待真实演练 |
| Cloudflare Access 邮箱 OTP 与服务令牌 | 不在仓库内 | 策略和凭据边界已核对 | 已创建 | OTP、6/6 外部探针和故障/恢复 Issue 生命周期均已验证 |
| Cloudflare-only 源站限制 | 事务式 playbook 已完成 | 本地结构测试通过 | 待单项确认 | 待 CDN/直连双向验证 |
| Loki 7 天 retention | 配置已完成 | Loki 3.1 配置验证通过 | 待单项确认 | 待真实删除证据 |
| Account Vault GHCR digest 发布 | 已完成 | 本地门禁通过；代码变化后须重新跑真实镜像/安全扫描 | 待 CI/GHCR 与单项确认 | 待发布/回滚演练 |
| AreaForge 受控 Compose | 已按生产文件只读采集 | Compose 与 inventory 校验通过 | 待同步 `/opt/ops` | 待漂移为零 |
| 运维文档、台账与 Governance CI | 已纳管 | Runbook 全索引、链接、YAML/JSON 与工作流安全测试通过 | 待提交并运行真实 CI | 待 Git/生产一致性验证 |

## 3. 已取得的本地证据

- Observability Python：35 项通过。
- Ansible：26 项通过。
- Inventory：5 项通过。
- Account Vault 发布脚本：21 项通过，包含失败后恢复 Compose、原子 generation 状态、provenance/SBOM/Trivy 三类 attestation fail-closed、候选 digest 留痕、R2 同一 manifest、角色分离门禁和 1 项真实 PostgreSQL 15 权限集成测试。
- 备份、R2、归档与隔离恢复：47 项通过；Runbook 索引和链接：2 项通过。
- Account Vault 供应链：15 项 Node 24 测试与 Actionlint 通过；expand-only migration gate 覆盖 3 个 Prisma migration。
- 既有 Account Vault amd64 候选镜像证据只覆盖补强前代码；当前提交必须由 GitHub CI 重新构建、扫描、生成 SBOM、attestation 和 RepoDigest。
- 容器 smoke：管理角色执行三条 migration，`account_vault_app` 运行 Web、无法创建表、可读取业务表，`/ready`、`/api/auth/status`、首页和 Docker health 均通过。
- Trivy：HIGH 0、CRITICAL 0、MEDIUM 1，无例外。
- CycloneDX：规范 1.7，268 个组件。

以上仅证明当前本地工作树，不是远端 commit、GitHub CI、GHCR RepoDigest 或生产运行证据。

2026-07-18 外部控制面只读核查：`AreaSong/ops` 与 `AreaSong/sorryiosSearch` 均未配置 classic branch protection；`AreaSong/ops` 当前没有 Actions secrets；`sorryiossearch` GHCR package 页面不存在，需首次成功 publish 后再确认 private 与访问控制。

## 4. 变更顺序

1. 完成 root 只读审计，确认 Docker、Nginx、Loki、AreaForge、`/opt/ops` 和 secret 权限。
2. AreaForge 真实 Compose 已完成只读采集；统一 runtime 路径并纳入漂移监控。
3. 完成文档、全量测试、配置解析和 diff 检查。
4. 提交并推送 `ops` 与 Account Vault 分支，运行真实 GitHub CI。
5. 确认 GHCR 私有、配置生产 packages-read 凭据，取得 published release manifest 与 RepoDigest。
6. 已创建 Grafana Cloudflare Access Application、指定邮箱 OTP allowlist、6 小时会话和 service token，并写入 GitHub Actions secrets；2026-07-29 验收通过。
7. 创建最小权限 GitHub Issues token，部署 Alertmanager Issue 同步，完成 failure/recovery 演练。
8. 部署低风险 observability、cron、Dashboard 和容器加固变更。
9. 分别确认并执行 Cloudflare-only 源站限制、Loki 7 天删除和 Account Vault migration/digest 发布。
10. 权威回验 Cloudflare-only 后，将四条路由的 `observed_origin_policy` 晋升为 `cloudflare-only`，提交并同步 inventory，再确认运行态零漂移。
11. 执行 30、60、120 分钟验收和最终逐项证据审计。

## 5. 单项高风险门禁

| 变更 | 影响 | 验证 | 回滚 |
| --- | --- | --- | --- |
| Cloudflare-only 源站限制 | 代理域名的非 Cloudflare 请求返回 403 | Cloudflare 公网路径可用；源站 IP 直连拒绝；`nginx -t` | playbook 自动恢复快照并 reload |
| Loki 7 天删除 | 超过保留期的 Loki 日志永久删除 | compactor/retention 指标、磁盘和时间边界抽检 | 关闭 retention；已删除数据不能恢复 |
| Account Vault migration | 生产 schema 可能变化 | 精确本机/R2 backup manifest、expand-only CI、角色权限、`/ready` 与业务接口 | 恢复旧镜像与 Compose；不自动 schema downgrade；必要时另行确认数据库恢复 |

三项必须分别取得明确确认，不能合并为一次长期授权。

## 6. 生产验收记录

以下为本轮已批准范围的最终权威证据：

- [x] 运行配置基线为 `4fa7844e35cfa44155a7317260f4495073ad02be`；收口文档及证据修正均通过 PR/main 双重 CI，生产 `/opt/ops` 与最新 `origin/main` 一致且工作树干净；纯文档同步未触发 reload 或容器重建。
- [x] 18 个生产服务容器均 running；无 unhealthy、异常重启或 OOM。既有退出的 `areaforge-ops006-tools` 为一次性工具容器，不是生产服务。
- [x] 12 张 Grafana 看板桌面端逐张加载，关键 panel 无 `No data`、查询错误或插件缺失（2026-07-29）。
- [x] 12 张 Grafana 看板以真实 `426×632` 移动视口逐张加载，横向溢出、裁切、查询错误和 `No data` 均为 0（2026-07-29）。
- [x] 5 张本次修改看板在生产 Access 登录态再次加载，共渲染 50 个 panel region，无查询错误、`No data` 或 Access 重定向。
- [x] `ops_config_drift` 对受控 Compose 和 Nginx 路由总和为 0。
- [x] 四个业务日志源均有采集游标，collector 成功且 source failure 为 0；真实样本的邮箱、IP、Bearer、UUID、JWT 和敏感赋值原文命中均为 0。
- [x] Alertmanager 受控故障由工作流 #192 创建唯一 Issue #86，工作流 #193 恢复后自动关闭；历史生产故障 Issue #5 也已恢复关闭。
- [x] Grafana 浏览器访问触发邮箱 OTP；service token 外部探针成功（2026-07-29，工作流 #191）。
- [x] Loki 30 天 retention 有运行证据：marker 队列和 pending delete request 均为 0，原积压 46 个 marker 已处理（2026-07-29）。
- [x] 30、60、120 分钟窗口由连续 2 小时 Prometheus 数据覆盖：down target、firing 告警、规则评估失败和通知错误均为 0；仅有 1 个 15 秒 `AppHttpProbeSlow` pending 采样，随后恢复且未 firing，已解释。
- [x] 最终日报覆盖 2026-07-28 UTC：数据源失败 0、critical/warning 0、邮件 attempted/accepted 均为 1；本机、R2、完整 9 件套与 R2 独立回读均在 RPO 内。
- [x] PR #87、#88 已合并，main 治理 CI #67、#69 成功；Prometheus reload HTTP 200，配置与 5 个规则文件有效，Grafana `component` 11 条且旧 `exported_service` 0 条；生产 API 返回 12 条记录规则且全部 `health=ok`。

以上复选项均有权威证据，本轮可标记 100% 完成。

Account Vault 新 DB-readiness 镜像、GHCR RepoDigest 与 migration 发布继续作为独立高风险变更；GitHub required checks、PAT 轮换和第二台机器等属于后续治理或未来增强，不能倒推为本轮未完成。
