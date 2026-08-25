# AreaSong Ops 生产变更包草案

> 状态：草案，仅用于评审和审批准备。本文不代表生产已部署，也不授权执行任何生产写操作。
>
> 目标：在一个明确维护窗口内，以单服务灰度方式启用控制面第一阶段能力；所有写入、流量变化和服务状态变化仍须在窗口内逐项确认。

## 1. 变更摘要

| 项目 | 内容 |
| --- | --- |
| 变更对象 | `areasong-ops` Runner、Web、受信适配器、schema 4 服务目录 |
| 首批业务目标 | AreaForge 单服务生命周期计划流；Sub2API 只读核对 |
| 目标主机 | 生产 inventory 中明确登记的单台服务器；以 `serverId` 和 Runner 租约为准 |
| 变更类型 | L2：控制面组件发布和单服务受控生命周期验证 |
| 维护窗口 | 待审批人填写 |
| 变更负责人 | 待填写 |
| 第一批准人 | 待填写 |
| 第二批准人 | 待填写；不得与高风险操作人相同 |
| 目标 Git commit | 待填写；必须是已通过本地门禁的 40 字符 commit |
| 观察窗口 | 每个成功任务按计划中的 `observationSeconds` 单独记录 |

## 2. 本包范围

本包只覆盖以下能力：

1. Runner/Web 安装或更新到同一批准 commit。
2. schema 4 服务目录、Fleet 单机登记、Access/RBAC 只读核对。
3. AreaForge `inspect`、`check` 和生命周期计划创建、审批、执行、健康检查、审计。
4. AreaForge 首次生产生命周期动作只允许单服务、单批次、单 Runner。
5. Sub2API 只执行 `inspect` 和 `check`，不执行更新、停止、恢复或数据库操作。

## 3. 明确不在本包执行

- 自动更新、批量作业、跨租户或跨服务器执行。
- Compose `apply`、宿主文件修改、网页终端和 Break-glass Shell。
- 生产数据库、卷、快照恢复，或任何 `production` recovery plan。
- Kubernetes apply、rollout、回滚和集群级操作。
- 扩展启用、插件安装、Runner 自更新和凭据轮换。
- 修改 Prometheus、Grafana、Loki、Alertmanager 规则或静默策略。
- 任何以 `ops.areasong.top` 为流量控制目标的操作。

未列出的能力保持关闭或只读；不得用本包的批准替代后续专项批准。

## 4. 变更前置条件

以下条件全部满足后，才能进入窗口：

- [ ] 变更编号、窗口、操作人、两名批准人和沟通渠道已填写。
- [ ] 批准 commit、Runner/Web 构建 revision、配置 digest、镜像 digest 已固化。
- [ ] 当前 Runner/Web/Nginx/Compose 身份、SQLite 快照、最近完整备份 manifest 已保存。
- [ ] `/opt/ops` 工作树与批准 commit 一致，无非预期改动。
- [ ] `services.example.json` 对应生产目录已完成租户、服务器、Runner、TrafficPolicy 和适配器核对。
- [ ] Access principal、tenant、role、object scope 和过期策略完成负向核验。
- [ ] Runner mTLS、Socket owner/mode、Web 非 root 和只读 rootfs 通过预检。
- [ ] Alertmanager loopback readiness、Prometheus targets、Nginx `-t` 和 Web/Runner `/healthz` 通过。
- [ ] AreaForge 维护页、traffic policy、drain 超时和回滚文件均已验证可读。
- [ ] 已确认本次不执行生产恢复、批量、Compose apply、Kubernetes apply 和自动更新。

## 5. 分项变更与批准点

### C0：窗口冻结与运行态取证

**影响**：只读；不改变服务、流量或配置。

**执行**：按 `services/areasong-ops/deploy/deploy-checklist.md` 的阶段 0 和部署前清单收集 revision、digest、Socket、容器、备份和告警证据。

**通过条件**：证据完整、时间戳在窗口内、回滚制品可读，且没有活动阻断告警未被确认。

**停止条件**：任一身份、备份、租户、服务器或告警证据缺失，立即停止，不进入 C1。

### C1：安装或更新控制面组件

**影响**：可能短暂影响控制面 Web/API；不应改变 AreaForge/Sub2API 业务流量。

**执行顺序**：先安装/替换 Runner，再验证 Socket 和 `/healthz`；随后只重建 Web，验证非 root、只读 rootfs、revision 一致性。

**验证**：

- Runner systemd 状态、日志和 Socket 权限正常。
- Web 健康检查返回 200，Cloudflare Access/CSRF 行为符合预期。
- `/v1/services`、`/v1/states`、`/v1/fleet`、`/v1/alerts` 只读接口返回正确租户和服务器范围。
- 业务服务的 inspect/check 仍为只读，当前运行身份无漂移。

**回滚**：恢复上一批准 Runner 二进制和适配器；恢复上一 Web image/commit；Nginx 仅在 `nginx -t` 通过后 reload。保留 SQLite、审计和任务证据，不回滚业务数据库。

**停止条件**：Socket 不可用、Web/Runner revision 不一致、Access 越权、业务身份漂移或健康检查失败。

### C2：AreaForge 生命周期单服务灰度

**影响**：AreaForge 可能短暂进入维护页、排空连接并停止/启动应用；公网流量只允许按 TrafficPolicy 受控变化。

**执行顺序**：

1. 创建 `ReleasePlan`，绑定操作者、tenant、AreaForge、`serverId`、TrafficPolicyDigest、幂等键、超时和回滚说明。
2. 由独立批准人核对计划摘要和确认短语；高风险操作禁止自己批准自己。
3. 执行 `stop` 时必须按 `preflight -> drain -> enter-maintenance -> stop -> health`。
4. 只有应用健康且流量检查通过，才允许后续 `start` 计划执行 `resume-traffic`。
5. 失败时保持维护保护，进入 `needs_attention`；不自动暴露 502，不自动恢复业务数据库。

**通过条件**：旧 worker 和活动连接真实归零或达到明确超时；维护页已生效；应用停止/启动状态与计划一致；健康检查、事件、任务终态和审计原子提交。

**回滚**：仅执行计划声明的受控 rollback；恢复前再次核对运行身份、TrafficPolicyDigest 和备份证据。流量恢复失败时保持维护状态，不强行切回公网。

**停止条件**：TrafficPolicy 缺失或摘要漂移、`ops.areasong.top` 出现在 hostname、drain 只依赖 graceful reload、应用不健康却尝试恢复流量、任务终态不确定。

### C3：Sub2API 只读核对

**影响**：只读，不停止、不更新、不恢复、不修改数据库。

**验证**：执行 `inspect` 和 `check`，保存当前 image digest、运行身份、PostgreSQL/Redis 状态、发布发现摘要和告警状态。

**停止条件**：发现身份漂移、迁移状态异常、备份证据过期或告警阻断；不得转为更新或恢复操作。

## 6. 统一停止与回滚规则

- 任一请求重试必须使用同一幂等键；不同请求摘要复用同一键必须拒绝。
- 任务、阶段事件、计划终态、目标状态和审计记录必须原子提交；发现不一致时按“生产可能已改变”处理。
- 任何身份漂移、TrafficPolicy 漂移、Runner 离线、维护页失败、drain 超时或健康检查失败，立即停止后续动作。
- 失败时优先保持维护保护；不通过重启、关闭防火墙、放宽 Socket、任意 Shell 或数据库恢复来“修复”。
- 回滚只恢复本次变更涉及的受控 revision；不执行 schema downgrade，不删除 namespace/PV，不覆盖业务数据库。
- 回滚完成后必须重新执行 health、identity、traffic 和审计核对，并记录实际结果。

## 7. 观察与收口

每个成功计划单独记录以下证据：

- 计划 ID、请求摘要、TrafficPolicyDigest、tenant、server、Runner lease。
- 每个阶段的开始/结束时间、输出摘要、任务和事件 ID。
- 应用健康、公网流量、活动连接、阻断告警和维护静默状态。
- 观察窗口结束时的二次 inspect、告警复核、静默解除和终态提交。

观察窗口内出现阻断告警、身份漂移、健康下降、静默未解除或终态无法确认时，不得标记 `Succeeded`，转为 `NeedsAttention` 并保留全部证据。

## 8. 审批签字栏

| 项目 | 姓名/主体 | 时间 | 签名或审计事件 |
| --- | --- | --- | --- |
| 变更负责人 | 待填写 | 待填写 | 待填写 |
| 第一批准人 | 待填写 | 待填写 | 待填写 |
| 第二批准人 | 待填写 | 待填写 | 待填写 |
| C0 放行 | 待填写 | 待填写 | 待填写 |
| C1 放行 | 待填写 | 待填写 | 待填写 |
| C2 放行 | 待填写 | 待填写 | 待填写 |
| 观察收口 | 待填写 | 待填写 | 待填写 |

## 9. 关联文档

- [AreaSong Ops 部署检查清单](../../services/areasong-ops/deploy/deploy-checklist.md)
- [控制面 schema](../../services/areasong-ops/docs/control-plane-schema.md)
- [AreaSong Ops 控制面运维手册](../playbooks/areasong-ops-control-plane.md)
- [变更管理规范](../../standards/05-change-management.md)

## 10. 当前证据状态

以下结果来自本地隔离 Runner、开发 Web 和适配器测试，不等同于生产运行态通过：

| 验收项 | 当前状态 | 证据/剩余动作 |
| --- | --- | --- |
| 生命周期计划与审批 | 本地已证实 | 隔离 Web/Runner HTTP 实测 `pending_approval -> approved -> succeeded`；计划绑定 `tenantId=production`、`serverId=losangeles` 和 TrafficPolicyDigest；窗口内需用真实 Access 身份重做 |
| `stop` 阶段顺序 | 本地已证实 | 任务事件包含 `preflight、drain、enter-maintenance、stop、health`，任务重放返回同一任务；生产需保存实际连接归零证据 |
| `start` 流量恢复门禁 | 代码/适配器已覆盖 | 生产需证明应用健康后才恢复公网，失败保持维护页 |
| Nginx drain 超时保护 | 本地已证实 | 适配器测试覆盖旧 worker、活动连接和超时，隔离 Nginx `1.27-alpine` 容器的真实 `nginx -t` 通过；生产需执行真实站点和隔离前缀验收 |
| TrafficPolicy 摘要绑定 | 已证实 | 远程任务和契约不匹配负向测试通过；生产需保存 Runner 复验摘要 |
| 幂等与原子提交 | 本地已证实 | Go store/runner 测试、HTTP 计划重放和任务重放均通过；生产需核对审计、任务、终态一致性 |
| direct desired-state | 本地已证实 | 隔离 Web 的公开 `GET/POST /api/services/areaforge/desired-state` 均返回 `404`；生产窗口需再做一次外部 Web API 负向请求 |
| 租户隔离与双人审批 | 代码/负向测试已覆盖 | 生产需用实际 tenant binding、第二批准人和越权主体验证 |
| Compose、文件、终端、恢复、Kubernetes | 默认关闭 | 本包不执行；各能力必须另建专项变更包 |
| 生产部署身份与备份 | 未验证 | 本地已完成源码/Go/适配器/Web/Compose 门禁、Linux 静态交叉编译及 Docker Runner/Web 构建；生产仍需补齐批准 commit、镜像/二进制 digest、SQLite 快照和 fresh backup manifest |

### 本地生产等价验收记录（2026-08-24）

- 运行环境：macOS ARM64；Docker OrbStack `29.4.0`、Compose `v5.1.2`、buildx 可用。
- 已通过：`OPS_PREFLIGHT_REPO_ROOT=/Users/as/Ai-Project/project/ops deploy/preflight.sh source`、schema JSON `jq` 校验、`CGO_ENABLED=0 go test ./...`、`go vet ./...`、适配器 Python 测试（30 项）、`bash -n`、`shellcheck`、Web lint/typecheck/build、Compose 结构校验（使用非生产命令行插值值和 `--no-env-resolution`；未读取或创建 `/etc/areasong-ops/web.env`）。默认 `/opt/ops` 预检路径在 macOS 本地不适用，使用脚本提供的只读路径覆盖参数。
- 历史本地制品证据（不可用于最终发布）：曾使用 `--network host` 构建成功，Runner 导出为 Linux ARM64 静态 ELF；Web 镜像 `sha256:5f4085f22d6444a8cd784aba2ffa559242c983d95b4ae9d09d6d5366725a43fd` 对应修复前 dirty tree 的 commit `c8033c294e4baecdf5d3ad574224de7d3caf2ba7`。最终发布必须基于本次批准 commit 重新构建并记录新 digest。
- 隔离运行态：临时 SQLite、Unix Socket、开发身份；Runner/Web `/healthz` 200，Fleet 单服务器和 Runner 心跳在线。
- 容器运行约束：Web 实测 `User=65532:65532`、只读 rootfs、`CapDrop=ALL`、`no-new-privileges`；共享 Runner GID 后 Web 镜像 `/healthz=200`，Fleet API 返回 200。
- 负向门禁：注入 `BusinessHttp5xxHigh` 时执行被拒绝；终端和文件模块关闭时 API 返回 `409`；公开 desired-state 路径 `GET/POST` 均 `404`。
- Nginx：隔离 `nginx:1.27-alpine` 容器真实 `nginx -t` 通过；本机仍无宿主 Nginx，真实生产站点、证书、Cloudflare 源站和流量连接归零未验证。
- 浏览器：Vite `http://127.0.0.1:4173` 页面、总览、生命周期和终端关闭态通过；截图见 `services/areasong-ops/web/output/playwright-local/ops-ui.png`。终端关闭态产生预期的 API `409` 控制台错误，不影响页面状态。
- 未完成外部条件：真实生产 Access/mTLS/备份/Prometheus/Alertmanager、真实站点流量、生产观察窗口，以及任何生产写操作。

只有表中“生产需”项在 C0-C3 窗口内形成带时间戳的审计证据，才允许将本包标记为完成。
