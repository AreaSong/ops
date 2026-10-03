# AreaSong Ops 生产功能变更包

> 本文件把已实现能力拆成可独立批准的生产变更包。它不是执行授权，也不是生产已启用证明。每个包都必须在有效变更窗口内通过 `la-share` 执行，完成备份、验证、审计和回滚记录后才能关闭。

> 自 2026-10-02 起，本文同时是“全功能完成与生产上线”的**唯一验收台账**。C0–C7 保留变更边界；后文固定验收 ID、证据和交接状态。[部署检查清单](deploy-checklist.md) 是操作门禁，[控制面 Schema](../docs/control-plane-schema.md) 是契约来源，历史 records 是证据来源，均不另建一份完成状态。历史“已具备/已验收”表述必须按下文的版本、环境和证据范围解释。

## 共用门禁

- 批准 commit 必须同时构建 Web、Runner、适配器和配置；运行 `preflight.sh source`、Go/适配器/Shell/前端门禁。
- 变更前只读记录 `/opt/ops` HEAD、dirty 文件、Web/Runner/Nginx/Compose/image digest、Socket、容器健康和公网 HTTP；配置文件先按日期备份。
- 所有写请求使用 Release Plan 和 UUID 幂等键；公开 API 不允许直接写 desired state。
- 执行后验证 Runner/Web revision、任务终态、审计摘要、告警状态、流量状态和回滚证据。异常或身份漂移立即停止并进入 `needs_attention`。
- 未列入某一变更包的服务、数据库、流量、Kubernetes 集群级对象和凭据不作任何修改。

## 包 C1：生命周期与审计

- **范围**：AreaForge/Sub2API inspect、check、维护、排空、start/stop、计划批准、任务、观察、回滚和审计。
- **等级**：L2；AreaForge `start/stop` 允许已批准的 C2 单人例外，其他高风险动作按创建人/独立批准人两方流程执行。
- **前置证据**：TrafficPolicy digest、Nginx `-t`、drain 连接归零或明确超时、健康端点、维护文件可读、Runner/Web runtime preflight PASS。
- **执行**：逐服务创建计划，先 preflight 和流量保护，再应用变更；start 只有健康后恢复流量。
- **回滚**：失败保持维护页并标记 `needs_attention`；恢复上一受控配置/应用身份后再次 health，不自动暴露 502。
- **当前状态（2026-10-02 校正）**：原记载“代码和本地/生产基线已验收；不重复执行 AreaForge stop/start”仅保留为历史表述。当前没有可关联的 AreaForge stop/start 成功完整闭环证据，OPS-02-01/02-02 待验证；不能据此断言历史上绝对未执行，也不能因此直接重跑。后续先核对证据，再对缺失动作申请明确变更单元。

## 包 C2：更新与批量编排

- **范围**：签名发布发现、prepared gate、单机更新、Fleet 标签、显式目标、Canary、并发和失败停止。
- **等级**：L2；跨服务器或生产更新按服务逐项批准。
- **前置证据**：GHCR image digest/签名、fresh recovery point、目标 server/Runner 在线租约、批次列表和观察窗口。
- **执行**：先 canary，再按固定批次 dispatch；任一失败停止后续批次并保留节点状态。
- **回滚**：按批准的来源任务恢复应用身份；数据库不由普通 rollback 自动恢复。
- **当前状态**：后端/UI/负向测试已具备；生产批量仍需单独目标清单和窗口。

## 包 C3：Compose 与受管文件

- **范围**：Compose validate/propose/approve/apply/health/rollback，受管文件 allowlist 读取和原子替换。
- **等级**：L2；文件写入和 Compose apply 分开批准。
- **前置证据**：root-owned allowlist、expected digest、Compose config、镜像摘要、依赖容器快照、fresh backup。YAML 允许非循环 anchor/alias，拒绝 merge key、循环引用、宿主路径和特权字段。
- **执行**：只更新声明的 controlled/runtime 副本；应用服务必须 `--force-recreate`，依赖容器身份保持不变。
- **回滚**：恢复受控备份和上一 digest；容器重新创建后从运行态复核挂载内容。
- **当前状态**：Sub2API 现有 anchor Compose 已通过本地校验；生产功能开关仍按逐项审批。

## 包 C4：受控终端与 Break-glass

- **范围**：只读命令目录、Shell 计划、独立批准、短时租约、录制和审计。
- **等级**：L3；Break-glass 必须两名独立主体，不能由操作人自批。
- **前置证据**：固定命令 allowlist、root-owned 工作目录、命令超时、录制存储、自动过期和告警验证。
- **执行**：先创建计划，再由独立批准人批准，最后创建人提交原始输入；只允许声明对象和命令。
- **回滚**：终止未收口会话，保留录制和审计；不提供任意 root Shell 或任意路径写入。
- **当前状态**：本地只读命令 profile 已验证；生产 `terminal.enabled`/`breakGlass` 默认关闭。

## 包 C5：恢复中心

- **范围**：恢复点检查、隔离恢复演练、生产恢复。
- **等级**：L3；生产恢复需要恢复点、目标和影响的独立双确认。
- **前置证据**：备份完整性、版本兼容性、全部 artifact role、expected-before、隔离资源和数据库健康。
- **执行**：先 isolated drill，再创建 production restore plan；恢复阶段失败保持现场和维护状态。
- **回滚**：生产数据库恢复不自动回滚；重新恢复必须重新双确认，不能重放旧请求。
- **当前状态**：恢复代码和负向测试已具备；生产可执行性取决于真实备份 manifest 和演练证据。

## 包 C6：扩展与 Runner 自更新

- **范围**：用途隔离的签名插件 WASM 沙箱、Runner signed bundle、分批激活和失败收口。
- **等级**：L3；Runner 更新采用创建人/独立批准人两方授权，插件默认关闭。
- **前置证据**：发布者公钥、Sigstore/Ed25519 签名、artifact digest、Runner mTLS、当前二进制备份和 systemd 状态。
- **执行**：先准备和验证制品，再独立激活；重启单个 Runner 后验证 socket、revision、版本和 preflight。
- **回滚**：恢复旧二进制和 unit 状态，只重启 Runner；插件失败删除隔离暂存，不触碰宿主 Docker Socket。
- **当前状态**：签名校验、WASM 计划/双审批/独立执行状态机、Runner Fleet 显式目标/Canary/分批/并发/失败停止/逐节点回滚、失败收口和本地页面已具备；扩展与 Fleet 自更新均默认关闭，生产启用前还需制品、mTLS/心跳、WASM/Fleet 负向验收及单独批准。

## 包 C7：Kubernetes Namespace 级计划

- **范围**：登记 namespace 的 manifest validate、diff、apply、rollout 和回滚。
- **等级**：L3；禁止集群级任意操作、namespace/PV 删除和请求拼接 `kubectl`。
- **前置证据**：固定 context/namespace/resourceKinds/object allowlist、manifest digest、dry-run、回滚 manifest 和观察窗口。
- **执行**：只创建受控 Apply Plan；批准后按 namespace 执行并等待 rollout/health。
- **回滚**：使用同一 allowlist 内的批准 manifest；失败保留操作证据并进入人工关注。
- **当前状态**：目标投影、dry-run、计划和负向测试已具备；生产 apply 尚未执行。

## 关闭条件

只有当某一包的运行证据、审计事件、观察窗口和回滚验证全部归档，并更新 `inventory/` 后，才能把该包标为“生产已启用”。其他包保持“代码已具备/默认关闭/待审批”，不能因为页面可见或本地 profile 可用而提前开启。

## 控制面发布统一入口（C0）

Web + Runner 的版本发布不再由多条临时命令拼接，统一通过
`deploy/release-orchestrator.sh`。它是 C1–C7 能力启用前的控制面基础变更，固定执行顺序为：

1. manifest、签名、版本、revision、Web digest、Runner checksum 校验；
2. 生产源码和已安装运行态只读 preflight；
3. 创建唯一 deployment ID，备份 Runner/updater/unit、Web env、Compose、image inspect 和 SQLite；
4. 先安装并验证 Runner，再拉取 immutable Web digest 并仅重建 Web；
5. 运行 health、socket、metrics、rootfs/用户/Docker Socket 隔离和 runtime preflight；
6. 原子写入 state/audit，失败立即停止并按组件逆序回滚，保留恢复材料。

相同 deployment ID 只允许相同制品摘要的幂等重放；成功重试不重复重启/重建，已回滚或
`needs_attention` 必须新建 ID。入口默认固定生产路径并要求 root，测试只能通过
`OPS_RELEASE_TEST_MODE=1` 使用临时隔离目录；它不执行业务服务生命周期、数据库恢复、流量切换、
Kubernetes apply 或 Git checkout/pull。

## 阶段 1：固定验收台账（2026-10-02）

阶段状态：`completed`，仅表示本阶段文档和定向核对已完成。D1/D2 留待用户决策，产品各项状态以下表为准；未开始 S2.1。

### 状态、版本与证据口径

- 本阶段仅作本地定向核对和本文编辑；不修业务代码，不连接生产，不部署，不提交或推送，不修改生产配置、权限、AGENTS.md 或技能。阶段编号使用 S1/S2.x/S3.x/S4.x/S5.x，与旧部署清单的“阶段 0–5”和历史 C1/C2 编号不同。
- **已有证据通过**：指定版本、环境、日期、正负向行为及必要回滚都有可定位结果；只能覆盖该证据所述事实。**部分完成**：目标的一部分已有实现或结果，缺项明确。**未实现**：定向源码核对确认缺少该能力。**待验证**：没有足够结果，不能因页面、测试或代码存在而通过。**待资源**：缺真实目标、账号或运行条件。**待用户决策**：验收结果会随选择变化；推荐不等于用户已确认。
- 页面存在、测试存在记录在“实现”列；测试运行结果在“本地验证”列；构建、制品发布、生产部署分列于交付事实表；“验收状态”表示整项用户标准，不能由前述任意一列替代。没有百分比，也不按文件数计进度。
- 本地列中的 `S@B0/E##` 表示 2026-10-02 对 B0 源码的有限静态核对，**没有运行应用测试或浏览器验收**；`H-L` 仅指历史套件结果，不代表每个目标行为均已覆盖。所有源码入口 E01–E14 均绑定 B0；后续改动须记录新 HEAD、相关工作区 diff 指纹及失效证据。
- 生产列 `P0` 统一表示“待验证；截至 2026-10-02，本阶段无该项生产功能证据，未连接生产”；有 H-P/H-B/H-D 的行另列历史事实。日期全部采用 Asia/Shanghai；历史未给出具体时刻的，不补猜时刻。
- 每次新增运行证据至少包含：验收 ID、日期时区、源码 revision/相关 diff、环境及脱敏目标、命令或操作、预期/实际结果、证据路径或 URL、失败/跳过项；生产另含批准单元、制品版本/digest、计划/任务/审计 ID、观察窗口及回滚结果。没有原始日志地址的历史信息明确保留来源等级。

### 基线与交付事实（不同列不能互相替代）

| 证据 ID | 已知事实及来源 | 有效范围与缺口 |
| --- | --- | --- |
| B0 | 本阶段 `git branch --show-current` = `main`；`git rev-parse HEAD` = `b4ec0f9268afa6f1b23259204c3544b798ad47f3`；`git diff b4ec0f9 -- services/areasong-ops .github/workflows/areasong-ops-release.yml` 在编辑前为空 | 产品目录及发布工作流未偏离历史提交；不证明运行环境、依赖或远端未变化。本文编辑后仅文档有未暂存差异 |
| W0 | `git status --short`：27 项用户暂存改动及未跟踪 `scripts/local/`；初始未暂存 tracked diff 为空。暂存二进制 diff SHA-256：`4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155` | 保护既有暂存及未跟踪文件；收尾对比暂存指纹、20 个未跟踪文件内容指纹及未暂存路径。不把用户治理变更混入功能交付 |
| H-L | 用户本次任务提供的“上一轮本地验证”，关联 B0：Go 全量测试及 vet；前端 11 项测试、lint/typecheck/build；适配器 36 项；部署测试 95 项、1 项因 Linux/Python 3.12 要求在 macOS 跳过；11 个 Shell 文件、治理校验和差异检查通过 | 历史套件通过，未重跑；未提供逐用例日志路径，不能反推交互、真实 Fleet、Kubernetes、恢复或权限验收完成。GitHub CI run `35116689309` 成功亦为用户提供的历史结果，本阶段未查询远端 |
| H-A | 用户本次任务提供：最后已发布 AreaSong Ops `1.1.11`，对应 B0 | 已有历史发布报告；manifest、签名身份、checksum、image digest 和远端下载回验待取证；不能当成已部署 |
| H-P | 用户本次任务提供的 2026-10-02 生产检查：LosAngeles Web/Runner `1.1.5`，revision 与 `/opt/ops` 均为 `a232253805286d8a72ba8fda4453afda0c676916`；Prometheus 23/23 正常，queued/running 为 0；9 条 `failed_recoverable`、1 条 `needs_attention` 保留 | 是旧版本历史运行快照，不是 B0 的功能验收。配置漂移、JadeAI SLO 预算不足、Account Vault SLO 预算耗尽告警未闭环；历史任务不能删掉来制造健康状态。原始命令输出/任务 ID 未随本次提供 |
| H-B | 用户本次任务提供：2026-10-02 12:00 完整备份 10 个产物；12:15 完成 10 个 R2 产物回读校验 | 仅该备份集完整性/回读历史结果；manifest ID、摘要和校验记录地址待补，不证明恢复执行、网页流程或生产数据库恢复 |
| H-D | 用户本次任务提供：AreaForge 最近本地隔离演练 2026-09-28，恢复 72 张用户表 | 仅该次隔离演练，源码/恢复点/日志地址待补；不能推导当前恢复点的新鲜演练、R2 直接恢复或生产恢复通过 |

W0 收尾期间观察到新的未跟踪文件 `scripts/local/clash_audit/checks.py`、`report.py`、`source.py`（当次共 23 个未跟踪文件）。它们不是本阶段创建或修改的文件，未纳入本次差异；原有 20 个未跟踪文件未删除且内容指纹一致，27 项暂存差异指纹一致。工作区存在并行变化，下一阶段必须重新读取状态，不能把本快照当作工作区冻结。

| 交付维度 | B0 / 1.1.11 | 生产历史 1.1.5 | 本阶段新增结果 |
| --- | --- | --- | --- |
| 页面存在 | E01–E14 对应入口，缺口见逐项实现列 | 未登录网页核验，历史停在 Cloudflare 登录页 | 仅源码定向核对 |
| 测试存在 | E01–E14 中已有测试入口；存在不代表覆盖完整 | 不适用；运行态不能由本地测试代证 | 未新增测试 |
| 本地验证 / 构建 | H-L 历史测试及前端 build 通过；Linux 跳过项未通过，其他构建产物日志待关联 | 不由版本号推导构建门禁 | 仅文档验证，无新应用构建 |
| 制品发布 | H-A 历史已发布报告，签名/digest 原始证据待核验 | 不能用 1.1.11 的发布替代 1.1.5 的运行身份 | 无发布 |
| 生产部署 | 待验证：未取得 1.1.11 部署证据 | H-P 历史部署/运行身份报告 | 无部署 |
| 功能验收 | 见下表，不能把 H-L 套件成功批量登记为通过 | H-P 仅覆盖运行快照，不覆盖各模块闭环 | 未新增生产功能通过项 |

### 定向源码证据索引

下列行号均为 B0 的入口定位；不是整仓审计结论。后续先按符号定位，再只读相关片段。测试路径仅证明测试存在，执行结果仍取 H-L 或新增记录。

| ID | 实现 / 文档与测试入口 | 本次可确认的事实 |
| --- | --- | --- |
| E01 | [api.ts](../web/src/api.ts) L332 `createPlan`；[ConfirmationDialog.tsx](../web/src/components/ConfirmationDialog.tsx) L21/L113；[plans.go](../internal/runner/plans.go) L345；[control_flow_atomic_test.go](../internal/store/control_flow_atomic_test.go) L144 | B0 时 `createPlan` 不提交 scheduleAt、scheduled 按钮持续禁用；S2.1 本地已补齐，版本与运行证据见下文 S2.1。后端到期显式激活原逻辑未改，未增加自动执行器 |
| E02 | [AccessControl.tsx](../web/src/views/AccessControl.tsx)（S3.1 当前源码）；[control.go](../internal/model/control.go) L117；[access_changes.go](../internal/runner/access_changes.go) L23；[access_changes_test.go](../internal/runner/access_changes_test.go) L21/L369；[tenant_idor_test.go](../internal/runner/tenant_idor_test.go) L14 | S3.1/S3.1a 新增租户／名称编辑、服务端只读差异与跨账号审批应用本地完成，证据见下文；S3.2a 角色只读差异本地完成；S3.2b 角色新增／编辑网页、真实双会话及独立复核本地 completed，见下文；S3.2c 登记权限创建／最终应用校验本地 completed，验证环境与兼容限制见下文；S3.2d1 删除合同／只读详情及 S3.2d1a 摘要专项本地 completed（历史部分完成记录保留）；S3.2d2 删除网页及双账号本地 completed，两张历史截图原件缺失已经用户接受为有限证据保全例外，见下文收口记录；OPS-07-02 整体及生产验收仍部分完成，既有绑定管理仍缺 |
| E03 | [fleet.go](../internal/model/fleet.go) L570–604 `ConcurrencyPolicy`；[batch_test.go](../internal/runner/batch_test.go) L444 | 已有 global/per_runner/per_server 并发及队列限制；定向检索 internal/model、runner、store、web/src、docs 未找到通用租户/用户 quota 模型/API/UI。`resourcequota` 仅出现在 Kubernetes 禁止对象列表，不是该配额实现 |
| E04 | [recovery_center.go](../internal/runner/recovery_center.go) L28/L43/L65；[RecoveryCenter.tsx](../web/src/views/RecoveryCenter.tsx) L59；[recovery_center_test.go](../internal/runner/recovery_center_test.go) L81 | 后端读最多 20 个恢复点但只返回 Latest，网页固定使用 latest；创建恢复计划已接受明确 recoveryPointId，并按选中点校验演练 |
| E05 | [restricted.go](../internal/runner/restricted.go) L124；[terminal_shell.go](../internal/runner/terminal_shell.go) L107/L149；[terminal_shell_test.go](../internal/runner/terminal_shell_test.go) L16；[store/terminal_shell.go](../internal/store/terminal_shell.go) L323；[maintenance.go](../internal/store/maintenance.go) L274 | 固定命令和 Shell 均用 CombinedOutput，结束后返回；保存摘要、脱敏输出及终态。未见 PTY/实时录制链；Prune 未列 terminal_sessions/terminal_shell_plans 清理，不能推导终端输出已按 30 天清理 |
| E06 | [Overview.tsx](../web/src/views/Overview.tsx) L29；[reconcile.go](../internal/reconcile/reconcile.go) L22；[services.go](../internal/runner/services.go) L113；[README](../README.md) 运行边界 | 总览和自动任务状态投影存在；desired/actual/health/drift 分开。自动任务调度仍以 cron/systemd 为权威，网页不是任意调度编辑器 |
| E07 | [auto_update.go](../internal/runner/auto_update.go) L154；[auto_update_observation_test.go](../internal/runner/auto_update_observation_test.go) L45；[Schema](../docs/control-plane-schema.md) L83；[areaforge.sh](../adapters/areaforge.sh) L487 | 自动发现默认生成计划；已声明签名/prepared 门禁及观察回滚路径，不代表真实发布者签名、运行身份和回滚已验收 |
| E08 | [batch.go](../internal/runner/batch.go) L61；[batch_test.go](../internal/runner/batch_test.go) L137/L444；[Batches.tsx](../web/src/views/Batches.tsx) L147 | 通用批量支持显式目标/Canary/限并发相关路径，但拒绝 FailureRollback；UI 仍提供 rollback。逐目标人工受控回滚需闭环，不得引用 Fleet 自更新自动回滚作为通用批量证据 |
| E09 | [fleet.go](../internal/runner/fleet.go) L208；[fleet_security_test.go](../internal/runner/fleet_security_test.go) L72；[Fleet.tsx](../web/src/views/Fleet.tsx)；[Schema](../docs/control-plane-schema.md) L123 | 登记、审计、v2 心跳身份入口存在；示例单机及模拟心跳不是第二 Runner 的 mTLS/租约验收 |
| E10 | [compose_apply.go](../internal/runner/compose_apply.go) L216；[managed_file_apply.go](../internal/runner/managed_file_apply.go) L58；[compose_apply_test.go](../internal/runner/compose_apply_test.go) L226；[Files.tsx](../web/src/views/Files.tsx) | Compose 与受管文件具有独立受控路径；本地模拟不能替代生产挂载内容、依赖容器身份和回滚验收 |
| E11 | [extension_runtime.go](../internal/runner/extension_runtime.go) L32；[extension_plans_test.go](../internal/runner/extension_plans_test.go)；[runner_update.go](../internal/runner/runner_update.go) L60；[runner_fleet_update_test.go](../internal/runner/runner_fleet_update_test.go) L253；[RunnerFleetUpdate.tsx](../web/src/views/RunnerFleetUpdate.tsx) | WASM 沙箱、Runner 单机准备/激活与 Fleet 回滚有实现/测试入口；生产默认关闭，模拟器不能证明真实自更新 |
| E12 | [kubernetes_plans.go](../internal/runner/kubernetes_plans.go) L18；[kubernetes_plans_test.go](../internal/runner/kubernetes_plans_test.go) L111；[Schema](../docs/control-plane-schema.md) L148 | Namespace/kind/object 白名单、预览摘要、独立回滚计划已有入口；真实集群仍待资源，不开放集群级对象或未知 apply 自动重试 |
| E13 | [runner/main.go](../cmd/runner/main.go) L288；[maintenance.go](../internal/store/maintenance.go) L274；[observation_maintenance.py](observation_maintenance.py) L148；[README](../README.md) L42/L47 | 通用事件 30 天、任务/审计摘要 365 天，非租户自定义留存。历史观察计划工具转 needs_attention，不是成功或业务回滚。凭据范围仅 GitHub 告警 Issue 同步 Token，详见 [credentials.go](../internal/runner/credentials.go)、[credentials_test.go](../internal/runner/credentials_test.go)、[Credentials.tsx](../web/src/views/Credentials.tsx) |
| E14 | [release_orchestrator.py](release_orchestrator.py) L21；[release workflow](../../../.github/workflows/areasong-ops-release.yml) L122；[test_release_lifecycle.py](tests/test_release_lifecycle.py) L68；[部署清单](deploy-checklist.md) L38 | C0 发布入口和签名工作流存在。激活前须满足隔离证据才可完整回退；activationStarted 后失败保留新状态并停止控制面、needs_attention。上文“逆序回滚”的摘要不能解释为可用旧 SQLite 覆盖新审批/审计 |

### 逐项验收矩阵

ID 永不复用；拆分时保留父 ID 与关联，不通过删除待资源项改变分母。下表“具备”只指有限证据中的现有代码，完整运行覆盖由后续 S3/S5 补齐。`S3.nn`、`S5.nn` 按范围编号 nn 分别指本地专项和生产专项；**每个叶子 ID 单独交接**，不得一次执行整列。

| 验收 ID | 用户可观察的完成标准 | 实现（页面 / 测试） | 本地验证、版本、出处 | 生产验证、日期、出处 | 验收状态 | 依赖 / 未决选择 | 后续阶段 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| OPS-01-01 | 总览可核对各对象实际状态、健康、新鲜度；加载/空态/数据源失败不显示假健康 | 页面/状态代码存在 E06；测试覆盖待逐项关联 | S@B0/E06；H-L 非逐项证明 | P0；H-P 23/23 仅监控快照 | 待验证 | 当前数据源与登录身份 | S3.01、S5.01 |
| OPS-01-02 | 漂移、活动告警及来源可追踪；阻断告警与状态一致，过期数据有提示 | 展示与映射具备 E06/E07；相关套件存在 H-L | S@B0/E06/E07；H-L | P0；H-P 有漂移及两项 SLO 告警 | 部分完成 | 告警根因/处置证据，不能无审批修改监控规则 | S3.01、S5.01 |
| OPS-01-03 | 自动任务状态、最近执行/新鲜度可核对；受控补跑仅允许既有白名单 | 页面/投影具备 E06；逐项测试待关联 | S@B0/E06；H-L | P0；H-P queued/running=0 不代表自动任务成功 | 待验证 | cron/systemd 仍是调度权威 | S3.01、S5.01 |
| OPS-02-01 | 单服务维护→排空→停止及启动→健康→恢复流量闭环可见，计划/任务/审计可关联 | 生命周期代码/页面及既有测试具备；契约见 Schema L65 | S@B0/Schema；H-L | P0；H-P 无 AreaForge 完整成功闭环证据 | 待验证 | 服务、窗口、连接归零、TrafficPolicy；缺证不等于从未执行 | S3.02、S5.02 |
| OPS-02-02 | 排空超时、健康失败、回滚失败均保持流量保护及明确终态；恢复后复核旧身份 | 失败保护路径具备；页面/测试存在不代替故障演练 | S@B0/Schema；H-L | P0 | 待验证 | 新鲜备份、批准回滚及故障注入边界 | S3.02、S5.02 |
| OPS-03-01 | 预览→摘要批准→执行→观察→终态贯通；重试幂等、漂移失效、审计完整 | 页面/后端/测试具备 E01；[engine_test.go](../internal/runner/engine_test.go) L441 | S@B0/E01；H-L | P0；H-P 遗留任务不代表收口成功 | 待验证 | 两独立身份、观察时间、失败终态证据 | S3.03、S5.03 |
| OPS-03-02 | 网页提交明确时区时间；提前执行被拒，到期后合规创建人可手动执行，同键不重复 | 本地已实现：定时创建、审批、到期显式执行闭环 E01/S2.1；后端逻辑沿用 | S2.1 同版单测、Go、隔离浏览器及独立复核通过，见下文 | P0；S2.1 尚未部署/生产验收 | 部分完成（本地 completed；生产待验收） | 到期不自动批准或无人值守写生产 | S2.1→S3.03→S5.03 |
| OPS-04-01 | 展示签名发布发现及 prepared 状态；无效签名/过期证据拒绝；默认只生成计划 | 自动更新页面/代码/相关测试具备 E07 | S@B0/E07；H-L | P0 | 待验证 | 真实发布者、制品、服务专属 prepared 条件 | S3.04、S5.04 |
| OPS-04-02 | 已批准更新按备份/窗口执行，观察告警触发一次受控版本回滚；重启不重复动作 | 观察回滚实现/测试存在 E07 | S@B0/E07；H-L | P0 | 待验证 | 新鲜恢复点、回执、身份与冲突门禁；不隐式恢复数据库 | S3.04、S5.04 |
| OPS-05-01 | 显式目标 Canary、并发/队列、观察、失败停止可逐节点核对，拒绝跨租户/离线/越界目标 | 批量页面/实现/测试具备 E08 | S@B0/E08；H-L | P0 | 待资源 | 第二 Runner/真实多目标、红线批准 | S3.05、S5.05 |
| OPS-05-02 | 失败后按批准来源任务逐目标回滚并验证旧身份；UI 不承诺后端拒绝的策略 | 部分完成；通用自动批量 rollback 未实现，UI/后端策略不一致 E08 | S@B0/E08；H-L 不证明逐目标闭环 | P0 | 部分完成 | 按 C2 既有人工逐目标批准路径验收；若改为自动补偿须另定范围及授权 | S2.8→S3.05→S5.05 |
| OPS-06-01 | 有权主体登记 Server/Runner；标识、能力、标签、租约及撤销/失效状态可核对 | Fleet 页面/登记/v2 心跳测试存在 E09 | S@B0/E09；H-L | P0；H-P 仅单机运行报告 | 待验证 | 登记权限、身份一致性；登记即外部状态变更 | S3.06、S5.06 |
| OPS-06-02 | 第二 Runner 真实 mTLS、心跳、离线拒绝、目标路由闭环，不串租户/机器 | 远端入口/安全测试存在 E09；本地开发 Fleet 为模拟 | S@B0/E09；H-L 仅本地 | P0 | 待资源 | 第二 Runner、证书、网络及登记变更单元；仍在最终目标 | S3.06、S5.06 |
| OPS-07-01 | 有权用户在网页创建/修改/停用或删除租户，引用冲突可见，审批后才生效 | S3.1/S3.1a 新增／名称编辑、只读差异及跨账号审批应用本地完成 E02 | S3.1 前端、Go、Chromium 证据见下文 | P0；未部署／生产未验收 | 部分完成 | 只读详情已完成；停用／删除及引用冲突完整验收不在本阶段 | S3.1→S3.07→S5.07 |
| OPS-07-02 | 自定义角色创建/修改/移除形成可审阅变更；内置角色、引用约束不被绕过 | S3.2a/b/c/d1/d1a/d2 本地完成（d1 原摘要阻塞由 d1a 解除；d2 两张历史截图原件缺失按用户接受的有限例外收口），见下文 E02 | S3.2d1 HTTP/SQLite、S3.2d2 Go/前端、真实双会话及独立复核见下文；保留此前证据 | P0；未部署／生产未验收 | 部分完成 | apply 摘要专项及 S3.2d2 删除网页本地链已验证；整体及生产验收仍部分完成，既有绑定管理仍缺；存量角色／历史提案不清退 | S2.3（S3.2a→b→c→d1→d2）→S3.07→S5.07 |
| OPS-07-03 | 新增及编辑/撤销既有绑定均有对象范围、版本冲突、审批和生效反馈 | 部分完成：新增绑定/审批 UI 存在；既有绑定管理表单缺失 E02 | S@B0/E02；H-L | P0 | 部分完成 | expectedVersion、即时撤权及旧会话负向验证 | S2.4→S3.07→S5.07 |
| OPS-07-04 | 创建人与独立批准人两账号完成审批；跨租户读写/自批/撤权后操作被拒 | 审批与隔离实现/测试存在 E02 | S@B0/E02；H-L | P0；H-P 网页停在 Access 登录页 | 待资源 | 两个真实授权账号；既有 AreaForge start/stop 单人例外不扩展 | S3.07、S5.07 |
| OPS-08-01 | 按 D1 确认的对象、单位、作用域、超限行为和调整权限展示并执行配额 | 通用租户/用户配额未实现；只有批量并发/队列 E03 | S@B0/E03；无该通用能力运行证据 | P0 | 待用户决策 | D1；默认数值/启用策略待确认，不增加收费系统 | S2.5→S2.6a→S2.6b→S3.08→S5.08 |
| OPS-08-02 | 窗口与 IANA 时区可见；窗外拒绝，策略变更使未执行旧批准失效 | 窗口模型/自动更新页面及测试具备 E07 | S@B0/E07；H-L | P0 | 待验证 | 边界时刻、跨日/DST、生产窗口及审批 | S3.08、S5.08 |
| OPS-09-01 | Compose 校验/差异/摘要审批/apply/health/rollback 闭环；只动受控副本及目标服务 | 配置页面/后端/回滚测试存在 E10 | S@B0/E10；H-L；原 C3 仅本地 anchor 校验 | P0 | 待验证 | digest、fresh backup、依赖身份及 force-recreate 复核 | S3.09、S5.09 |
| OPS-10-01 | 白名单文件读取、差异审批、原子替换及恢复可见；越界/漂移/冲突拒绝 | 文件页面/后端具备 E10；逐项测试待关联 | S@B0/E10；H-L | P0 | 待验证 | root-owned allowlist、备份、真实挂载内容；默认关闭 | S3.10、S5.10 |
| OPS-11-01 | 按 D2 确认的一次性命令或交互模式验收；授权、超时、输出和失败状态明确 | 一次性固定命令/输出具备 E05；持续 PTY/实时录制未实现 | S@B0/E05；H-L | P0；历史仅本地 profile 验证 | 待用户决策 | D2；不扩大到无限制 root Shell | S2.9→S3.11→S5.11 |
| OPS-11-02 | Break-glass 独立批准、短租约、原输入绑定、到期/重放拒绝及执行审计可核对 | Shell 计划与独立审批测试存在 E05 | S@B0/E05；H-L | P0 | 待验证 | D2；terminal/breakGlass 分别批准启用、失控会话终止证据 | S3.11、S5.11 |
| OPS-11-03 | 按 D2 固定录制粒度与期限，可查询合法记录并验证到期清理、不泄露敏感内容 | 终态输出存储存在；定向未见终端表清理 E05，期限不能借用通用事件结论 | S@B0/E05/E13；无该清理行为通过证据 | P0 | 待用户决策 | D2；若新增清理，需 mock 时间/保留例外测试及数据处理批准 | S2.9→S3.11→S5.11 |
| OPS-12-01 | 网页列出历史恢复点并明确选择；计划/批准/执行固定同一 ID，过期/不完整点拒绝 | 部分完成：后端接受 ID；列表只返回 Latest，UI 无历史选择 E04 | S@B0/E04；H-L | P0 | 部分完成 | 不回退 latest，不把某次演练当成所有恢复点可用 | S2.7→S3.12→S5.12 |
| OPS-12-02 | 备份集产物角色、大小、时间、摘要和 R2 回读结果可追踪至同一 manifest | 备份校验路径具备；不是独立网页恢复完成证明 | H-B/H-D 是外部历史报告，非 B0 本地测试 | H-B：2026-10-02 12:00/12:15，10 产物历史报告通过；原始地址待补 | 部分完成 | 新鲜 manifest/摘要、回读日志，不能替代恢复 | S3.12、S5.12 |
| OPS-12-03 | 从网页选点并审批完成隔离恢复，表数/健康/镜像/配置及隔离无生产写入有证据 | 恢复中心/计划/负向测试存在 E04；服务适配器须逐服务确认启用能力 | S@B0/E04；H-L；H-D 仅 9/28 的 72 表 | P0；H-D 不是网页全流程 | 待验证 | 隔离资源与选中点演练；不推导 Sub2API/所有服务可恢复 | S3.12、S5.12 |
| OPS-12-04 | R2 来源恢复链能关联选定点、下载/回读完整性和实际隔离恢复结果 | R2 回读有 H-B；控制面直接恢复链实现范围未核实，不能标通过 | 待验证：H-B/H-D 不能拼成同次 R2 恢复 | P0 | 待验证 | 先核对受控 R2 取回/本地 materialize 契约；不擅自新增云凭据/通用恢复网关 | S3.12、S5.12 |
| OPS-12-05 | 明确 production 模式、选定恢复点、独立双确认、影响和恢复后数据/服务健康；失败保留现场 | 后端恢复计划/负向测试具备 E04，真实服务恢复能力逐项验收 | S@B0/E04；H-L；生产恢复未本地证明 | P0 | 待资源 | 批准目标、可用备份、同点隔离演练、权限与窗口；重新恢复须重新确认 | S3.12、S5.12 |
| OPS-13-01 | 固定 GitHub 告警同步 Token 验证→切换→真实同步→确认旧 Token 已撤销；失败恢复旧配置 | 凭据页面/真实后端/测试存在 E13 | S@B0/E13；H-L | P0 | 待资源 | 目标仓库、受控新旧 Token 和轮换批准；不建设通用密钥平台 | S3.13、S5.13 |
| OPS-14-01 | 受信签名 WASM 上传/审批/沙箱执行及失败可见；越权导入/资源超限拒绝 | 沙箱/计划/UI 及测试存在 E11 | S@B0/E11；H-L | P0 | 待验证 | 发布者公钥、签名制品、用途/资源限制；默认关闭 | S3.14、S5.14 |
| OPS-15-01 | Runner 单机签名准备、独立批准、激活后版本/socket/心跳验证及失败回退 | 更新页面/后端与相关测试存在 E11 | S@B0/E11；H-L | P0 | 待验证 | 真实 bundle、旧二进制/unit、更新权限及窗口；默认关闭 | S3.15、S5.15 |
| OPS-15-02 | Fleet Runner Canary→观察→分批，失败停止；成功节点逐个恢复旧身份且保留审计 | Fleet 更新页面/协调器/回滚测试存在 E11 | S@B0/E11；H-L 仅模拟 | P0 | 待资源 | OPS-06-02、真实 mTLS 与至少第二 Runner；默认关闭且不移出最终目标 | S3.15、S5.15 |
| OPS-16-01 | 登记 Namespace 内 validate/diff/批准/apply/rollout 闭环；漂移/过期拒绝，独立新计划回滚 | 配置入口/后端/模拟回滚测试存在 E12 | S@B0/E12；H-L 仅模拟 | P0 | 待资源 | 真实集群/context/namespace/object allowlist 与批准；不可用模拟器通过替代 | S3.16、S5.16 |
| OPS-17-01 | 分页历史完整可查，事件 30 天、任务/审计摘要 365 天及产物 30 天的边界/保留例外可验证，文档一致 | 审计页面/Prune/产物清理存在 E13；逐项测试待关联 | S@B0/E13；H-L | P0 | 待验证 | 不推导租户可配置留存；终端另验 OPS-11-03 | S3.17、S5.17 |
| OPS-17-02 | 历史遗留计划逐条关联事实、处置理由及审计；失败与人工关注保留，不伪造成功 | 历史 observing 处置工具存在 E13；其他终态不能套用 | S@B0/E13；H-L | P0；H-P 保留 9 failed_recoverable + 1 needs_attention | 待验证 | 先只读取证再批准精确 ID；不删除/重放未知写入 | S3.17、S5.17 |
| OPS-18-01 | 同一源码和差异指纹的 Web/Runner/适配器/配置通过必需本地与 Linux 门禁并形成候选物 | 构建/部署测试与编排入口存在 E14 | H-L：B0 历史通过；Linux/Python 3.12 一项跳过 | P0；候选物不是生产部署 | 部分完成 | 最终修改后重跑适用检查；用户现有 staged 不混入 | S4.1 |
| OPS-18-02 | manifest、签名发布者、版本/revision、Runner checksum、Web digest 可从发布渠道回验 | 发布/验签工作流存在 E14 | S@B0/E14；原始制品回验待执行 | H-A 历史 1.1.11 发布；本阶段未查远端，无部署结论 | 部分完成 | 批准的可追溯制品发布与原始证据；不能自动复发版本 | S4.2 |
| OPS-18-03 | C0 编排备份、先 Runner 后 Web、维护验收、正式激活、正常 runtime preflight 与运行版本一致 | 编排器/生命周期测试存在 E14 | S@B0/E14；H-L 部署套件历史结果 | H-P 生产 1.1.5 旧版本；B0 部署 P0 | 待资源 | S4.3 目标/权限/备份/窗口确认；sudo 与 Access 当前状态未知 | S4.4 |
| OPS-18-04 | C0 观察/审计收口及阶段匹配的回退有证据；激活后失败不覆盖新任务/审批数据 | 失败边界/测试存在 E14 | S@B0/E14；H-L，不证明真实回退 | P0 | 待验证 | deployment ID、激活边界、状态快照；失败保留现场 | S4.4、S5.18 |

### 待决策与真实前置条件

| 决策 | 推荐及理由 | 确认状态 / 后续限制 |
| --- | --- | --- |
| D1 配额 | 租户＋用户两层的运行任务数和排队任务数，单位为任务个数；所有任务入口共同约束，超限拒绝新增，不打断现有任务；调整走平台管理审批。复用任务体系，不加入收费/支付、CPU/内存/存储计费 | 已集中询问，待用户决策。具体限额、批次子任务计数、并发预留、重启恢复、降额行为须在 S2.5 固定后再实现；数值和启用仍待明确批准，不能把推荐当成默认生产配置 |
| D2 终端与留存 | 保留审批后一次性命令；输入摘要、脱敏输出、退出码、起止时间构成记录；建议输出明细 30 天、审计摘要 365 天，并补足清理验证。与现有受控模型接近，减少交互会话权限面 | 已集中询问，待用户决策。若必须 PTY，应先定录制粒度/期限、断连/租约/终止行为再拆实现；不能扩大为无限制 root Shell。推荐期限目前不是终端已实现事实 |

- 定时计划按“到期后有权创建人显式执行”闭环，不隐含无人值守批准；自动更新发现只生成计划是既定目标。通用批量先按 C2 原有逐目标受控人工回滚验收，自动批量补偿不因 Fleet 已实现而默认加入。
- 第二 Runner 与真实 Kubernetes 是最终范围内的待资源项；不能通过暂缓、默认关闭或缩分母标为通过。尚未核实资源可用性，本阶段不申请或创建资源。
- 生产 Profile 当前未读取，标记未知；本阶段无生产连接。上一轮无 sudo、任务库/配置受保护、网页停在 Cloudflare 登录页是历史限制，是否仍阻塞由 S4.3 只读核验。未获得当前权限或审批前不能写入。
- 漂移、SLO 告警、缺失新鲜备份/同点演练、未收口任务会影响后续执行门禁；先定位事实，不能关闭告警、删除任务或放宽权限以通过验收。高风险模块逐项批准、逐项启用，禁止生产全开。

### 后续子阶段与退出条件

所有子阶段以约 18k 上下文为上限控制读取范围：输入仅本文对应行、当前 Git 状态、最近规则、具体契约和相关代码/测试；不读整段历史会话、不重扫全仓。一次只处理一行定义的紧密问题；有多个叶子 ID 的专项也逐叶子执行和交接。到限仍未满足退出条件，写 partially_completed/blocked 与准确剩余项，不自动接下一阶段。

| 子阶段 / 唯一目标 | 输入与建议读取 | 修改边界 | 验证与退出条件 |
| --- | --- | --- | --- |
| S2.1 定时计划网页闭环（本地 completed） | OPS-03-02/E01；api.ts、ConfirmationDialog.tsx、其调用入口、types.ts、runner/plans.go、store/plans.go、web/tests/api.test.mjs、control_flow_atomic_test.go；Schema ReleasePlan 合同 | 仅时间输入/请求/幂等键语义/到期操作及必要测试与原契约文档；不增加后台自动批准/执行器 | 本地时间/时区、提前拒绝、到期手动执行、刷新重入、同键重试、审批角色/摘要漂移测试及页面操作；受影响 lint/typecheck/test/build；契约变化独立只读复核。退出：该 UI 缺口有同版证据，生产仍 P0；停止交接 |
| S2.2 租户管理表单 | OPS-07-01/E02、当前租户/引用约束及审批链 | 仅租户 UI/请求映射，语义变化先确认权限边界 | 创建/编辑/停用或删除、引用冲突、stale version、审批拒绝与隔离 mock；退出：目标表单闭环且约束未绕过 |
| S2.3 角色管理表单 | OPS-07-02/E02，S2.2 交接与现有内置角色契约 | 仅角色操作与现有审批生产者/消费者 | 内置角色保护、权限集合、引用冲突、独立批准及 UI 回归；退出：同版证据与独立复核 |
| S2.4 既有绑定管理 | OPS-07-03/E02，S2.3 交接、expectedVersion/即时撤权测试 | 编辑/撤销绑定及审批反馈，不修改登录身份来源 | 对象/租户范围、撤权后旧会话、幂等、版本冲突；退出：两账号本地闭环与独立复核 |
| S2.5 固定配额合同 | D1 用户回答、OPS-08-01/E03、现有任务/批次入口 | 只定对象、单位、作用域、子任务/队列计数、错误、调整审批和兼容策略；本阶段不实施 | 场景表覆盖超限/竞态/降额/重启/跨入口及预算数值来源；退出：用户确认会改变结果和配额边界的选择，合同记回本文 |
| S2.6a 配额状态与原子准入 | S2.5 已确认合同、任务启动/终态事务及存储测试 | 仅配额模型/持久化/统一准入核心及相关测试；迁移/权限边界先批准 | 合成数据并发、失败回收、重启、回滚兼容验证；退出：核心证据及独立复核，不标 UI/生产通过 |
| S2.6b 配额入口与管理反馈 | S2.6a 交接、API/批量/自动计划/网页真实调用方 | 接入既有执行入口、审批管理和 UI；不新增收费 | 各入口不能绕过，两层限额/超限提示/调整审批/页面回归；退出：OPS-08-01 本地目标闭环且复核 |
| S2.7 历史恢复点选择 | OPS-12-01/E04、恢复点模型/Store 与当前 UI | 列表投影、分页/选择及请求绑定，保留 production 双确认；不执行真实恢复 | 历史/最新/过期/缺角色/错服务/错租户、同点演练及不回退 latest；退出：API/UI/回归和独立复核 |
| S2.8 通用批量回滚契约闭环 | OPS-05-02/E08、C2 回滚边界、逐目标来源任务 | 只消除 UI/后端策略承诺差异，并补逐目标人工审批回滚衔接；自动补偿设计须另行确认 | 失败停止、来源绑定、逐目标恢复、未知写入不重放；退出：与既有授权模型一致的本地闭环和复核 |
| S2.9 终端目标与记录闭环 | D2 回答、OPS-11-01/03/E05、Prune/终端表存储 | 若选择一次性：仅记录/查询/期限与必要 UI；若选择 PTY：先单独形成会话/录制合同，后续实现再拆子阶段，不与清理一次打包 | 一次性模式验证摘要/脱敏/超时/过期/幂等及 mock 时间清理；PTY 必须追加独立里程碑。退出：选定模式的本地证据与复核，或明确尚未实施 |
| S3.nn.<叶子> 单项本地功能验收 | 矩阵对应叶子 ID、E##、已有实现及匹配测试；有缺口先完成对应 S2 | 默认只验证；确需修复时限同一叶子并遵守授权，禁用真实生产目标 | 隔离正向/负向/失败/回滚、页面操作及相关套件，明确 mock 与真实边界；退出：关联最终版本结果、必要独立只读复核、记录未覆盖项。S3.12 的恢复点/隔离/R2/生产能力模拟各开一项 |
| S4.1 固定候选物 | OPS-18-01、所有相关 S2/S3 交接、最终候选源码与干净隔离构建来源、README/部署清单 | 只处理候选构建和必需门禁；不夹带 W0 暂存，不发布 | 同一源码 Web/Runner/适配器/配置、Linux/Python 3.12 跳过项、签名前材料校验；退出：可追溯候选物和所有必需本地证据，缺资源明确阻塞 |
| S4.2 制品发布与回验 | OPS-18-02、S4.1、release workflow/verify-release-assets.sh、既有版本发布情况 | 经批准才提交/推送/签名发布；先核对 1.1.11，不能重复发布混淆来源 | 校验版本/revision/manifest、固定签名身份、Web digest、Runner checksum 和渠道回读；退出：实际发布证据，尚不称部署 |
| S4.3 生产接入与发布前置核验 | H-P/H-B、OPS-18-03、当前规则与 inventory、目标 Profile、C0/部署清单 | 只读确认当前环境、权限/双账号、告警、任务、备份、源码/runtime/preflight；提交具体变更单元 | 退出：目标/备份/回退/窗口/验证/权限清楚，可审核部署方案；缺少批准或资源保持 blocked，不执行写入 |
| S4.4 C0 部署与控制面观察 | S4.2 制品、S4.3 有效批准单元、OPS-18-03/04、编排器及部署清单 | 仅该单元的控制面部署/备份/校验/分阶段回退，不开启其他模块 | Runner/Web 正式版本、socket/health/runtime preflight、状态/审计、观察及激活边界回退证据；退出：已部署和未验收模块分列，失败状态准确 |
| S5.nn.<叶子> 逐模块生产验收 | 对应叶子 ID 的 S3 结果、当前 C0 运行身份、C1–C7 或专项明确单元、最新门禁 | 每次一个模块/叶子与明确目标；只开启批准能力，范围外不动；跨机红线按规则复述确认 | 真实正向/负向/观察及必要回滚；危险失败演练优先隔离，不可执行的生产验证明确保留缺口。退出：按证据更新该行；待第二 Runner/真实 K8s 不关闭该项 |
| S5.19 最终逐 ID 关账 | 全部验收行及原始证据、实际部署版本、W0 保护记录 | 仅核对台账和必要原有文档；新增修复/清理另开批准单元 | 无未决/未实现/未验证/待资源项才可称最终目标完成；分开发布、部署、功能验收，不用构建代替；输出最终交接 |

S3/S5 的范围编号固定映射为矩阵的 01–18；同一范围有多个叶子时，交接写完整阶段名，例如 `S3.12.OPS-12-01`。S5.06 第二 Runner 就绪是 S5.05/S5.15 Fleet 的依赖，S3.12/S5.12 选点与隔离证据是生产恢复依赖；这不授权提前开始后续阶段。公共输入输出、持久化、复杂状态或权限行为有变更时，相关验证后必须安排独立只读复核，不能让复核替代测试或批准。

### 阶段交接约定与 S1 结果

每个阶段结束仅回写本文的对应 ID、证据和下一唯一目标；不创建平行验收报告。原始测试输出/部署记录可以作为证据保存于已有机制，但完成状态只在本文维护。交接至少写：阶段/状态、branch/HEAD/相关差异与用户改动保护、变更文件、通过 ID/证据、未决/阻塞、下一唯一目标/输入文件、实际命令结果、生产/提交/推送情况及临时资源。

| 阶段验收 ID（不计作产品功能通过） | 完成标准 | 本阶段结果 |
| --- | --- | --- |
| S1-01 | 规则、HEAD/分支、工作区保护与源码版本核对 | 已有证据通过：B0/W0；未发现 services/ 下更近 AGENTS.md；本地文档任务无需读取无关 inventory 或连接生产 |
| S1-02 | 18 类最终范围均有稳定 ID、用户标准、分层状态和依赖 | 已有证据通过：OPS-01-01 至 OPS-18-04 按范围覆盖；第二 Runner/Kubernetes 保留待资源，D1/D2 保留决策状态 |
| S1-03 | A–E 定向确认、已有证据边界与冲突可追溯 | 已有证据通过：E01–E05、H-L/H-A/H-P/H-B/H-D；另定向确认 E08 的批量 rollback 不一致，不扩为全仓审计 |
| S1-04 | 后续拆为单目标、有限输入、明确验证和退出条件 | 已有证据通过：S2–S5 子阶段表；下一唯一目标 S2.1，不自动开始 |
| S1-05 | 最终差异、引用、状态、覆盖及保护校验 | 已有证据通过：本会话 `git diff --check`；内联 Python 校验 40 个唯一 OPS ID、18 类覆盖、8 列逐项字段、状态枚举、60 个本地链接、各表列数及原内容保留；W0 暂存与 20 个未跟踪文件指纹不变。独立只读文档复核无实质问题；未运行全应用测试 |

本阶段实际验证入口为 `git status --short`、`git branch --show-current`、`git rev-parse HEAD`、限定路径的 `git diff b4ec0f9 --name-only`、`git diff --check`、`git diff --stat`、`git diff --numstat` 及本会话内联 `python3` 文档/指纹检查；结果关联 B0 + 本文未暂存差异。引用存在性不等于被引用功能已验收；独立复核也不替代运行测试。本次唯一修改文件为本文，既有 27 项暂存保持原样；无生产连接或变更，无提交、推送、发布，无新建测试服务、临时文件或资源。下一阶段唯一目标为 S2.1，须由用户另行发起。

治理收尾候选（仅报告，不自动修改其他文档）：C1 原“无需 stop/start”结论已在本文校正；Schema 的定时调度描述不能作为网页到期操作已通的证据；通用批量 rollback 选项与后端拒绝存在契约差异；终端留存不可继承通用 Prune 结论；旧部署草案的历史审批人数/编号须按其版本解释，不覆盖当前两账号流程及例外边界。未发现本阶段必须修改的治理规则；其他引用文件只核对，不重写。


## S2.1：定时计划网页闭环（2026-10-02，Asia/Shanghai）

阶段状态：`completed`，仅指本阶段本地已实现／已验证。对应稳定验收 ID 为 **OPS-03-02**，证据入口 **E01**。生产尚未部署／验收，整项生产完成状态仍保留缺口；不更新其他阶段状态，不自动进入 S3。

### 源码与差异身份

- 分支 `main`；HEAD `b4ec0f9268afa6f1b23259204c3544b798ad47f3`，未提交。证据绑定 **HEAD + 下列 12 个源码/测试文件的工作区内容**，不以 HEAD 代替修改后版本。
- 本阶段文件（相对 `services/areasong-ops/`）：`web/src/App.tsx`、`web/src/api.ts`、`web/src/types.ts`、`web/src/components/ConfirmationDialog.tsx`、`web/src/components/PlanScheduleDialog.tsx`、`web/src/schedule.ts`、`web/src/usePlanDialog.ts`、`web/src/styles/components.css`、`web/tests/api.test.mjs`、`web/tests/schedule.test.mjs`、`web/tests/schedule-browser.mjs`、`internal/runner/scheduled_plan_test.go`；另就地更新本文。
- 内容指纹 SHA-256：`2907e503f3203bbc8c2fb24641fbd2f4435243f5e6d70ff2cff4aa83078172f7`。复算方式：将上述 12 个相对路径按字典序排列，逐个向 SHA-256 输入 `UTF-8路径 + NUL + 文件原始字节 + NUL`；不包含自引用台账。
- 原有 27 项暂存差异保持完整，`git diff --cached` 的 SHA-256 前后均为 `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155`。本文原 S1 未暂存内容保留，仅更新 E01/OPS-03-02/S2.1 并追加本节；`scripts/local/` 其他工作未处理。

### 已实现行为与合同

复用中文文案、现有弹窗与样式。在 Release Plan 入口默认选择“立即执行”，允许改为指定时间；创建仅展示计划，不自动批准或执行。输入按浏览器 IANA 时区解析，提交 UTC ISO 时间；预览/批准弹窗显示完整日期、偏移与时区。切回立即模式不发送 scheduleAt。空值、非法日期和夏令时跳跃时间拒绝；后端原有合同允许已到期时间，因此网页也允许，避免失败重试跨到期后被额外限制。该时间只表示最早可执行时刻，不代表维护窗口授权。

创建幂等键按完整创建内容绑定，失败重试保留、改时间换键、成功后有意新建换键。执行仍只发送原有幂等键，不允许覆盖审批后的时间。弹窗每秒只刷新本地时钟：未来等待，到期后合法身份显式执行；终态禁用，列表刷新同步已有弹窗。加入提交中保护、真实错误展示、键盘焦点循环与 Escape。中低风险审批按钮按已有后端身份规则展示，保留合法旧双审批下一步；没有改变审批人数、权限模型、数据库结构、持久化、事务或审计语义。`requiresDualApproval` 仅补全已有响应字段的前端类型；`scheduleAt` 仅接通已有请求合同。

### 实际验证

- macOS arm64，Node `v25.1.0`，Go `go1.22.12`；前端 `npm test` **17/17 通过**，`npm run lint`、`npm run typecheck`、`npm run build` 全通过。覆盖时区转换（Shanghai/New_York）、非法日期/DST、到期边界、默认请求、时间修改与键、执行重试、后端拒绝及 CSRF 失效不自动重试。
- Go 默认 `go test ./internal/runner ./internal/store ./internal/model ./internal/web` 因本机链接器 `missing LC_UUID` 未运行成功；安全替代 `go test -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web` **四包通过**。后续新增测试实际运行 `go test -ldflags=-linkmode=external ./internal/runner -run 'TestScheduledPlanRequestBindingAndEarlyExecution|TestDueScheduledPlanExplicitExecutionReplay' -count=1` **通过**。隔离临时 SQLite 与 fakeExecutor 验证时间/摘要/幂等绑定、提前与错误身份拒绝、显式到期激活和同键任务重放；现有 store 测试覆盖可控时间下激活及审计原子性。
- 浏览器入口：[schedule-browser.mjs](../web/tests/schedule-browser.mjs)。先在 web 目录运行 `npm run dev -- --host 127.0.0.1 --port 4173 --strictPort`，再用已有 Playwright 模块与 Chromium 执行 `PLAYWRIGHT_MODULE=<已有playwright/index.mjs绝对路径> PLAYWRIGHT_EXECUTABLE=<已有Chromium可执行路径> node tests/schedule-browser.mjs`。本机使用 npm 缓存 `31e32ef8478fbf80` 中 Playwright `1.64.0-alpha-2026-09-14` 与浏览器缓存 `chromium_headless_shell-1234`，未安装依赖。默认匹配的 1244 浏览器缺失，显式使用已有 1234 后通过。
- 浏览器全部 API 被内存夹具拦截，非本地地址阻断；真实 App 与真实组件覆盖立即/定时创建、切回立即、双击创建、创建失败跨到期原键重试、独立批准、刷新换身份、到期前后、无自动写、显式执行拒绝/重试；并覆盖错误身份、审批不足、终态、旧四身份策略、中低风险重复/第二审批、Tab/Shift+Tab/Escape。可控浏览器时钟，无真实长等待。1280×900 与 375×812；窄屏无弹窗水平溢出，页面无 JS 异常。截图 [s2.1-due-mobile.png](../output/playwright/s2.1-due-mobile.png) 已人工视觉检查；截图是本地忽略目录证据，源码测试可复现。未验证 Safari/Firefox 或真实生产账号，不把 mock API 浏览器验收冒充生产链路验收。
- **历史截图保全说明（2026-10-03 追加）**：上述历史原图在 S3.2d2 回归期间被覆盖，原始字节不可核验；当前同名文件不再作为 S2.1 历史阶段原图使用。上项测试执行与当时人工检查记录保留。现存图片仅登记为 S3.2d2 当前版本回归证据，有限例外及对应关系见文末 S3.2d2 收口记录，不表示历史原图已恢复。
- 独立只读复核 `/root/s21_review` 完成：修正旧策略样本、中性执行身份提示、中低风险重复审批展示；最后指定复查无剩余问题。复核未自行运行测试，运行证据由主代理提供。
- 差异空白检查、台账 ID/引用/指纹检查、原暂存差异保护核对通过。没有生产连接、配置修改、业务操作、Git 提交/推送、发布/部署或全局配置变更。临时 Vite 与浏览器进程已停止，Go 临时数据由测试清理；保留本地构建和截图证据。

治理收尾：仅报告已有审批 helper 与后端中低风险限制差异这一候选，本阶段仅修正受影响弹窗，不扩展修改全局 helper 或治理文件。未发现需本阶段修改的规则或失效引用。下一步由规划对话安排后续阶段；S3.03/S5.03 的隔离综合验收与生产变更须分别发起授权，D1/D2 不属于本阶段阻塞。


## S3.1：租户新增／编辑网页（2026-10-02，Asia/Shanghai）

阶段状态：本地 `completed`（S3.1a 补齐只读详情与跨账号审阅后收口）。以下为 S3.1 历史实施证据，当前增量见 S3.1a。关联稳定验收 **OPS-07-01 / E02**；不关闭整个 RBAC，也不将 OPS-07-04 的隔离合成身份测试记为真实双账号生产验收。实现与验证均仅限本地；生产基线仍沿用历史 1.1.5、已发布制品 1.1.11，未连接刷新。

- **已实现边界**：[TenantDialog.tsx](../web/src/components/TenantDialog.tsx)、[tenantChange.ts](../web/src/tenantChange.ts) 经 [AccessControl.tsx](../web/src/views/AccessControl.tsx) 接入新增与名称编辑；App 返回创建提案结果并保留表单固定 expectedVersion，API 接收显式幂等键，类型增量扩展；样式仅调整租户行与弹窗换行。新增输入 id/displayName，状态固定 active；编辑 ID 只读、只允许名称变化并原样保留 active 状态。禁止默认／bootstrap／非 active／ID 非规范化租户编辑；空白、已知重复 ID、无变化均阻止提交。网页新增 ID 采用身份目录兼容的 2–40 位格式、名称 1–120 字符输入限制；后端租户写入本身未声明这些格式／长度上限，前端不替代权威校验。
- **请求合同**：仅传单个 tenants 条目、expectedVersion、requiresDualApproval 和 idempotencyKey，不携带 roles/bindings/principals/移除列表/enforced。后端是按 ID upsert 单条租户，非字段 patch；省略其他集合保持不变，重复 ID 在后端表示更新。创建只保存不可变审批载荷，租户校验与 bootstrap 保护在应用阶段执行。新提案独立账号批准、创建人应用；版本冲突及同提案重复应用沿既有 Store 事务、摘要与审计处理。失败保留表单，提交有同步锁；同身份同请求按请求内容分别使用 sessionStorage 保留键，A→B→A 不丢失键，刷新不自动发送请求。服务端返回旧 rejected 提案时清理该请求键并保留草稿、明确本次未新建；用户核对后再次提交才生成新键，不自动重建。
- **当时阻塞与最小补充（已由 S3.1a 解除）**：当时 AccessChange 返回摘要与状态，Store 明确不在列表回传 payload，另一账号无法从现有接口核对租户修改内容及旧值。已请求专项授权：新增受既有 manage_access 保护的只读租户详情投影，核验载荷摘要，仅投影 id/displayName/status、expectedVersion 与对应历史策略快照的旧值；混合角色／绑定提案不暴露完整载荷。尚未收到批准，未实施任何后端运行逻辑／读取端点／模型或持久化变更。不得据当前网页已有审批按钮认定安全可审阅闭环完成；也不能用创建者本地表单内容冒充服务端批准依据。剩余：获准后实现投影、前后差异展示及缺证阻断、跨账号/冲突验证和复核，再判断 S3.1 completed。
- **验证**：`npm test` 22/22；`npm run lint`、`npm run typecheck`、`npm run build` 通过（macOS arm64，Node v25.1.0，Go go1.22.12）。使用已有工具链执行 `go test -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web` 四包通过。新增 [tenant_change_test.go](../internal/runner/tenant_change_test.go) 的 `TestTenantProposalHTTPContract` 使用真实 Runner HTTP handler、临时 SQLite 和合成身份，覆盖创建未生效、错误摘要/自批/无权限/错误执行身份拒绝、独立批准创建人应用、同键重放／异载荷拒绝及创建/批准/应用审计各一条、版本冲突、保护对象、未知字段/空名称/disabled 应用拒绝、其他租户/角色/绑定/主体/开关不变；[tenant-change.test.mjs](../web/tests/tenant-change.test.mjs) 与 [api.test.mjs](../web/tests/api.test.mjs) 覆盖精确请求、字段/版本/空提案和键保留。既有 tenant_idor、访问审批原子性与 S2.1 Go 测试随四包回归。
- **浏览器**：[tenant-browser.mjs](../web/tests/tenant-browser.mjs) 使用真实 App 与内存 API、阻断非 localhost 请求，覆盖新增/编辑、创建未生效、版本冻结、重复点击、失败原键重试、自批按钮隐藏、独立批准/创建人应用按钮、冲突不自动写、刷新、撤销及同内容显式重建、新增绑定、无权入口、Tab/Shift+Tab/Escape、取消草稿及焦点归还。1280×900 / 375×812；[s3.1-tenant-mobile.png](../output/playwright/s3.1-tenant-mobile.png) 已人工视觉检查。原 [schedule-browser.mjs](../web/tests/schedule-browser.mjs) 全部回归通过。命令同 S2.1：web 目录启动仅 127.0.0.1:4173 的 Vite，用已有 npm 缓存 31e32ef8478fbf80 的 Playwright 与 chromium_headless_shell-1234，通过 PLAYWRIGHT_MODULE/PLAYWRIGHT_EXECUTABLE 执行两个脚本；没有安装依赖。浏览器模拟审批结果不是服务端差异读取、真实账号或生产验收。
- **历史截图保全说明（2026-10-03 追加）**：上述历史原图在 S3.2d2 回归期间被覆盖，原始字节不可核验；当前同名文件不再作为 S3.1 历史阶段原图使用。上项测试执行与当时人工检查记录保留。现存图片仅登记为 S3.2d2 当前版本回归证据，有限例外及对应关系见文末 S3.2d2 收口记录，不表示历史原图已恢复。
- **本地问题处理**：首次 Vite 启动 cwd 错误未启动服务，改为 web 后成功；测试桩 mock Node 内置 sessionStorage 挂起，已停止本任务测试进程并改为注入内存存储，重跑通过；浏览器修正旧绑定 select 定位与刷新后导航步骤，无产品规则削弱。未验证 Safari/Firefox。
- **工作区保护**：main / b4ec0f9268afa6f1b23259204c3544b798ad47f3；原 27 项暂存 SHA-256 为 4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155，核对不变。S2.1 专属八个源码/测试逐字节不变，共享文件按本轮基线做增量修改；scripts/local/ 未操作。没有修改 AGENTS.md、权限规则、数据库迁移、事务或后台运行逻辑；没有生产连接、配置、提交、推送、发布、部署。
- **独立复核**：只读子代理按本轮开始快照审阅本阶段增量，发现两个 P2（单槽覆盖 A 键、rejected 键生命周期）；均修正，代理定向回验确认原两项消除。其后指出旧 ID 大写/空白经后端规范化可能改变目标；主代理将这类记录从编辑入口拒绝并补断言。复核建议的同键异载荷测试已改为真实租户内容差异，增加审计各一条断言；新增 Chromium rejected 回包保留草稿、不自动重建、再次点击换键检查。最终前端22项/lint/typecheck/build、tenant-browser 及 `go test -ldflags=-linkmode=external ./internal/runner -run TestTenantProposalHTTPContract -count=1` 再次通过。S2.1 浏览器在共享 App/API/类型最终修改后通过；后续仅改租户专属文件。未覆盖 sessionStorage 被禁用/篡改、弹窗中跨会话身份切换的浏览器场景；后端现有身份门禁未改。
- **源码／差异指纹**：本阶段 11 个源码/测试文件，按相对 services/areasong-ops 路径排序，逐项 `path + NUL + bytes + NUL` SHA-256：`080c9fcbd5f43fb7184d17a1b01bdbc37ca3b8e2ad37e9d8b7f3d4c2995aac56`。集合为 internal/runner/tenant_change_test.go、web/src/{App.tsx,api.ts,types.ts,tenantChange.ts,components/TenantDialog.tsx,styles/control-plane.css,views/AccessControl.tsx}、web/tests/{api.test.mjs,tenant-change.test.mjs,tenant-browser.mjs}。相对于本轮开始工作区的增量证据 [s3.1-local.patch](../output/s3.1-local.patch) SHA-256：`978bc110fc8e6a2560d7c2d93c49a5840b156b03018e64ef7dcdf01a9821ba18`；不包含台账自身，不将原 S1/S2.1 差异计入 S3.1。patch/截图为本地忽略目录证据，非已发布制品。
- **资源**：临时 SQLite 由 t.TempDir 和 db.Close 清理；Chromium 在 finally 关闭；本任务 Vite 与挂起测试进程已停止。本轮临时基线在生成可核对增量 patch 后移除；保留忽略目录中的构建、截图、增量证据。

治理收尾候选（仅记录于本台账）：E02 原“后端字段存在即可复用现有链”不足以证明跨账号可审阅；提案详情缺失、upsert 重复 ID 语义和应用时校验须在后续同阶段补齐验收。未改治理规则；删除、停用、角色/绑定管理、配额和 D1/D2 均不在本阶段范围。

## S3.1a：租户提案只读详情与跨账号审阅（2026-10-02，Asia/Shanghai）

阶段状态：本地 `completed`；S3.1 同步为本地 completed。关联 **OPS-07-01 / E02**；仅补齐 S3.1 新增／名称编辑本地闭环，不关闭完整租户管理或 OPS-07-04 真实生产双账号验收。生产未部署／未验收。

- **授权与基线**：本轮已获本地专项授权。开始时 main / `b4ec0f9268afa6f1b23259204c3544b798ad47f3`；原 27 项暂存 SHA-256 `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155`，未暂存 tracked diff SHA-256 `09eb19ee848f236ec2d952463a2ff2f7b5384e3b05da8b51c69f4842dd108d4d`。本次以开始时文件字节记录增量，保护 S1/S2.1/S3.1；scripts/local/ 未操作，旧 s3.1-local.patch 未应用且指纹不变。
- **接口与裁剪**：[Runner](../internal/runner/access_change_detail.go) 新增 `GET /v1/access/changes/{id}/detail`，经已有 Web 代理映射 `/api/access/changes/{id}/detail`。仅返回 id、requestDigest、state、expectedVersion、currentVersion、reviewerHash（本次认证身份）、kind、availability，以及有效时的 operation/before/after；租户值仅 id/displayName/status。新增 before 缺省，网页显示“此前不存在”。无原始载荷、完整策略、其他租户、角色、绑定、主体列表或凭据；HTTP/fetch 均 no-store。
- **权威来源**：不可变 payload_json 内的 expectedVersion 绑定基准，核验载荷 SHA-256、当前模型编码和幂等键。Store 新增按 version 读取已有不可变快照的 SELECT 并核验摘要；before 只取历史快照，after 取确切提案并复用应用的 normalizeAccessTenant。只支持单租户新增／active 名称变更；保护对象、来源不明历史租户、元数据输入、混合／未知／多租户／删除记录不输出局部差异。版本漂移为 stale，缺快照／无固定版本为 unavailable，均不返回 before/after、不补建快照。
- **权限与网页**：入口及返回前复用既有 `authorizePlatform(access.manage, access)`，以当前策略授权，不增加权限或借历史身份放行。[AccessChangeReview](../web/src/components/AccessChangeReview.tsx) 按需读取，显示完整 ID／摘要／状态／基准及前后值；失败或失效清空并禁批。组件按身份／ID／摘要／状态／版本隔离，取消旧请求；API 核对 reviewerHash，批准前再次读取，POST 仍绑定原摘要及确认短语。已识别非租户提案提示不支持租户详情，沿原批准合同。原后端批准阶段不检查策略版本，应用事务拒绝 ExpectedVersion 冲突；本次未改变该规则，不声称网页可阻止绕过页面调用旧 API。
- **后端与前端验证**：macOS arm64、Node v25.1.0、Go go1.22.12。前端 25/25、lint/typecheck/build 通过；`go test -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web` 四包通过。[HTTP/SQLite 测试](../internal/runner/access_change_detail_test.go) 覆盖两个身份读取、真实应用值、未登录／无权／跨租户／撤权／不存在、字段白名单、版本漂移与不兼容记录、全部持久化表读取前后内容不变，以及自批／错摘要／错误执行人拒绝、批准重试／应用重放和原绑定审批应用回归。S3.1、S2.1 原 Go 测试随套件通过。
- **真实浏览器**：[Go 隔离入口](../internal/runner/tenant_review_browser_test.go)＋[脚本](../web/tests/tenant-review-browser.mjs) 以两个独立 Chromium Cookie 会话，连接真实 Web 认证／CSRF／Unix 代理、Runner 与临时 SQLite，完成新增／改名服务端审阅、独立批准、创建人应用并核对 SQLite 最终值。1280×900／375×812、失效禁批、完整摘要换行通过；[桌面](../output/playwright/s3.1a-review-desktop.png)、[窄屏](../output/playwright/s3.1a-review-mobile.png) 已视觉检查。复跑：先在 web 构建，再在服务目录执行 `S31A_BROWSER=1 PLAYWRIGHT_MODULE=/Users/as/.npm/_npx/31e32ef8478fbf80/node_modules/playwright/index.mjs PLAYWRIGHT_EXECUTABLE=/Users/as/Library/Caches/ms-playwright/chromium_headless_shell-1234/chrome-headless-shell-mac-arm64/chrome-headless-shell go test -ldflags=-linkmode=external ./internal/runner -run TestTenantReviewBrowser -count=1 -v`。普通 Go 套件有意跳过按需浏览器测试，本轮已显式执行通过。
- **故障注入及边界**：[真实组件测试](../web/tests/access-review-browser.mjs) 使用内存读取函数，验证失败清除、身份／提案切换及迟到响应、stale／unsupported、批准前漂移、120 字符窄屏和非租户批准；原 tenant-browser、schedule-browser 均回归通过（内存 API）。三者在 web 的本机 4173 Vite 上使用已有 Playwright 路径运行。未验证 Safari/Firefox、生产身份提供者、存储禁用及租户表单打开期间跨会话切换；详情本身不依赖持久缓存。
- **失败修正**：初次 TypeScript 类型名／图标导入错误、命令目录错误、浏览器输入定位歧义、组件夹具默认导出错误均修正后通过；视觉发现摘要继承 nowrap 截断，局部改换行并重建、重跑。未放宽产品规则或权限。
- **独立复核**：只读代理检查实际增量、原授权／审批／应用及 Web 代理，未发现阻断代码问题。指出 patch 落后于最后长文本测试的 P2，已重生成，代理以原 baseline 在内存重建全部 18 文件并确认逐字节一致。复核未独立复跑测试。保留边界：GET 与批准 POST 非原子；重复 JSON 键、提案键不匹配、快照摘要损坏及 GET 期间并发撤权未逐项执行故障注入，不将静态拒绝分支算作测试通过。
- **指纹与资源**：本轮增量 [s3.1a-local.patch](../output/s3.1a-local.patch) 排除台账自身及前阶段差异，SHA-256 `12e15c666c584662061ac536b88f36ea3caa8fca60b1e21488f4ba8b80ff3102`。18 文件按相对服务目录路径排序，以 `path + NUL + bytes + NUL` 计算集合 SHA-256：`98cdbd2f880e86698615eeb4d82f5c93f3d35545f8ca1d71a721f9b890d375b2`；文件集合可从 patch 的 `+++ b/` 行去掉 services/areasong-ops 前缀复算。临时 SQLite／HTTP／Unix socket 随 Go 测试清理，Chromium 在 finally 关闭；本次 Vite 和临时基线在交付前清理，保留忽略目录的构建、patch、截图。没有数据库字段／迁移、权限绑定／审批人数／执行人／事务语义变化，没有生产连接、生产数据读取、配置修改、提交、推送、发布、部署或系统依赖安装。

治理候选：明确区分“批准通过”和“可成功应用”，仅摘要状态不能证明可审阅；未自动修改 AGENTS.md／治理规则，未启动角色、绑定管理扩展或其他阶段。

## S3.2a：角色合同与最小只读差异（2026-10-02，Asia/Shanghai）

阶段状态：本地 `completed`（实现、验证及独立只读复核完成）。**S3.2 角色管理仍为部分完成：网页及完整交互验收待 S3.2b**。角色归属既有 **OPS-07-02 / E02**（原计划 S2.3 的实施细分）；承接 OPS-07-01 / S3.1 / S3.1a，不替换稳定验收 ID，不关闭 OPS-07-01 整体、完整 RBAC 或生产验收。

### 已核实的角色合同

- 来源：[模型](../internal/model/control.go)、[应用](../internal/runner/access.go)、[审批](../internal/runner/access_changes.go)、[原子写入](../internal/store/access_policy.go)、[默认角色及行持久化](../internal/store/access.go)。角色字段为 id/displayName/permissions/builtIn 及创建人、创建/更新时间；没有独立的“改 ID”合同。请求为按规范化 ID upsert：策略其他角色保留，同 ID 角色的名称与**完整权限数组替换**，不是权限增量合并；新 ID 表示新增，不表示改名旧 ID。
- ID lower+trim、名称 trim；ID/名称及权限数组非空，builtIn 必须 false。权限字符串不 trim、不排序、不去重；投影和应用共用提取后的 [normalizeAccessRole](../internal/runner/access_role.go)，保持原行为。Store upsert 保留已有创建人/创建时间、更新 updatedAt；这些元数据不开放网页输入或详情输出。
- 默认 viewer/operator/release-manager/platform-admin 由 Store 种子保护；既有 bootstrap 对象 ID 碰撞在应用事务内拒绝，incoming builtIn 拒绝。投影额外保守拒绝 BuiltIn、默认 ID、来源空/不明及 bootstrap 角色，包括默认种子不在历史策略地图中的情况。后续网页创建仅可输入 ID、displayName、完整 permissions；编辑固定 ID，只开放名称和完整权限数组。不得开放删除、元数据、builtIn、自动绑定或权限迁移。
- 已登记权限唯一来源为 model/control.go：ops.read、ops.inspect、ops.lifecycle、ops.deploy、ops.batch、ops.recover、fleet.manage、access.manage、config.manage、break_glass、runner.update。**既有写入仅检查权限非空，并未拒绝未知权限或 `*`；Role.Allows 将 `*` 视为全部权限。S3.2a 的已登记权限白名单仅限只读支持范围，不能当作原后端写入保护。** 后续若要将此限制变为不可绕过的后端规则，需要专项授权：最小方案在共享角色写入校验中拒绝未知/通配权限，影响旧调用方这类请求；验证创建/审批应用拒绝、事务不写入及正常角色回归；回退为撤回该校验代码，无迁移/存量角色改写。本阶段未执行此修复。
- 修改角色不改写现有绑定；绑定继续引用原 ID，权限随角色变更。删除仍有引用时既有应用拒绝，本阶段不开放删除。approve 校验独立人和摘要/短语，two_party_v1 由创建人 apply；GET 与 approve 非原子，最终 apply 事务仍负责版本冲突拒绝。

### 接口与安全边界

- 沿用 `GET /v1/access/changes/{id}/detail`，Web 代理 `GET /api/access/changes/{id}/detail` 不变。最小公共字段仍为 reviewerHash/id/requestDigest/state/expectedVersion/currentVersion/kind/availability；有效角色 kind=role、operation=create|edit，新增独立可选字段 `role`。旧租户顶层 before/after 与 create|rename 保持；角色永不填顶层 before/after。前端 [共享类型](../web/src/types.ts) 增加 role/edit/可选 reason，旧审阅消费者按 kind 隔离，不展示角色为租户、不据角色投影放行旧租户批准；角色网页留给 S3.2b。
- `role = {before?: {id,displayName,permissions}, after: {id,displayName,permissions}, permissionDiff: {added: string[], removed: string[], unchanged: string[]}, impact: {bindingCount: number, tenantCount: number, affectsExistingBindings: boolean}}`。新增不带 before。before/after 权限数组保留真实顺序和重复项；差集按集合去重并按权限字符串升序，空差集为 `[]`。名称编辑、顺序变化或重复项变化均不制造权限新增/移除。
- 原载荷 SHA-256、当前模型精确编码、幂等键核验后，按 payload.expectedVersion 读取**已有**历史快照并校验快照摘要。角色另拒绝不兼容快照编码及非 two_party_v1/无独立审批标记/创建身份异常记录。before 取该基准，after 取保存的确切提案及共享规范化，不接收客户端 before/diff/impact；不创建或修补快照。
- 影响统计只扫描同一基准的 Bindings：bindingCount 为全部引用绑定数（包括过期/非当前有效绑定），tenantCount 为这些绑定的去重具体租户数；有绑定即 affectsExistingBindings=true，表示角色修改影响既有引用，**不是当前有效主体或授权人数**。新角色正常为 0/0/false。字段不含 subject、邮箱、绑定列表、对象范围、其他角色或完整策略。既有 access.manage/access 平台读取授权涵盖该最小投影，入口与返回前继续以当前策略授权。
- 仅纯单自定义角色新增/编辑可 ready。角色混合租户/绑定/主体或其他写入、多角色、删除/删除旧 ID 再新增、未知字段/权限/通配符、受保护角色、元数据输入均 unsupported 且无局部 role。存在 principal.Roles 直接引用、通配租户绑定、缺失租户、重复绑定 ID 或新增角色却已有悬空引用时，返回 unsupported_reference_scope，避免将不完整影响统计写成零引用。
- 版本漂移 stale；无正基准/缺快照/摘要损坏 unavailable（载荷摘要损坏为 unsupported）；非 pending_approval/approved 不 ready。只有 ready 才附 operation/role，其他情况不返回角色前后值或统计。`reason` 为可选机器码：protected_role、role_metadata、invalid_role_or_permissions、incompatible_role、unsupported_reference_scope、incompatible_baseline、incompatible_approval；通用不支持/不可用由 availability 表达。不返回完整提案载荷或错误内部细节。

### 验证、差异与后续入口

- [HTTP 合同测试](../internal/runner/access_role_detail_test.go) 使用 httptest.NewServer 回环真实 HTTP 到 Runner handler、临时 SQLite、合成身份，完成申请/读取/独立批准/创建人应用/幂等重放，核对策略快照和角色行与投影一致；新增不生效、名称/权限编辑、顺序/重复项集合差异、3 绑定/2 租户（含过期绑定）、0 引用、字段白名单、未授权/跨租户/未知身份/未知 ID、撤权/过期/禁用、漂移及 apply 冲突通过。故意修改当前绑定行仍得到同一历史统计，恢复夹具后核对不变。
- [故障与保护测试](../internal/runner/access_role_detail_integrity_test.go) 覆盖载荷/快照摘要、重复 JSON 键、未知字段、幂等键不匹配、无固定版本、缺快照、旧审批合同、rejected、保护/历史角色及统计不可表达场景；重复 GET 前后全部 SQLite 表内容不变。默认四角色及 bootstrap 自定义角色经真实审批后，实际 apply 409 且持久化全表不变。原租户 detail/租户审批应用回归通过。
- 实际命令（服务目录）：`go test -ldflags=-linkmode=external ./internal/runner -run 'TestRoleChangeDetail|TestTenantChangeDetail' -count=1` 通过；最后 `go test -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web` 四包通过，runner 40.280s，其余包复用缓存。沿用项目记录的 macOS 外部链接方案，未重试已知 LC_UUID 默认链接问题。web 目录 `npm test` 26/26、`npm run typecheck`、`npm run lint`、`npm run build` 通过。未强行重复全站浏览器，S3.2b 的角色双账号网页、真实会话和生产验收尚未执行；GET 期间并发撤权的精确时序未注入，不将返回前授权代码阅读写成并发实测。
- 本轮开发遇到临时磁盘空间不足，首次编辑未落地；只读核对与本轮临时文件写入探针确认恢复后继续，没有清理用户磁盘文件。定向测试曾检出遗漏角色分派分支，已修正并重跑通过；一次测试命令目录错误未运行测试，切回服务目录后执行。以上不改变产品规则。
- 基线 main / `b4ec0f9268afa6f1b23259204c3544b798ad47f3`；原 27 项暂存 diff SHA-256 `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155` 不变，S1/S2.1/S3.1/S3.1a 保留，scripts/local/ 未修改。旧 s3.1a-local.patch 仍为 `12e15c666c584662061ac536b88f36ea3caa8fca60b1e21488f4ba8b80ff3102`，未应用。
- 本轮相对开始文件字节的增量 [s3.2a-local.patch](../output/s3.2a-local.patch) SHA-256 `995a8c5a63d68a93375ab8aa64e0d0350c7750c0890a7cc93ebd179156610339`；排除台账自引用及前阶段差异。8 文件集合为 internal/runner/{access.go,access_change_detail.go,access_role.go,access_role_detail.go,access_role_detail_test.go,access_role_detail_integrity_test.go}、web/src/types.ts、web/tests/access-change-review.test.mjs；按服务相对路径字典序逐项 `path + NUL + 原始bytes + NUL` SHA-256 为 `8a513d42a0a8ab655b1124c958254245d67e5b3603e3141b64ed2f31e79fa245`。patch 为本地忽略目录证据，不是已提交/已发布制品。
- 独立只读复核：默认只读代理检查本轮 8 文件实际增量与授权/Store/审批应用调用链，未发现阻断问题；确认规范化提取等价、权限不扩张、GET 无持久化写入、基准统计及混合提案拒绝、租户兼容，并在内存从原 baseline 应用 patch 后验证全部文件逐字节一致。未独立复跑测试。保留 GET/POST 非原子和并发撤权精确时序未注入边界；后续网页应明确“基准全部绑定数（含过期绑定）”，避免当作当前有效授权人数。
- 本阶段没有数据库结构/迁移、权限检查/绑定、审批人数/执行身份或事务语义变更；应用仅等价提取规范化函数。没有生产连接、配置修改、提交、推送、发布、部署或依赖安装。临时 SQLite/HTTP 随测试清理；本轮临时基线已于最终指纹/引用检查后清理，保留 patch 和本地构建证据。
- S3.2b 最小读取：本节；model/control.go；runner/access.go、access_role.go、access_changes.go、access_change_detail.go、access_role_detail.go 及两份角色测试；store/access_policy.go、access_change_detail.go；web/src/types.ts、api.ts、accessChangeReview.ts、components/AccessChangeReview.tsx、views/AccessControl.tsx，并参照 tenantChange.ts 与 components/TenantDialog.tsx 的现有提交流程。新增提案 `POST /api/access/changes`，body 最小为 `{roles:[{id,displayName,permissions}],expectedVersion:正整数,idempotencyKey:UUID,requiresDualApproval:true}`，不得附其他变更/元数据；Runner 对应 `/v1/access/changes`，202 返回 AccessChange（包括 ID、requestDigest、state、confirmationPhrase）。读取本文 detail；批准 `POST .../{id}/approve` body `{digest:requestDigest,confirmation:confirmationPhrase}`；创建人 `POST .../{id}/apply`，返回 AccessChange，最终 applied 才刷新生效态。编辑是同 ID 的全量 permissions 替换，不是新增/移除权限数组请求；前端须用完整 role.after 构建审阅，不得信任草稿差集，不把 ready 当作未来 apply 成功保证。

治理收尾候选：角色权限枚举写入校验缺口及直接主体引用/通配租户影响表达已记录，未自动修规则；未发现本阶段需改动的过时引用，未改治理路由文件，未启动 S3.2b。


## S3.2b：自定义角色新增／编辑网页与双账号本地验收（2026-10-03，Asia/Shanghai）

阶段状态：本地 `completed`（网页、必要验证、真实双会话及独立只读复核完成）。仅覆盖新增／编辑网页及本地可信审批应用链。关联 **OPS-07-02 / E02**；OPS-07-02 整体仍为部分完成，删除、未知／通配权限写入校验专项、既有绑定管理与生产验收未完成，不关闭完整 RBAC。S3.2a 上文“网页待完成”为当阶段历史状态，以本节为最新进展。

- **基线与保护**：main / `b4ec0f9268afa6f1b23259204c3544b798ad47f3`；本轮开始未暂存 tracked binary diff SHA-256 `7121ba4f2b03abbf17600825cff40b9402ed4ded7698bee7d2170bde95f558bd`。原 27 项暂存 SHA-256 `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155` 不变。开始时 S3.2a 八文件集合复算为 `8a513d42a0a8ab655b1124c958254245d67e5b3603e3141b64ed2f31e79fa245`，旧 patch `995a8c5a63d68a93375ab8aa64e0d0350c7750c0890a7cc93ebd179156610339` 未应用、未覆盖；S1/S2.1/S3.1/S3.1a/S3.2a 增量保留，scripts/local/ 未操作。
- **实现**：[roleChange.ts](../web/src/roleChange.ts)、[RoleDialog.tsx](../web/src/components/RoleDialog.tsx) 接入既有访问控制页，中文／现有主题与弹窗焦点机制。新增规范化 ID/name、已知 ID 碰撞、默认四角色、builtIn/bootstrap/来源不明及未知/通配/空权限阻断。编辑固定 ID，仅改名保留完整原权限顺序和重复项；用户勾选后按登记顺序提交确定的完整数组。无变化不发请求。权限枚举与 model/control.go 由测试核对，不开放任意字符串输入。
- **请求与生命周期**：仍用 `POST /api/access/changes`，只含单项 `roles:[{id,displayName,permissions}]`、正整数 expectedVersion、UUID idempotencyKey、requiresDualApproval=true；不附绑定或元数据。弹窗保存打开时策略与版本快照；提交前 GET access 核对当前主体、租户及管理资格，不替换版本。同身份／完整载荷／版本沿 sessionStorage 保留键，A→B→A、失败、关闭重开同载荷复用。写入不自动重试；rejected 重放保留草稿并清键，下一次显式提交才重建。只读身份核对不是原子身份锁；跨 Cookie 变化检测测试通过，不宣称 GET 与 POST 原子。
- **可信审阅**：[accessChangeReview.ts](../web/src/accessChangeReview.ts)、[AccessChangeReview.tsx](../web/src/components/AccessChangeReview.tsx) 使用独立 role 字段显示服务端保存的 before/after、完整权限与集合差集，不使用创建者草稿。影响为“基准版本的全部引用绑定，包含过期绑定”，不是有效用户数；不支持影响范围不得显示零影响。身份／提案／摘要／状态／版本变化清除旧展示，非 ready 禁批，批准前重新 GET 核验，GET/approve 非原子的事实保留，最终 apply 事务拒绝版本冲突。原租户顶层 before/after 和其他绑定审阅路径保留。独立批准后由创建人应用，正式列表依据服务端策略刷新，提案创建不表示生效。
- **单元及后端**：web 最终 `npm test` 31/31、`npm run lint`、`npm run typecheck`、`npm run build` 通过。服务目录 `go test -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web` 通过（Runner 40.543s，store/model/web 缓存；按需浏览器测试在普通套件中跳过，另行执行）。新增 [角色 HTTP 身份与重放](../internal/runner/role_change_test.go) 用回环 Runner HTTP＋临时 SQLite 覆盖同键异载荷、自批、无权限、错误摘要、错误执行人拒绝，审批重试、应用重放不改持久化、撤销与换键重建。既有角色详情完整性／漂移和租户测试继续通过。
- **真实双账号**：[Go 入口](../internal/runner/role_review_browser_test.go)＋[浏览器脚本](../web/tests/role-review-browser.mjs) 使用两个独立 Cookie 上下文，接真实 Web 认证接口/CSRF/Unix 代理、Runner、临时 SQLite。完成新增、仅名称编辑保留重复权限、已绑定角色权限新增/移除三次审阅批准应用；读取身份绑定、最小网络请求、创建未生效、2绑定/2租户含过期绑定、最终权限数组、完整绑定/租户/主体不变通过。认证主体为合成测试身份，并非生产身份提供者或生产双账号验收。S3.1a 真实租户双会话同时回归通过。复跑：先 web build；服务目录设置 `S32B_BROWSER=1 S31A_BROWSER=1 PLAYWRIGHT_MODULE=/Users/as/.npm/_npx/31e32ef8478fbf80/node_modules/playwright/index.mjs PLAYWRIGHT_EXECUTABLE=/Users/as/Library/Caches/ms-playwright/chromium_headless_shell-1234/chrome-headless-shell-mac-arm64/chrome-headless-shell` 后执行 `go test -ldflags=-linkmode=external ./internal/runner -run 'TestRoleReviewBrowser|TestTenantReviewBrowser' -count=1 -v`，角色 3.33s、租户 2.44s 通过。
- **组件、浏览器及视觉**：[role-dialog-browser.mjs](../web/tests/role-dialog-browser.mjs) 验证失败原键、载荷换键、双击锁、关闭重开、固定版本冲突、不自动写重试、撤销重建和取消零写；[role-review-fault-browser.mjs](../web/tests/role-review-fault-browser.mjs) 验证读取失败、非 ready、响应身份不匹配、身份/提案切换、迟到响应和批准前漂移。两者及原 tenant-browser、access-review-browser、schedule-browser 在仅回环 4173 Vite 上使用上述 Playwright/Chromium 路径通过；内存夹具只作故障补充。真实浏览器覆盖 1280×900／375×812、120 字长名称、权限选择、Tab/Shift+Tab/Escape、草稿提示及焦点归还。已查看 [表单窄屏](../output/playwright/s3.2b-form-mobile.png)、[审阅桌面](../output/playwright/s3.2b-review-desktop.png)、[审阅窄屏](../output/playwright/s3.2b-review-mobile.png)。没有新增语言或主题。
- **执行问题**：两次 npm 命令误在仓库根目录运行而未找到 package.json，切回 web 后执行通过。浏览器首次发现提交禁用后失焦使 Escape 无响应；增加失败返回弹窗焦点，故障和真实双会话重新通过。初次 Runner 全套因此失败，修复后最终四包通过。未降低断言或产品规则。
- **安全缺口与未覆盖**：后端旧角色写入仍仅检查权限非空，未知/通配权限可绕过网页，Role.Allows 仍把 `*` 视为全部权限；本阶段没有修改后端权限模型、授权检查、写入规范化、审批人数、执行身份、事务、结构或迁移。网页与 detail 支持限制均不是不可绕过的后端保护，生产启用评审必须显式处理该专项。未验证 Safari/Firefox、生产身份提供者、sessionStorage 禁用/篡改、GET/POST 间精确身份切换或并发撤权时序。删除、绑定管理、生产上线均留后续专项。
- **增量证据**：[s3.2b-local.patch](../output/s3.2b-local.patch) 排除台账自引用和前阶段差异；SHA-256 `86af45b72ebd297cff6615a4678317b748b208dc8e51b58cda9b73cfad654c16`。15 文件集合可从 patch `+++ b/` 行取得，去掉 services/areasong-ops 前缀、按字典序逐项 `UTF-8路径 + NUL + 文件原始bytes + NUL`，SHA-256 `c7652b0a82cabeddff5576e1f773dcf1af2d6f57c21711f309bda36540518b44`。已在临时目录从本轮开始字节执行 git apply --check/apply，15 文件逐字节与当前文件一致。忽略目录的 patch、截图、dist 均为本地证据，不是已发布制品。
- **独立复核与收尾**：默认只读代理按原始任务、本轮增量及已有测试证据复核，发现 P2：历史名称超过 120 字时仅调整权限也被迫改名。主代理修正为仅新增/实际改名限制长度，补 121 字历史名称单元与真实组件测试；最终前端 31 项/lint/typecheck/build、role-dialog-browser、真实角色双账号再次通过（TestRoleReviewBrowser 3.29s）。代理定向回验消除，独立执行角色单元 5/5，最终 15 文件 patch 内存重建字节一致，未发现其他阻断。代理未独立复跑完整 Go/浏览器。临时 SQLite/HTTP/Unix socket 随 Go 测试清理，Chromium 在 finally 关闭；本轮 Vite 与临时基线在交付前清理，保留忽略目录中的 patch/截图/dist。没有生产连接/数据读取、配置修改、提交、推送、发布、部署或系统依赖安装。本阶段结束即停止，不自动进入写入校验专项、删除或绑定管理。

治理收尾候选：继续保留权限枚举后端写入校验缺口及不可表达影响范围；本次提交禁用失焦问题已局部修复，可作为其他表单的后续核验候选，不扩展修改共享规则。未改 AGENTS.md 或治理路由。


## S3.2c：角色未知／通配权限写入校验（2026-10-03，Asia/Shanghai）

阶段状态：本地 `completed`（批准范围实现、验证及独立只读复核完成；最终 Go 四包在既有 Python 3.12.12 的隔离测试进程环境下通过，默认 Python 3.9 的范围外兼容缺陷保留）。关联 **OPS-07-02 / E02**，不关闭角色删除、既有绑定管理或生产验收。S3.2a/S3.2b 的缺口描述为历史事实，以本节为最新状态。

- **专项授权与边界**：用户在本对话明确批准创建前校验、最终应用前校验、详情共享登记判定及必要隔离测试、真实双会话、独立只读复核、台账与证据整理；窗口仅本次 S3.2c 本地阶段。未授权生产连接／数据读取、配置修改、提交、推送、发布或部署。权限种类、Role.Allows、Store 授权解释、初始化、审批人数／身份、摘要、幂等结构和事务均不修改，不清退存量角色或历史提案。
- **工作区保护**：main / `b4ec0f9268afa6f1b23259204c3544b798ad47f3`；本轮起点未暂存 binary diff SHA-256 `d327d51016c0288164d3422dae95a0344db20f2ff971ec8b6fda3ebe76acb9ff`。原 27 项暂存 SHA-256 `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155` 不变，S3.2b patch 与 15 文件指纹在实施前均与交接一致。修改前保存原文件字节及工作区内容清单，未 stash/reset/恢复旧文件/重放历史 patch。收尾发现 scripts/local/clash_audit/ 的并行修改与新增，未操作或纳入本阶段增量。
- **实际实现**：[access_role.go](../internal/runner/access_role.go) 将原投影的精确权限判定移为共享 registeredRolePermissions，继续引用 model/control.go 的 11 个登记常量；validateAccessRolePermissions 仅扫描全部 request.Roles。[access_changes.go](../internal/runner/access_changes.go) 在创建序列化／摘要／Store 前检查；[access.go](../internal/runner/access.go) 在构造 proposed 策略及 Store 写入前检查；[access_role_detail.go](../internal/runner/access_role_detail.go) 复用判定。normalizeAccessRole 函数字节不变；详情的保护对象优先级和支持范围不变。覆盖 POST 创建、schema 4 PUT 审批入口、schema 3 直接写及历史提案 apply，不只覆盖网页单角色路径。
- **输入与历史兼容**：未知值、`*`、`ops.*`、空字符串、空白／大小写变体及合法非法混合整体拒绝；空权限数组沿用“自定义角色必须至少包含一个权限”，其他非法值为“自定义角色包含未登记权限”。使用既有 409/error JSON，解码 400、授权 403 保留。合法数组顺序、重复项与原 ID/name 规范化不变。只检查本次提交角色，不扫描全策略或引用角色；存量未知／通配角色、bootstrap 和无关租户／绑定变更保留。存量非法角色仅改名仍需提交完整数组，携带原非法值会拒绝；显式全合法替换可沿原审批链应用，网页与详情的历史不支持范围不扩展。
- **历史提案状态**：旧 pending 可按原合同批准，旧 approved 最终 apply 409，保留载荷、摘要、审批、approved 状态及既有审计，不写 error/rejected 或自动清退。旧非法 payload 携原键重新创建、schema 3 已生效非法请求直接重放由原可能成功改为 409；历史 applied 提案同执行人 apply 仍返回原幂等结果。拒绝在 Store 事务前发生，本次授权成功的权限校验分支无新增失败审计；测试比较全部 SQLite 表，确认角色、绑定、策略版本／快照、收据和成功应用审计不残留部分写入。
- **定向证据**：[access_role_validation_test.go](../internal/runner/access_role_validation_test.go) 与 [access_role_compatibility_test.go](../internal/runner/access_role_compatibility_test.go) 共 12 个顶层角色写入测试；[原详情测试](../internal/runner/access_role_detail_test.go) 的四类非法请求改由隔离历史夹具保存，未删除旧覆盖。先运行两条拒绝回归，旧代码实际创建 202、历史 approved apply 200，测试变红；修复后同命令通过。`go test -ldflags=-linkmode=external ./internal/runner -run 'TestRoleWrite|TestRoleChangeDetail' -count=1 -v` 通过（11.397s）。覆盖 11 个登记值逐项新增／编辑、全合法组合、顺序重复、15 类非法数组×3 个 HTTP 入口、JSON 转义/null/类型错误、混合多角色及重复 ID 两种覆盖次序、pending/approved 最终拒绝、合法混合原子应用、旧角色显式替换、存量授权、历史 applied 和直接重放。最终测试 helper 抽取后存量兼容重跑通过。
- **完整检查与本地双会话**：Darwin arm64 / Go 1.22.12，沿已核实的 macOS 外部链接方案。初次指定四包命令通过（runner 53.377s，store/model/web cached）；最终使用既有 Python 3.12.12、`-count=1` 的四包全部实跑通过，结果与环境差异见下条执行记录。web `npm test` 31/31、lint、typecheck、build 通过；未修改前端源码。按 S3.2b 方法设置 S32B_BROWSER=1、S31A_BROWSER=1 及既有 PLAYWRIGHT_MODULE/PLAYWRIGHT_EXECUTABLE，运行 `go test -overlay=<临时映射> -ldflags=-linkmode=external ./internal/runner -run 'TestRoleReviewBrowser|TestTenantReviewBrowser|TestRoleWritePreservesLegacyPolicy' -count=1 -v` 通过：角色 2.88s、租户 1.55s。overlay 只将两个 Go 浏览器测试的 cmd.Dir 指向临时脚本副本，脚本原字节不变，用于隔离截图路径；真实 Web 认证接口／CSRF／Unix 代理／Runner／临时 SQLite 链保持。角色新增、名称编辑保留数组、已绑定权限替换及租户回归通过，身份为合成 Cookie，会话不代表生产身份提供者验收。已查看 [角色桌面审阅](../output/playwright/s3.2c-role-review-desktop.png) 与 [角色窄屏表单](../output/playwright/s3.2c-role-form-mobile.png)，另保留角色及租户窄屏／桌面截图，历史截图未覆盖。
- **执行记录与范围外问题**：最终 `go test -json -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web` 首次实跑时，恢复演练 TestRestorePlanRunsRealShellWithSelectedPointNotLatest 失败 `revalidatedAt is invalid`；94 个本轮角色测试事件通过。定位为本机 Python 3.9.6 datetime.fromisoformat 拒绝 Go RFC3339Nano 生成的五位小数秒 `.58474Z`，六位 `.584740Z` 可接受，已用相同字符串隔离复现；恢复测试 `-count=2` 连续通过。restore_contract.py、recovery_shell_integration_test.go、recovery.go 与本轮基线字节一致，不属于本次修改，未修复此历史兼容问题。原默认环境的完整有界复跑再次出现同一错误（另一五位小数秒 `.52613Z`），store/model/web 命中缓存通过。随后核实既有 `/Users/as/.local/bin/python3.12` 为 Python 3.12.12，可解析两条实际失败时间；只在临时目录建立 python3 映射并前置于该测试子进程 PATH，不改全局环境、不安装依赖、不改脚本或断言。设置 GOTOOLCHAIN=local、GOPROXY=off，运行同一四包命令并加 `-count=1`：runner 43.632s、store 7.948s、model 0.007s、web 0.142s 全部通过，无测试结果缓存，共 568 个通过事件（含子测试）。常规四包仅跳过 TestRoleReviewBrowser/TestTenantReviewBrowser，两者已另行真实运行通过。默认 Python 3.9 的兼容缺陷仍未修复，不能将替代环境通过表述为默认环境已修复。保留两次完整失败及最终隔离环境通过记录于 [Go 检查日志](../output/s3.2c-go-tests.jsonl)。
- **独立只读复核**：默认代理 fork_turns=none，收到原问题、用户批准边界、实际增量／原字节基线、兼容影响和测试证据；未发现具体绕过或回归。独立使用 GOTOOLCHAIN=local GOPROXY=off 运行 `go test -mod=readonly -ldflags=-linkmode=external ./internal/runner -run 'TestRoleWrite|TestRoleChangeDetail' -count=1` 通过（12.152s），内存重建 patch 7/7 与当前字节一致；另核对 normalize、model/control.go、Store、bootstrap、HTTP 和授权源码不变。未独立复跑四包、前端、浏览器或物理反向回退。历史 legacy 多人审批策略＋非法权限、重复 JSON 键／字段大小写的专用动态用例未新增；已静态确认沿同一解析后最终校验，不将其表述为逐项实测。
- **增量与回退**：本轮 [s3.2c-local.patch](../output/s3.2c-local.patch) SHA-256 `993726c7fb57abae7dbb6588611ed19abef0597fcd8fd7ef349a6a40a6d22cb6`；7 文件集合 SHA-256 `ecf7f12494099f6369fa0d44f157a19846c287b48b4e6eb305ef602fb11091db`。集合从 patch 的 +++ b/ 路径提取、去掉 services/areasong-ops 前缀、字典序按 `UTF-8路径 + NUL + 原始bytes + NUL` 复算。patch 相对本轮开始字节，排除前阶段差异与台账自引用。在隔离目录执行 git apply --check/apply，7/7 字节一致；再 --reverse --check/--reverse，原有字节与新增文件不存在状态完全恢复。回退仅撤回 S3.2c 增量，存在后续交叉编辑时逐块处理，不对工作区执行整体恢复；无 schema 或存量数据迁移。patch、日志和截图均为本地忽略目录证据，不是发布制品。

治理候选仅记录：旧 approved 非法提案无法由现有 pending-only 拒绝接口清退；Store/bootstrap 保留历史权限承载能力，未来新增外部入口须接入受控校验；Python 3.9 与 Go 可变小数秒存在已复现的范围外恢复测试兼容问题。未修改 AGENTS.md 或治理规则。生产未部署／未验收；角色删除、既有绑定管理及其他全局待办继续保留，本阶段结束即停止。

本轮收尾：临时 SQLite／HTTP／Unix socket 随测试清理，Chromium 在 finally 关闭；浏览器脚本副本和 Python 映射随临时目录清理，原字节基线在最终重建／保护检查后清理。保留 patch、Go 检查日志、本轮截图及本地 dist；没有生产连接、配置修改、系统依赖安装、提交、推送、发布或部署。


## S3.2d1：自定义角色删除合同与最小只读详情（2026-10-03，Asia/Shanghai）

阶段状态：`partially_completed`。关联 **OPS-07-02 / E02**。本阶段允许的最小只读投影、兼容验证与独立复核已完成；隔离测试发现既有最终 apply 未重算载荷摘要的保护缺口，未获专项授权、不修改写入，不宣称完整摘要保护已通过。S3.2d2 删除网页、完整角色／绑定管理及生产验收仍未完成；租户停用／删除等全局待办保留。原 40 个稳定验收 ID 不增删，不创建另一份台账。

后续状态（S3.2d1a）：摘要阻塞已解除，S3.2d1 的只读详情、兼容实现、同版回归与独立复核满足原退出条件，现为本地 `completed`。上方部分完成和下文缺陷证据保留为历史事实；不关闭 S3.2d2、OPS-07-02 整体或生产验收。

### 已核实的原删除合同与本阶段支持范围

- 原请求 `removeRoleIds` 的处理见 [access.go](../internal/runner/access.go)：创建提案保存原请求；应用时 `normalizeIDs` lower+trim、丢弃空值、去重并排序。不存在 ID 跳过；纯空／不存在 ID 仍可完成审批应用并新增一个策略版本，已通过真实 HTTP 刻画，不把它解释为详情可支持。没有独立“改 ID”语义；同请求 upsert 再删除同 ID 最终为删除，原链支持删除角色同时删除其绑定。本阶段不投影这些混合／多项／非规范请求，也不改变上述写入行为。
- Runner 先构造 proposed 策略，再检查 BuiltIn、所有剩余绑定（含过期及通配租户绑定）和所有主体直接 Roles 引用；即使主体停用或绑定过期，也不能留下角色引用。混合移除引用后可通过是原写入能力，不是级联删除。Store [access_policy.go](../internal/store/access_policy.go) 事务另检查 bootstrap 角色及剩余 `role_bindings` 行，先删除获请求的绑定，再删除角色；不另检查主体表，主体约束来自 Runner 的完整策略及事务版本校验。默认四角色的 Store 种子为 bootstrap，结合 Runner BuiltIn 与 Store bootstrap 拒绝；来源空／任意非身份来源并非独立写入禁止规则，仅在新详情保守阻断。
- `expectedVersion>0` 固定提交基准；原非正版本采用应用时版本。本详情只接收正整数基准和 `two_party_v1`／显式双人审批记录。批准核对提案摘要、确认短语和独立身份，创建人 apply；幂等键与身份／摘要、applied 成功重放保持既有合同。事务将角色行、策略快照、receipt、applied 状态和审计一起提交，失败不留下部分删除、额外版本或虚假成功审计。**这不等于最终应用重新计算 payload 摘要，缺口见下节。**
- 新详情仅支持原始请求恰有一个规范化自定义 ID，格式限定 `[a-z0-9][a-z0-9._-]*`；不主动 normalize 为可审阅请求。纯角色删除之外的角色 upsert、租户、绑定、主体、开关或 confirmation 一律不支持。默认四角色、BuiltIn、bootstrap、来源非合成身份格式、缺失角色、ID 歧义／别名、未知／通配／空权限均阻断。这些额外限制是本阶段只读支持范围，不是新增后端删除规则。

### 响应与读取边界

沿用 `GET /v1/access/changes/{id}/detail` 及 Web `/api/access/changes/{id}/detail`。入口和返回前均保留既有 `access.manage` 对 `access` 的当前授权。公共 `reviewerHash/id/requestDigest/state/expectedVersion/currentVersion/kind/availability/reason` 合同保持。删除使用 `kind=role_deletion`，仅 ready 附 `operation=delete` 和：

```json
{"roleDeletion":{"before":{"id":"custom","displayName":"待删角色","permissions":["ops.inspect","ops.read","ops.read"]},"references":{"bindings":"none","directPrincipals":"none"}}}
```

没有 `after`、权限差集或主体／绑定列表；权限原数组完整保留顺序和重复项。原角色 `role.after` 仍必填，租户及角色新增／编辑字段不变。旧 `reviewAllowsApproval` 的 kind 白名单拒绝新 kind，原审阅组件不显示角色新增／编辑差异或批准操作；本轮只扩展共享类型和运行兼容测试，未改组件／按钮／审批入口，不将此视为删除网页验收。

before 与引用判断均取 `expectedVersion` 对应已有快照，先验 payload 摘要／精确编码／幂等键及快照摘要／精确编码，不取当前角色拼接历史，不补建或修复快照。引用完整扫描同一基准：普通或过期绑定返回 `binding_reference`；直接主体引用返回 `principal_reference`；通配／缺失／非active租户、缺失或歧义角色引用、损坏主体、重复或损坏绑定等返回 `unsupported_reference_scope`。阻断仅返回机器原因，不返回角色或零统计。非 ready 均不赋值 operation／投影；缺基准、损坏、版本漂移、非可审阅状态等保持安全失败。ready 仅证明本次基准审阅，不承诺 apply 成功；GET 与 POST 非原子，事务仍可因新增引用或版本变化拒绝。

### 范围外写入摘要缺口与专项方案

[access_changes.go](../internal/runner/access_changes.go) `ApplyAccessChange` 只反序列化已存 payload，将提案行 `RequestDigest` 传到 Store；事务比较摘要字段，却不重算 payload。没有发现正常 HTTP 修改已存 payload 的入口，复现前提是受控临时 SQLite 的故障注入，不能写成已发现外部越权入口。

[TestRoleDeletionExistingPayloadDigestGap](../internal/runner/access_role_deletion_contract_test.go) 已真实复现：创建两个合成角色，独立批准删除 A 后仅改 `payload_json` 为删除 B、保留摘要；新 GET 拒绝，原 apply 却返回 200，A 保留、B 消失。用例明确标注 KNOWN GAP，其通过代表缺陷刻画成功，**不代表应用摘要保护通过**；专项修复时须将其改为拒绝及全表不变断言。已保留 [定向日志](../output/s3.2d1-targeted-tests.log)，未改写入合同。

最小专项建议：Runner 在解析／应用前重算已存载荷摘要并比较提案摘要；Store 在最终事务内重新读取并验证载荷，覆盖读取到事务之间的变化。影响仅为拒绝损坏或不一致提案，不改变合法双人审批、执行身份和成功重放。验证需覆盖批准后改载荷、并发变动、错误摘要、全表原子不变和合法重放。回退撤回该独立代码增量，无 schema／数据迁移；不修补存量提案。此方案未执行，须用户另行专项批准；S3.2d2 不应将本阶段解释为完整应用摘要保护已就绪。

### 验证、独立复核与证据

- 复用真实回环 HTTP、已有身份夹具和临时 SQLite。新增 4 个 Go 文件（1 个只读实现、3 个测试文件），覆盖准确历史 before、提案／重复读取不删除、GET 全部持久化表内容不变、普通／过期／直接主体／异常引用、保护角色、纯单／混合／歧义请求、身份／跨租户／撤权／失效、payload／snapshot 摘要及历史编码、独立批准／创建人应用／成功重放、版本与新增引用拒绝、Store 行引用及审计失败回滚；原新增／编辑和租户详情回归通过。
- 独立默认只读代理 `fork_turns=none` 查实际增量、原字节及原合同，发现非active租户漏判；主代理先用两项 HTTP 测试复现 ready，日志 [tenant-status-repro](../output/s3.2d1-tenant-status-repro.log)，再仅在新只读辅助函数复用 `tenantIsActive` 收紧并重跑通过。代理最终复核确认修正充分、无新增量问题，并核实写入／授权／S3.2c／旧消费者字节不变；还发现并复核上述既有摘要缺口。代理实际独立运行前端定向 4/4；Go 为读取主代理日志，未独立重跑。最终台账／patch 重建由主代理验证。
- 主代理最终定向命令：`GOTOOLCHAIN=local GOPROXY=off go test -ldflags=-linkmode=external ./internal/runner -run 'TestRoleDeletion|TestRoleChangeDetail|TestTenantChangeDetail' -count=1 -v`，9.076s 通过。macOS arm64、Go 1.22.12；核实已有 `/Users/as/.local/bin/python3.12` 为 3.12.12，以临时目录 python3 软链接仅前置测试子进程 PATH，未安装、未改全局 PATH 或范围外恢复脚本。四包完整命令 `go test -json -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web -count=1` 最终全部实跑通过：runner 53.097s、store 8.848s、model 0.009s、web 0.380s，662 pass 事件（含子测试及已说明的缺口刻画），无测试结果缓存。此前一轮659事件与最终轮均保留于 [Go 日志](../output/s3.2d1-go-tests.jsonl)，按各包最后 start 分段复核，不混加两轮。
- web `npm test` 32/32、lint、typecheck、build 通过。未改 UI 行为，角色／租户浏览器在四包中按原开关 skip；未执行删除网页验收、生产身份提供者或实际生产数据验证。GET 中途撤权精确并发窗口未注入；已验证撤权后请求拒绝，入口／返回前校验沿用原代码。默认 Python 3.9 缺陷未修复，不能把替代环境通过写成默认环境通过。
- main / `b4ec0f9268afa6f1b23259204c3544b798ad47f3`。开始复算原27项暂存 SHA-256 `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155`、S3.2c patch `993726c7fb57abae7dbb6588611ed19abef0597fcd8fd7ef349a6a40a6d22cb6`、七文件集合 `ecf7f12494099f6369fa0d44f157a19846c287b48b4e6eb305ef602fb11091db` 均一致。先保存字节基线，再做必要共享文件增量；未 stash/reset/checkout/应用旧 patch，scripts/local 并行工作未操作。
- 本轮 [s3.2d1-local.patch](../output/s3.2d1-local.patch) SHA-256 `9862550331dcf362cbac1b4017cc30c323862c6e3d0ea24a90061950010e6c07`；7 文件集合 SHA-256 `7ca8df054b1fbed9d8277dc48bcf9c7875863951bee9e5cac443627fcff76664`。集合从 patch 的 `+++ b/` 提取，去 `services/areasong-ops/` 前缀，字典序逐项 SHA-256 输入 `UTF-8路径 + NUL + 原始bytes + NUL`。文件为 `internal/runner/{access_change_detail.go,access_role_deletion_detail.go,access_role_deletion_test.go,access_role_deletion_scope_test.go,access_role_deletion_contract_test.go}`、`web/src/types.ts`、`web/tests/access-change-review.test.mjs`。patch 相对本轮原始字节，排除前阶段差异和台账自引用；隔离目录 git apply --check/apply 7/7 与当前字节相同，--reverse --check/--reverse 恢复原字节和新增文件不存在状态。后续交叉修改不得整体反向覆盖工作区。

S3.2d2 最小入口：本节、model/control.go、runner/access_change_detail.go／access_role_deletion_detail.go／access_changes.go／access.go、store/access_policy.go，以及 web/src/types.ts／api.ts／accessChangeReview.ts／components/AccessChangeReview.tsx／views/AccessControl.tsx。请求为 `POST /api/access/changes` 的 `{removeRoleIds:[id],expectedVersion:正整数,idempotencyKey:UUID,requiresDualApproval:true}`；批准仍 `POST .../{id}/approve`，body `{digest:requestDigest,confirmation:confirmationPhrase}`，创建人 `POST .../{id}/apply`。未来删除网页必须独立消费 roleDeletion 和 delete 标识、重新读取详情并安全处理非ready；本轮未接入，写入摘要缺口需先单独定界处理。

治理候选仅记录：最终 apply 缺少载荷重新验摘要；详情的引用资格必须与既有非active租户拒绝口径一致。未修改规则或 AGENTS.md。临时 SQLite／HTTP、Python 映射和重建目录由测试／上下文管理器清理，原字节基线在复核和最终保护检查后清理；保留本地 patch、上述日志和 dist，不生成第二份报告／台账。无生产连接、生产数据、配置修改、系统安装、提交、推送、发布或部署。本阶段结束即停止。

## S3.2d1a：最终应用原始载荷摘要校验（2026-10-03，Asia/Shanghai）

阶段状态：本地 `completed`，安全修复结果 `fixed`。关联 **OPS-07-02 / E02**。S3.2d1 原摘要阻塞由本专项解除，原部分完成与故障证据保留。40 个稳定验收 ID 不增删；删除网页、既有绑定管理、租户停用／删除和生产验收继续待办，不进入 S3.2d2。

- **授权与工作区**：仅本次本地实施、隔离验证、只读复核和交接。起点 main / `b4ec0f9268afa6f1b23259204c3544b798ad47f3`；原27项暂存 SHA-256 `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155` 起止一致。开始核对 S3.2d1 patch `9862550331dcf362cbac1b4017cc30c323862c6e3d0ea24a90061950010e6c07`、七文件集合 `7ca8df054b1fbed9d8277dc48bcf9c7875863951bee9e5cac443627fcff76664` 均匹配。修改前保存原字节；未 stash/reset/checkout/重放旧 patch，scripts/local 并行工作未操作。
- **实际修改**：[Runner](../internal/runner/access_changes.go)、[Store](../internal/store/access_policy.go)、[原缺口回归](../internal/runner/access_role_deletion_contract_test.go)、[新增载荷测试](../internal/runner/access_change_payload_test.go)、[Store 原子夹具](../internal/store/access_policy_atomic_test.go)，加本唯一台账。Runner 在原 applied 重放、授权／状态／身份检查后、JSON 解析和执行期转换前重算原始 payload SHA-256。Store 在原最终事务读取 payload，沿用 AccessChangeDigest 与提案摘要绑定，在未 applied 分支重算 payload 摘要，之后才写策略。策略 RequestDigest／snapshot.Digest 与原审批摘要不混用。
- **兼容与错误**：尚未应用的损坏载荷拒绝；Runner 沿用 409/error“访问策略审批载荷损坏”，Store 沿用 ErrIdempotency，不泄露原文。不修补摘要、重新批准、清退或自动改写提案状态。合法请求、审批人数／身份、幂等和事务边界保持；applied 原执行人重放先于新校验，不重建当前策略。不附加精确 JSON 编码、未知字段或重复键限制；合法缩进和空白载荷可应用。无 schema、配置或存量数据迁移；schema 3 非审批直接写、bootstrap 和初始化不纳入加固。
- **红绿与原子证据**：[红灯](../output/s3.2d1a-red.log) 在原代码上实际得到 `200 want 409`；原同名测试已转为拒绝保护，先 POST apply（不依赖 GET），再查 GET，A/B 均保留且注入后全表不变。[定向日志](../output/s3.2d1a-focused.log) 覆盖空载荷／无效 JSON（匹配及不匹配摘要）、空白变化、Store 独立入口、载荷单改和载荷加摘要同改。变化采用隔离夹具固定“读取并验证→注入→updateAccess continuation／Store 最终事务”，不用 sleep；**不是运行中 HTTP/goroutine 的精确并发注入，也未模拟事务读取后的并发写入**。原并发只提交一次测试仍通过。全表比较从注入完成后开始，含业务行、版本、快照、receipt、审批状态、审计；这些完整性拒绝分支不新增失败审计，不泛化到授权失败。Store 旧夹具改用 `{}` 的真实摘要，引用拒绝和收口审计失败明确断言错误原因，避免提前摘要拒绝伪装回滚通过。
- **独立复核**：默认配置 `fork_turns=none` 的只读调查及一次候选复核完成，未修改或再委派。候选复核未发现摘要混淆、绑定绕过或实现回归；发现旧“执行人撤权”测试实际撤销另一身份，以及旧策略 Runner 链证据不足。主代理核实并补 [复核回归](../output/s3.2d1a-review-regression.log)：two_party_v1 与旧双批准策略均经真实回环 HTTP，实际执行人自撤权后 GET 403，原 apply 重放 200、同结果且无持久化增量；旧策略两名批准人、独立执行人、自批和错误执行人拒绝均验证。补充测试及最终台账由主代理自查，未宣称代理二次独立实跑。
- **最终检查**：[运行时](../output/s3.2d1a-runtime.log) 为 Darwin arm64 / Go 1.22.12 / Node 25.1.0。已安装 Python 3.12.12 通过临时 python3 映射仅用于测试子进程 PATH；默认 Python 3.9 可变小数秒缺陷未修复。最终 `GOTOOLCHAIN=local GOPROXY=off go test -json -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web -count=1` 全部实跑通过：runner 49.338s、store 7.639s、model 0.007s、web 0.163s，673 个含子测试 pass 事件，无测试结果缓存；[完整日志](../output/s3.2d1a-go-tests.jsonl)。S3.2c 权限、删除引用、版本冲突、角色新增编辑删除及绑定链保持。web `npm test` 32/32、lint、typecheck、build 通过，日志为 output/s3.2d1a-web-{test,lint,typecheck,build}.log；前端源码未改。
- **真实双会话**：[浏览器日志](../output/s3.2d1a-browser.log)：角色 2.46s、租户 1.48s。沿既有 PLAYWRIGHT_MODULE／EXECUTABLE，S32B_BROWSER=1、S31A_BROWSER=1，执行 `go test -overlay=<临时映射> -ldflags=-linkmode=external ./internal/runner -run 'TestRoleReviewBrowser|TestTenantReviewBrowser' -count=1 -v`。映射仅改变 Go 测试脚本目录，脚本原字节不变；截图另存 output/playwright/s3.2d1a-*，未覆盖历史图片。两独立合成 Cookie→真实 Web 认证／CSRF／Unix 代理→Runner→临时 SQLite；角色新增／编辑审阅批准应用及租户新增／改名通过。已查看角色桌面审阅与窄屏表单截图。四包默认跳过这两个按需测试，单独显式运行已补齐。删除仅 HTTP／Store 验证，没有删除网页或生产身份验收。
- **增量与回退**：[patch](../output/s3.2d1a-local.patch) SHA-256 `bcad6f82da4cbc468fe0a8d95521d1f2c6e3f6880b063a70494abe74d39e711b`；5 文件集合 SHA-256 `3eb2072913c5bed6436c37501c28cf7d77ac05d4f83f2f2541c4c6eda0710c97`；[指纹与重建](../output/s3.2d1a-integrity.log)。patch 相对本轮开始字节，排除前阶段差异与台账自引用；去 services/areasong-ops 前缀，字典序输入 `UTF-8路径 + NUL + 原始bytes + NUL`。隔离 git apply --check/apply 5/5 字节相同，再 --reverse --check/--reverse 恢复全部起点字节及新增文件不存在状态。只撤回本轮增量，交叉编辑须逐块处理；回退重新暴露摘要缺口。

临时 HTTP、SQLite、Chromium、Python 映射及重建目录已清理；原字节基线于最终保护检查后清理。保留必要本地日志、patch、截图和 dist。台账首次写入脚本因字符编码解析失败，未产生写入；改用明确 Python 3.12 和文件补丁完成，随后检查差异、引用及稳定 ID。

威胁边界：不防止任意数据库写权限者同时伪造 payload、摘要及全部审批记录；不增加签名或外部可信存储。治理候选仅报告：旧测试名称不能替代执行身份核验；更广编码规则、默认 Python 兼容问题继续保留。未修改治理规则，无生产连接／数据读取、系统安装、全局配置、提交、推送、发布或部署；OPS-07-02 整体仍部分完成。

## S3.2d2：自定义角色删除网页与双账号本地闭环（2026-10-03，Asia/Shanghai）

当前阶段状态：**本地 `completed`；两张历史截图原件缺失，已经用户接受为有限证据保全例外。** 接受范围、核验及后续保护见本节收口记录；OPS-07-02 整体及生产验收仍为部分完成。

原交付状态（历史记录，保留）：`partially_completed`。**删除网页、必要运行验证与独立复核已完成；历史截图保全未完全满足**。关联 OPS-07-02 / E02，40 个稳定验收 ID 不增删；完整 RBAC、绑定管理、租户停用／删除及生产发布验收仍未完成。

- **历史证据缺口**：原 tenant-browser／schedule-browser 的固定输出覆盖了 `output/playwright/s3.1-tenant-mobile.png`、`s2.1-due-mobile.png`。忽略目录图片未被源码基线 ZIP 收录，未找到可核验原字节，不能声称已保留或恢复。现图另存 `s3.2d2-regression-s3.1-tenant-mobile.png`、`s3.2d2-regression-s2.1-due-mobile.png`，仅作本轮证据；后续回归改用隔离工作目录。恢复旧截图仍需会话外原始副本；源码、旧 patch 与已核对历史指纹不受影响。
- **工作区与授权**：仅本次本地实施、合成身份／临时 SQLite、复核与交接。main / `b4ec0f9268afa6f1b23259204c3544b798ad47f3`；原27项暂存 SHA-256 `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155` 起止不变。S3.2d1a patch `bcad6f82da4cbc468fe0a8d95521d1f2c6e3f6880b063a70494abe74d39e711b`、五文件集合 `3eb2072913c5bed6436c37501c28cf7d77ac05d4f83f2f2541c4c6eda0710c97` 均保持。起点原字节保存于 output/s3.2d2-baseline.zip，口径见 s3.2d2-start.log。既有 internal 文件逐字节未改；未 stash/reset/checkout/重放旧 patch，scripts/local 并行工作未操作。
- **入口与请求**：[RoleDeletionDialog.tsx](../web/src/components/RoleDeletionDialog.tsx)、[roleChange.ts](../web/src/roleChange.ts)、[AccessControl.tsx](../web/src/views/AccessControl.tsx) 接入单个明确角色条目的删除提案。默认四角色、内置／bootstrap／来源非身份哈希、非法历史角色、已知绑定（含过期）及主体直接引用阻断。列表缺引用不代表已证明可删，完整范围由服务端基准详情判断；不探测性提交、不解绑、不级联、不清退。POST 仅 removeRoleIds 单项、固定正整数 expectedVersion、UUID idempotencyKey、requiresDualApproval=true。提交前只读核对身份／租户／管理资格与已知引用，不替换版本；失败与关闭重开保留同键，rejected 重放清键后由下一次显式提交重建。取消不写入，关闭不撤销已有提案；Enter 默认不提交。中文、现有主题、焦点循环与归还复用。
- **审阅与应用**：[accessChangeReview.ts](../web/src/accessChangeReview.ts) 与 [AccessChangeReview.tsx](../web/src/components/AccessChangeReview.tsx) 增加独立 role_deletion/ready/delete 分支，绑定提案 ID、摘要、状态、版本、two_party_v1；before 完整、references 两项必须确为 none，异常结构或混入其他投影拒绝。权限顺序与重复保留，引用表述为“基准版本未发现绑定及主体直接引用”。身份／提案切换、迟到详情、读取失败与非ready不沿用旧值，批准前重读。App 通用应用接入 [accessChangeApply.ts](../web/src/accessChangeApply.ts)，核对当前创建人、独立批准、提案与可信详情；损坏、引用、版本漂移或未知分支不发 apply。响应丢失只读恢复，applied 终态不因失效 before 重写；确认 applied 且刷新目标不存在后才提示删除成功。GET/POST 非原子，最终保护仍依赖未修改的 Runner/Store 摘要、身份、版本、引用与事务检查。
- **单元与后端**：最终 web npm test 37/37、lint、typecheck、build 通过，日志 output/s3.2d2-web-*.log。Darwin arm64 / Go1.22.12，既有 Python3.12.12 仅通过临时测试子进程 PATH 映射。`GOTOOLCHAIN=local GOPROXY=off go test -json -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web -count=1` 全过：runner 55.941s、store 10.601s、model 0.009s、web 0.162s；673测试pass＋4包pass，3个按需浏览器默认跳过后另行补齐，见 [Go日志](../output/s3.2d2-go-tests.jsonl)。自批／错误身份／摘要、普通及过期绑定／主体引用、版本冲突、原子拒绝、正常重放及S3.2c/S3.2d1a载荷故障保护继续通过。后续仅改前端和浏览器脚本，Go证据对应的源码未变。
- **真实双账号与兼容**：[Go入口](../internal/runner/role_deletion_browser_test.go)＋[浏览器脚本](../web/tests/role-deletion-browser.mjs) 使用两个独立合成 Cookie，经真实 Web认证／CSRF／Unix代理／Runner／临时SQLite，完成网页创建角色→删除提案→另一会话真实详情批准→创建人应用→刷新不存在。分别证明创建与批准时角色仍存在，丢弃成功apply响应只写一次，版本漂移零apply，无关角色／绑定／主体／租户不改写。最终删除2.86s、角色新增编辑2.29s、租户1.64s全过，见 [三链日志](../output/s3.2d2-regression-browser.log)。角色／租户旧脚本通过临时 Go overlay 仅改脚本工作目录，隔离截图且不改原字节。八组组件／故障脚本通过，覆盖删除弹窗、迟到通知、删除／角色／租户审阅和定时计划，见 [组件日志](../output/s3.2d2-component-regression.log)。原绑定HTTP/Store及other分支保留，未新增绑定管理网页。桌面／375px、120字长名称截图已查看；50项重复权限通过几何断言，无新增语言或主题。
- **独立复核**：默认配置、fork_turns=none，只读且不派生。初轮独立9/9单测通过，发现P2：父组件创建／应用通知缺少身份切换后的迟到保护；[红灯](../output/s3.2d2-lifecycle-red.log) 实际复现 create/actor 1!==0。主代理增加身份／租户代次，切换、往返与卸载作废旧结果；代理定向复核并独立运行创建／应用×身份／租户／往返六场景通过，未发现新增阻断。卸载仅静态核验。主代理最终回归通过；旧脚本一项偶发断言在 React render 提交前取count，三份相关脚本改为先观察 DOM 身份再执行原零按钮断言，未放宽规则；最后测试同步由主代理验证。
- **增量与回退**：[本轮patch](../output/s3.2d2-local.patch) SHA-256 `5b8be7037ea8af1fa1860feca99442a1a83fe15800ab10e893ec40830ee4de74`；17文件集合 SHA-256 `ae2e8e0ab6d192846097368eba87e71f9a1336c4e8c529d1ff68aa3782969c96`，见 [完整指纹](../output/s3.2d2-integrity.log)。相对本轮起点，排除前阶段、台账自引用与输出证据；去服务前缀、字典序按 UTF-8路径＋NUL＋原始bytes＋NUL。隔离 git apply 正向17/17字节一致，反向恢复原字节与新增文件不存在状态。回退只撤本轮增量，交叉修改逐块处理，保留S3.2c/d1/d1a；代码回退不恢复已删数据。
- **未覆盖与资源**：未验生产身份提供者、生产数据、Safari/Firefox、sessionStorage禁用/篡改、GET/POST间精确竞态；合成验证不代表生产验收。默认Python3.9可变小数秒缺陷、存量清退、绑定管理及租户停用／删除保留。临时服务／SQLite／socket／Chromium／Python映射由测试与收尾清理，保留必要日志、截图、基线、patch及dist。无生产连接／读取、系统安装、全局配置、提交、推送、发布或部署；本轮到此停止。

治理候选仅报告：历史脚本固定截图路径易覆盖旧证据，React异步提交后的检查应等待可观察状态。未改治理规则。执行纠正与截图缺口见 output/s3.2d2-runtime.log。

### S3.2d2 有限证据保全例外与收口（2026-10-03，Asia/Shanghai）

- **用户接受范围**：本次会话明确接受仅 `output/playwright/s2.1-due-mobile.png`、`output/playwright/s3.1-tenant-mobile.png` 两张历史原图目前无法恢复，不再以找到原始副本阻塞 S3.2d2 本地功能交付。原图未恢复，原始字节及历史视觉结果仍无法凭现存图片复核；不豁免其他源码、patch、日志、截图或验收要求，也不授权今后覆盖证据。上文原部分完成、覆盖经过、查找结果和影响说明，以及 [原运行记录](../output/s3.2d2-runtime.log) 全部保留；其中“恢复旧截图仍需会话外原始副本”保留为事实，不再作为本地收口条件。
- **有限影响核对**：核对原运行记录、起点记录、完整指纹、源码基线 ZIP、相关脚本输出路径及现存回归日志，未发现上述两张以外新增的历史证据覆盖影响；该结论限于现有可核验记录，不构成其他证据缺失的豁免。基线 ZIP 指纹与起点记录一致，338 个原文件无缺失，变化仅落在原登记的 S3.2d2 文件及本台账；ZIP 未收录上述历史图片。本次未重复搜索旧副本、未生成图片，未删除或重命名任何证据。

以下两张现存文件登记为 **S3.2d2 当前版本回归证据**。已核对文件存在、PNG 完整性、尺寸、视觉内容及 SHA-256，并确认分别与当前旧同名文件逐字节相同；这个相同关系仅说明当前副本对应，不证明历史原图恢复。指纹为本次核验的现存字节，不是补造的旧指纹；不改文件时间。

| 当前回归图片 | 内容、尺寸与对应脚本／日志 | 本次核验 SHA-256 |
| --- | --- | --- |
| [s3.2d2-regression-s2.1-due-mobile.png](../output/playwright/s3.2d2-regression-s2.1-due-mobile.png) | 到期后的“执行已批准计划”弹窗、手动执行提示与执行按钮；375×821，视口 375×812、fullPage 截图，61,557 bytes；[schedule-browser.mjs](../web/tests/schedule-browser.mjs) 截图点，对应 [组件回归日志](../output/s3.2d2-component-regression.log) 的 schedule-browser 两项 PASS | `04ae656ca92bcb1c212e4f625a407de04a7d7096e58eec1fccec38fa6d39bb5f` |
| [s3.2d2-regression-s3.1-tenant-mobile.png](../output/playwright/s3.2d2-regression-s3.1-tenant-mobile.png) | “编辑租户名称”弹窗、只读 tenant-one、长名称与版本 8；375×812，61,004 bytes；[tenant-browser.mjs](../web/tests/tenant-browser.mjs) 截图点，对应 [组件回归日志](../output/s3.2d2-component-regression.log) 的 tenant-browser 通过记录 | `4899d8b7fa548d1a8f1190cc5772c093936891e637a819479229c51f78f7c854` |

- **日志与原退出条件复核**：组件回归日志 SHA-256 `073491e3dcc1ce4aa182122e0978dd654785a1721c6eaee2cda8837df9a4817c`；上述两图对应真实组件／内存 API 回归，不混同真实双会话或生产验收。现有 web 日志为 37/37、typecheck、build 通过，lint 为 0 error、1 条既有 `react-hooks/exhaustive-deps` warning（AccessControl.tsx:53），保留且未修改代码。Go JSON 日志实核 673 个测试 pass、4 包 pass、0 fail；3 个默认跳过的浏览器测试均由 [三链日志](../output/s3.2d2-regression-browser.log) 补齐。八组组件回归、迟到通知红绿证据、既有独立复核记录及 patch 重建通过记录均保留，当前 17 文件及原 patch 指纹匹配上文。除本次接受的两图例外外，未发现新的功能问题或必要验证缺口；默认 Python 3.9、卸载仅静态核验及上文其他未覆盖边界仍保留，不扩展验收结论。本次只修正文档／证据引用，未重跑完整应用测试或浏览器。
- **工作区与台账保护**：本次前后核对仅本唯一台账变化；S3.2d2 patch、17 文件集合、S3.2d1a patch／5 文件集合、已有各阶段 patch、原暂存差异及 HEAD 保持。`scripts/local/` 和既有源码、日志、截图均保持；40 个稳定 ID 与其他验收行、全局待办和历史记录保留。E02、OPS-07-02 仅同步本地阶段进展，OPS-07-02 整体及生产验收仍部分完成。
- **后续证据保护交接**：后续浏览器运行前先核对工作目录及所有输出路径，包含截图、日志和脚本固定的相对路径；使用该阶段独立的隔离输出目录，并在运行前后检查受保护证据指纹。上文浏览器命令仅为历史执行记录，不直接执行会覆盖历史文件的默认命令。本次不改 AGENTS.md、全局规则或共享测试脚本，不扩展全仓治理；固定截图路径仍为既有治理候选，未新增治理规则。未连接生产、安装依赖、提交、推送、发布或部署，未进入绑定管理，未向其他对话发送消息。

## S3.3a1：既有绑定合同与可信详情兼容方案（2026-10-03，Asia/Shanghai）

阶段状态：本地合同与方案准备 `completed`；关联 **OPS-07-03 / E02 / 原 S2.4**。本节是下一阶段建议，不是新详情、既有绑定编辑／撤销网页或生产验收已实现。OPS-07-02／OPS-07-03 整体仍部分完成；40 个稳定 ID、全部原待办、S3.2d2 两张历史截图的有限接受例外及其他边界不变。任务仅本地诊断、隔离验证和本台账追加；未调用完整界面开发流程。

### 绑定真实合同（S＝静态核对，D＝本阶段动态刻画）

以下位置均相对服务目录；模型完整字段见 [RoleBinding](../internal/model/control.go) L69–85、请求 L117–130。D 的来源为 [隔离测试源码](../output/s3.3a1/binding_contract_test.go) 和 [通过日志](../output/s3.3a1/contract-tests-final.log)，不把刻画现状视为认可全部现状。

| 合同 | 输入 → 规范化 → 存储 → 授权消费／结果 | 来源与证据等级 |
| --- | --- | --- |
| ID、创建／编辑 | `bindings` 同一入口；ID 仅 trim，精确同 ID 整体替换，不存在则新增；大小写不同可并存。重复 additions 最后一项决定快照／可变表字段，表行创建元数据仍取首次插入。空白 ID 可创建／批准提案，最终 Store 拒绝，策略不写。无独立“必须存在才能编辑”检查 | [access.go](../internal/runner/access.go) `updateAccess` L187–199、`mergeBindings` L320；[store/access.go](../internal/store/access.go) `upsertRoleBinding` L274；S+D |
| 撤销、顺序、重放 | `removeBindingIds` 经 lower+trim、去空去重排序；精确删除规范化 ID。先验证 additions，再 merge，最后 remove；无效 addition 即使同时删除也先失败。合法同 ID 新增／编辑再撤销，最终不存在。不存在／重复撤销可成功，新的获批请求仍新增版本、收据／审计；同 applied 提案原执行人重放返回原结果，不再写。**大写 ID 的撤销实际指向小写 ID**：大写原项可残留，小写同名项可被删除 | access.go L195、L210–254、`normalizeIDs` L409；[access_changes.go](../internal/runner/access_changes.go) L80–131；[access_policy.go](../internal/store/access_policy.go) L240–254、L300–313；S+D |
| 主体、租户 | `subject` lower+trim；含 `@` 则对规范化文本 SHA-256，保存完整 64 位小写十六进制，无 `sha256:` 前缀。这里不是邮箱语法验证。绑定只要求哈希合法、租户 active 或 `*`、角色存在；不要求主体登记／启用／同租户，同 ID 可改主体和租户。Runner 请求先验证当前主体存在、状态／主体期限／JIT，再校验主体租户；绑定本身不能让未登记主体登录或绕过主体门控 | access.go `canonicalAccessSubject` L401、`validateBinding` L299；[config/policy.go](../internal/config/policy.go) L223–233；[rbac.go](../internal/runner/rbac.go) L70–107、L133–161；S+D：邮箱哈希、未登记主体 Store 可匹配而 Runner 拒绝、主体／租户替换 |
| 角色、存量 | 引用内置角色本身允许，不等于修改内置角色。校验角色存在，不重验被引用角色的权限；S3.2c 只检查本请求 `roles`，不能据此宣称旧异常角色已清退。最终 proposed 全部绑定仍须角色存在及租户有效，过期项也不豁免；悬空旧记录可能阻塞无关应用。授权按角色权限精确匹配或 `*`，Store 缺角色视为空权限 | access.go L108、L234–250；[access_role.go](../internal/runner/access_role.go) L26；model/control.go L188；store/access.go L447–485；S（不扩大成全策略安全扫描） |
| 对象范围 | omitted/null/`[]` 都是 len=0，快照 `omitempty` 省略；表中 null 与 [] 可分别保存，但均匹配任意对象。`["*"]` 同样广匹配；重复项、顺序、未知对象、对象字符串空白／大小写不自动整理，非空普通列表精确匹配。写入不校验对象存在／归属；已知服务对象在 Runner 另做主体租户隔离。`authorizePlatform(access 等)` 也消费绑定，空范围与 `*` 不应只写“全租户业务对象”：所引用角色若含相应平台权限，也可匹配平台资源；tenant=`*` 仍不直接取消已知服务的主体租户门控 | access.go L187–199、L299–317；rbac.go L84–107、L147–159、`bindingMatches` L327、`objectTenantForPolicy` L354；store/access.go L469–485；S+D：四种广范围、重复未知对象保存、平台 access 匹配与未知精确对象。没有据此断言外部跨租户漏洞 |
| 期限 | omitted/null 都为 nil，完整替换会清除旧期限，表示不设绑定到期时间，绝非撤销；过期时间可写，记录保留，比较 `now >= expiresAt` 即失效。Go time 按 RFC3339 解析；绑定应用不强制 UTC，快照可保留输入偏移，表时间写 UTC RFC3339Nano；比较绝对时刻。只提交 ID+期限不构成局部更新，还缺主体／租户／角色，并会失去其他遗漏字段 | access.go L187–199、L299；store/access.go L285–302、L463–466；[store.go](../internal/store/store.go) `timeText` L224；rbac.go L339；S+D：+08:00 入表 UTC、过期保存但拒绝、nil 恢复不限期；null 解码同 nil 为 S |
| JIT | 绑定 jit 缺省 false，原样存；普通主体的绑定权限匹配不要求 jit。仅当主体 jit=true，`principalUsable` 要求同主体、同租户或 `*`、未过期的 jit 绑定；此门控不检查对象、角色、绑定审批状态，也不要求必有 expiresAt。门控通过后主体直接角色／其他绑定继续参与；主体自身 disabled／suspended／到期始终先阻断。不能把 JIT 绑定仅视为“这个对象临时权限” | rbac.go `principalUsable` L260–282、L310–343；store/access.go `Authorize` L441；S+D：无期限、不同对象 JIT 绑定可开启主体直接角色；主体过期仍拒绝 |
| 两层审批 | 外层 AccessChange 的审批策略、批准人／时间由服务端流程维护，新提案 two_party_v1＝独立批准人批准、创建人应用；不是绑定字段自动签发。绑定 `requiresDualApproval`、`approvalState`、`approvedByHash`、`secondApprovedByHash`、`approvedAt`、`secondApprovedAt` 均可由请求输入，保存但不参与上述授权；外层批准不会回填它们。完整替换遗漏将清零／清空，不能从 TS 类型或表单没传推出“后端禁止输入” | access_changes.go L23–62、L65–131；[store/access_changes.go](../internal/store/access_changes.go) 审批实现；store/access.go L289–302；rbac.go L260–343；S+D：pending/rejected 和任意绑定批准标识保留，仍可授权 |
| 创建／更新时间 | Runner 每次把绑定 `createdBy` 改为本次执行 actor；`createdAt`／`updatedAt` 保留输入（省略是零值），写入策略快照。绑定表首次插入 createdAt 零值才用 Store now；冲突更新不改原 createdBy／createdAt；**没有绑定 updatedAt 持久化列，读回为零**。因此快照元数据与表行元数据并不等价，详情不能承诺“全部存储字段与 after 完全相同” | access.go L194、L260–279；store/access.go L282–302、L384–431；S+D：createdAt／updatedAt 双层差异；跨执行人 createdBy 差异为 S |
| 保护、版本、原子性 | Store 对现存 `created_by=bootstrap` 绑定覆盖／撤销在事务中拒绝；只看客户端 createdBy 不足。最后管理员保护：Runner `hasPlatformAdmin` 检查 usable 主体的 `*` 角色，绑定分支只看未过期+角色有 `*`，不核对目标主体可用性或对象范围；Store `ensurePolicyHasPlatformAdmin` 更简化，主体分支只看角色。存在保护不等于证明总有真实可用管理员。expectedVersion>0 用请求值，否则应用读取当前版本；最终 Store 比较并原子写行／快照／收据／审批／审计 | access.go L252–275、L427–456；access_policy.go L66–148、L201–206、L228–254、L383–443；S+D：bootstrap 拒绝用自行 seed 的临时表行；版本／原子／摘要复用定向旧测试。最后管理员有效性局限仅 S，待专项，未修改保护 |
| 撤权及读权限 | 每次 Runner 从 SQLite 最新快照读取，必要时查当前绑定表；无权限会话无需重登录即可在下一请求拒绝。仍须考虑其他绑定、主体直接角色、角色／租户通配及 JIT 门控；只撤一条不保证完全失权。`AccessControl` 在平台 `ops.read(access)` 后返回全部 principals/principalList（可能含邮箱）、bindings 等；canManage 只是操作能力，不裁剪响应。详情权限更严：`access.manage(access)` 入口和返回前检查，no-store。现有列表掩码只影响 DOM 展示 | rbac.go L18–35、L51–161；access.go L16–64；[access_change_detail.go](../internal/runner/access_change_detail.go) L40–58、L176–195；[web/server.go](../internal/web/server.go) L144–197 每次从 Authenticate 得到 subject 再代理；S+D：保留同一合成 actor header 的 HTTP 200→撤销→403；其他绑定、主体直接角色仍有效。未新跑 Cookie/身份提供者／在途任务撤销，不承诺注销会话、停止任务或完整即时撤权 |

### S3.3a2 推荐唯一目标与支持边界（尚未实施／批准）

目标：**单绑定可信详情及既有新增消费者同步兼容**。详情建议同时具备普通单绑定 create/edit/revoke 表达能力，以支撑后续编辑／撤销表单；本阶段和建议的 a2 均不顺带实现这两个表单。请求仍用原 `bindings:[完整记录]`／`removeBindingIds:[id]`、正 expectedVersion、UUID 幂等键和外层审批，不修改登录、审批人数／执行身份、持久化、事务或授权解释。

1. **可信来源与 after**：在现有详情的摘要、精确 JSON、幂等键检查后分派；before 仅取 `GetAccessPolicySnapshotVersion(expectedVersion)` 已有不可变快照，核验摘要和精确编码，缺失不补造。after 复用／等价验证 `updateAccess` 的规范化，按同 ID 完整替换而非覆盖非空字段；撤销 after 明确不存在。当前版本必须等于基准，提案状态限 pending/approved，新 two_party_v1 合同一致；非 ready 不泄露局部投影。快照元数据不是表行历史元数据：不以当前 `ListRoleBindings` 拼历史 before。若需核实 bootstrap／来源和快照表行一致性，可在同版检查中只读查目标行并作保守支持门控，**不返回该行原文，也不推导它是历史版本**；跨读取版本变化立即 stale。表行缺失／保护／来源不明／授权相关字段不一致均不 ready，不修改或修复它们。
2. **最小标识与隐私**：独立批准人必须能核对目标绑定 ID+完整规范化 subject 哈希+租户 ID。绑定 ID 标识记录而非人；完整哈希能对照另一路可信提供的邮箱规范化哈希／主体标识，但不能自动证明真实姓名；掩码只能辅助展示，不能单独排除混淆。建议详情白名单新增目标 `subject`（64位哈希），默认不返回明文邮箱、整个主体、其他绑定或完整策略。完整哈希是可关联／可字典猜测的假名标识，不是匿名信息；虽同权限用户当前能读更宽 AccessControl，新详情的数据扩展仍需在 a2 范围中明确确认。维持 `access.manage(access)`、双次当前权限检查及 no-store；前端展开／复制完整哈希不能替代后端字段裁剪。需要邮箱、目录搜索、主体状态对象或额外身份解析时另行定界确认。
3. **必须显示的差异**：operation、绑定 ID、subject、tenant ID、角色 ID及基准版本对应角色完整权限（明确 `*`，不能把角色名称当权限）、对象原列表（含顺序／重复）及“任意对象／可能匹配平台资源”标识；期限显示明确“不设绑定到期时间”或带时区绝对时刻；jit；绑定自身全部六审批字段的有效值／清空变化，与外层审批分栏。可用明确的“默认 false／空状态／无批准人和时间”摘要压缩全默认字段，但不能隐藏非默认值。after 仅声称授权相关字段与执行结果对应；创建元数据不列为可编辑字段，明确“快照 createdBy 为执行人、表行保留首次创建信息；updatedAt 不写绑定表”。不展示全策略和其他绑定，不宣称有效用户数或撤销后主体必然失权。
4. **推荐开放／暂缓**：普通绑定固定 ID／subject／tenant，edit 只开放 role、objectIds、必要 expiresAt；先支持非 JIT、绑定六审批字段全默认、规范化且无大小写别名的记录。create 保持现有普通新增字段，明确空范围／`*` 和无期限；引用已知内置角色（包括既有平台管理员角色）与合规自定义角色可解释，但 `*` 权限必须醒目，不能因 S3.2c 禁止新增通配自定义角色而误禁引用既有内置角色。主体登记／状态不构成写入新约束：详情不要承诺绑定一保存就能登录；如要求仅选择已启用同租户主体，这是独立写入规则变更。JIT／旧审批元数据非默认绑定初期 edit/revoke 不 ready，原因需明确；后续选项是完整保留／展示原字段及 JIT 对主体直接角色影响，再开放，而非静默清零。主体或租户迁移、tenant=`*`、不能解释的角色权限／对象标识、过期历史异常、特殊元数据输入先 unsupported，保留最终需求，不能记成永久产品限制。可准确解释的普通过期绑定可展示／撤销，不自动清理；期限扩展和清除都必须显式审阅。
5. **拒绝歧义**：单 additions=1 且 removals=0，或单 removal=1 且 additions=0；任一租户／角色／主体／enforced 混改、重复或多目标、同 ID 加删、不存在撤销、空 ID、大小写别名、bootstrap／来源不明、未知或不能完整解释的历史字段、缺基准／损坏／stale 都显式 unsupported/unavailable/stale。支持规则只控制可信详情／网页操作，**不自动收紧原写 API**；直接 API 仍按既有授权／审批运行，若要让后端 approve/apply 强制要求 ready，应作为新增授权／审批边界专项批准，不伪称前端校验已提供该保证。

### 既有新增链必须一起接入

已沿真实调用核对：[AccessControl.tsx](../web/src/views/AccessControl.tsx) L94–105（`binding-${Date.now()}`，主体 trim，对象按空白／逗号切分，提交后清空）→ [App.tsx](../web/src/App.tsx) L1486–1539 → [api.ts](../web/src/api.ts) L918–943（补 UUID，强制外层审批）→ Runner CreateAccessChange → detail → [reviewAllowsApproval](../web/src/accessChangeReview.ts) L4–19 → [审阅组件](../web/src/components/AccessChangeReview.tsx) L25–39、L82–86 → [applyReviewedAccessChange](../web/src/accessChangeApply.ts) L25–53。当前详情 L73–75 对不含租户／角色改动的绑定、绑定混合主体／enforced 请求归 other；前端 `other+unsupported` 在匹配 ID／摘要／状态后放行，不检查完整绑定差异／版本。页面要求“沿原流程审阅完整内容”，但该组件未展示绑定 payload；不是可信绑定审阅。

[消费者刻画](../output/s3.3a1/consumer-contract.mjs)／[日志](../output/s3.3a1/consumer-contract.log) 使用当前 TS 实现动态证明：①仅后端改 `kind=binding`，旧前端批准判定 false、apply 不发写入，普通新增链被阻断；②只给 other 增加 binding 字段，当前批准／apply 仍放行而未核验投影。故 a2 必须同批修改 kind/类型、严格操作与字段校验、最小审阅展示、应用前详情重读／同身份同摘要同版本检查及读回目标结果；已有新增必须走完整新链，不能以暂留 other 绕过。带任意 binding/removeBindingIds 的请求须在 other 早返回之前识别，混合／未知请求失败闭合；前端也拒绝其他 kind 携 binding 投影和缺字段／未知操作。其他已支持租户／角色／删除及无绑定的既有 other 是否进一步收紧，留原边界，不顺手扩大。

登记现状不足，不修复：时间戳 ID 无碰撞保护（发生同 ID 会替换而非新增）；空范围和 `*` 的平台匹配含义不充分，列表把 `["*"]` 显示为“1 个对象”；详情 other 无完整差异仍可批准应用；表单仅存在性校验，不校验主体登记／对象归属，也未提供期限、JIT／旧审批编辑。只读发现不证明存在未授权利用链。

### 验证、后续授权与证据保护

- 本次实际运行：macOS Darwin 25.6.0 arm64、Go1.22.12；`GOTOOLCHAIN=local GOPROXY=off go test -overlay=output/s3.3a1/overlay.json -ldflags=-linkmode=external ./internal/runner -run '^TestS33Binding' -count=1 -v`，3/3 通过（0.318s）。overlay 只将本次临时测试映射到未执行的 scheduled_plan_test.go 路径，原文件字节不变；日志中的该路径指向 [本次刻画源码](../output/s3.3a1/binding_contract_test.go)，不是修改了计划测试。真实回环 HTTP→Runner→临时 SQLite，创建／独立批准／创建人应用；bootstrap 个案在临时表 seed 合成保护行。没有外部账号或真实 Cookie 证据。首轮新增虚拟文件被 Go vet 路径限制阻断；次轮误在仓库根目录启动无模块，均未运行产品测试，失败日志保留，不计成功。
- 已有定向 [Go 日志](../output/s3.3a1/existing-targeted-tests.log)：Runner 2.969s、Store 0.283s 通过，覆盖 schema4 审批、混合角色原子性（含 S3.2c）、原始载荷摘要／实际执行身份撤权重放（S3.2d1a）、角色删除原摘要缺口回归、租户 IDOR、版本冲突、Store 应用事务回滚／收据恢复。完整执行表达式保存在 [验证记录](../output/s3.3a1/verification.log)；未重跑 Go 四包、构建或浏览器。`node --test tests/access-change-review.test.mjs` 原4项全过；另跑上述消费者刻画通过，不更改产品规则。未触及恢复测试，无 Python 全局／进程映射或依赖安装。
- **独立只读复核**：一个默认代理、`fork_turns=none`，仅复核模型／绑定写入授权／详情／新增消费者指定路径，不改文件、不再委派。复核确认完整替换、空范围平台匹配、主体与 JIT 门控、绑定审批不参与授权、other 兼容风险及当前表不能拼历史 before。指出 `web/src/types.ts` L1243 的 AccessBinding 缺 `approvedAt`、`secondApprovedAt`、`updatedAt`（对应 model/control.go L81、L82、L84）；主代理沿原文核对确认，a2 必须按后端全字段判定，尤其两个审批时间不得漏检。旧新增的默认字段／空对象数组必须可审阅，不要求客户端生成审计元数据。代理没有独立运行测试或读取日志，未独立检查哈希算法与服务具体操作入口；测试证据与方案取舍由主代理承担。
- **待决策与专项范围**：a2 开工需确认新详情 subject 哈希及角色权限白名单、普通字段／不支持历史范围、同步消费者范围；正常本地只读详情开发与授权解释修改严格区分。修复 ID 大小写撤销、改变完整替换／创建元数据、主体／对象写入校验、绑定审批真实性或 JIT 门控、最后管理员有效性保护、后端强制可信审阅均会改变权限／持久化或审批合同，需分别说明影响、验证与回滚后专项确认；本阶段未获得这些实施授权。存量清退、默认 Python、租户停用／删除、生产部署等原待办不动。
- **a2 最小入口与验收**：runner/access_change_detail.go＋单绑定投影辅助与定向测试，必要的等价规范化复用点 access.go；store/access_change_detail.go 仅在需要目标保护查询时扩展；web/src/types.ts、accessChangeReview.ts、components/AccessChangeReview.tsx、accessChangeApply.ts、views/AccessControl.tsx（仅既有新增提示／兼容）及相关测试，App.tsx/api.ts 原链按需核验。HTTP／SQLite 对照历史 before、应用规范化 after、三个操作、元数据层次、权限裁剪／摘要／版本／保护／混合拒绝；前端畸形／降级 kind／迟到身份／断网读回测试；既有新增真实双账号完整审阅→批准→应用→刷新，租户／角色删除消费者回归及独立只读复核。编辑／撤销网页与生产验收留后续阶段，不以详情 ready 代替最终应用成功。
- **工作区**：起点 main / `b4ec0f9268afa6f1b23259204c3544b798ad47f3`；原27项暂存 SHA-256 `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155`、S3.2d2 patch `5b8be7037ea8af1fa1860feca99442a1a83fe15800ab10e893ec40830ee4de74`、17文件集合 `ae2e8e0ab6d192846097368eba87e71f9a1336c4e8c529d1ff68aa3782969c96` 起点复算匹配。Git 已跟踪及未忽略源码、scripts/local 的35个未忽略文件、全部既有 output 文件纳入 [起点哈希](../output/s3.3a1/baseline.json)，收尾 [保护检查](../output/s3.3a1/verification.log) 核对原876项中仅本台账追加、全部其他原文件和暂存差异不变；新证据仅 output/s3.3a1，无历史文件覆盖。补查发现 scripts/local/clash_direct/.state 下6个忽略状态文件未在起点清单中，未操作这些文件，但不声称有其起止字节证据；其余35个 local 文件指纹一致。唯一维护文档为本节，不新增第二份报告；日志／测试源码保留用于核验。HTTP 服务与临时 SQLite 随测试 cleanup 关闭／删除，未留下长驻服务。
- **治理候选**：记录本节 ID 大小写、绑定元数据双层差异、空对象范围及 JIT 解释偏差风险；未自动改 gotchas／规则，未发现本次必改的过时引用。未改产品源码、AGENTS.md、全局配置，未 stash/reset/checkout/重放 patch、提交、推送、连接生产、读取生产数据、发布、部署或向其他对话发消息。S3.3a1 完成后停止。

## S3.3a1a：绑定撤销目标保护（2026-10-03，Asia/Shanghai）

阶段状态：本地 `completed`，安全修复结果 `fixed`。关联 **OPS-07-03 / E02 / 原 S2.4**；仅完成已批准的大小写误撤销保护。OPS-07-03 整体仍部分完成，40 个稳定验收 ID 及全部原验收行不变。上文 S3.3a1 的历史行为、复现源码和日志保留，不将其原缺口刻画通过解释为现行保护通过。

- **批准范围与实现**：用户明确批准本阶段本地实施、隔离验证、复核与交接。[access.go](../internal/runner/access.go) 新增 `validateBindingRemovalIDs`，逐项检查全部原始 `request.RemoveBindingIDs`：先 TrimSpace，非空且 ToLower 会改变字符串则拒绝整个请求。最终 `updateAccess` 在构造 proposed／规范化／写入前调用；[access_changes.go](../internal/runner/access_changes.go) 在创建序列化／摘要／保存前调用。共用 `normalizeIDs`、绑定新增／编辑、角色／租户、Store、授权与事务未改；不查询库存猜目标、不扫描或迁移大写绑定、不将历史批准重新解释为保留大小写的精确删除。
- **入口与错误**：POST `/v1/access/changes`、schema4 PUT `/v1/access`（强制提案）、历史 POST `.../{id}/apply`、schema3 PUT 直接写及已有 `Engine.UpdateAccess` 都受保护。沿用 HTTP 409、单一 `error` JSON，文案为“绑定撤销 ID 不支持大小写规范化后改变目标”，不带目标或其他绑定信息；解码 400、身份 401、授权 403 保持。Web 原 `/api/access` 代理和请求消费者未改。合法小写仍 trim／去空／去重／排序、精确撤销；普通无匹配／空值行为保持。
- **已接受兼容影响**：旧 pending 可按原合同批准，异常 approved 最终应用拒绝，原始载荷、合法摘要、批准记录和原状态保留；重新创建及旧直接写 receipt 重放含异常目标也拒绝。已成功 applied 仍先按原执行身份返回原幂等结果，不重新解析目标，错误身份仍拒绝。S3.2c 权限校验、S3.2d1a 原始载荷摘要及 applied／并发成功恢复分支字节保持。
- **产品回归与原子性**：新增 [撤销保护测试](../internal/runner/access_binding_removal_test.go) 和 [兼容测试](../internal/runner/access_binding_removal_compatibility_test.go)，10 个顶层专项、114 个含子测试通过事件。先在旧源码运行，[红灯](../output/s3.3a1a/red.log) 实际 err=nil、仅剩 Mixed，证明 mixed 被误删；[创建红灯](../output/s3.3a1a/red-create.log) 为 202 而预期409。修复后相同命令 [误撤销绿灯](../output/s3.3a1a/green-red-replay.log)／[创建绿灯](../output/s3.3a1a/green-create.log) 通过。覆盖两项并存／仅大写／仅小写／均不存在、Unicode 大小写及空白、首尾非法项、合法非法重复、多目标、JSON 转义和字段大小写、合法新增／编辑／撤销混合、历史 pending／approved 的有效原始摘要及审批保留。拒绝基线在夹具构造和批准之后，比较全部 SQLite 表内容，绑定、版本、快照、receipt、提案及成功审计均无部分变化；此授权成功后的校验分支无新增失败审计，其他授权失败审计仍依原合同。合法小写仅删除小写项、历史 applied 和直接重放差异、版本冲突、bootstrap、最后管理员、角色／租户规范化和完整替换元数据均有回归；不宣称修复最后管理员已知语义局限。
- **验证环境与命令**：Darwin arm64、Go1.22.12、Node25.1.0、既有 Python3.12.12，[环境](../output/s3.3a1a/runtime.log)／[实际命令与退出码](../output/s3.3a1a/commands.jsonl)。最终 `GOTOOLCHAIN=local GOPROXY=off go test -ldflags=-linkmode=external ./internal/runner -run '^TestBindingRemoval' -count=1 -v` 通过10.299s，[定向日志](../output/s3.3a1a/focused-final.log)。随后指定四包加 `-json -count=1` 全部实跑：[日志](../output/s3.3a1a/go-tests.jsonl)，runner63.637s、store8.435s、model0.009s、web0.158s，共787个含子测试通过事件；S3.2c／S3.2d1a及审批／事务旧回归保持。Python仅通过临时 python3 映射进入测试子进程 PATH；未改全局环境或范围外恢复脚本。`node --test web/tests/api.test.mjs web/tests/access-change-review.test.mjs` 14/14通过，[日志](../output/s3.3a1a/web-targeted.log)。前端未改，三个按需浏览器测试按原开关跳过；未重跑全站浏览器／前端构建，不属于本后端专项验收。
- **失败记录与复核**：首轮扩展测试因夹具漏设 Content-Type、直接响应无序主体数组的字节比较和 Store 内部字段与 HTTP 字段差异失败，[日志保留](../output/s3.3a1a/focused.log)；修正夹具／合同断言后全表不变、applied 原结果断言仍保留，最终定向及四包通过。安全修复技能要求的修复前只读边界调查及修复后一个新的默认只读子代理（均 fork_turns=none）完成，后者独立核对真实入口、历史摘要、拒绝原子性、兼容与四文件 patch／原字节／已有日志，未发现具体绕过或回归；未改文件、未派生代理、未独立重跑测试。最终台账／保全检查由主代理负责。
- **工作区与证据**：起点 main / `b4ec0f9268afa6f1b23259204c3544b798ad47f3`，[起点记录](../output/s3.3a1a/start.json)、[893文件基线](../output/s3.3a1a/baseline.json)、[原字节ZIP](../output/s3.3a1a/baseline.zip)。本轮改两处实施文件、新增两个产品测试，再追加本台账；未重放旧 patch、stash/reset/checkout 或恢复旧工作区。[最终保护检查](../output/s3.3a1a/verification.json) 核对原27项暂存 SHA-256 `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155`、S3.2d2 patch和17文件集合保持；scripts/local 全部41个起点文件（含6个忽略状态文件）及历史 output 源码、日志、patch、截图不变。此证据仅证明本轮起止，不补造此前缺失的起点字节。
- **本轮增量与回退**：[s3.3a1a-local.patch](../output/s3.3a1a/s3.3a1a-local.patch) SHA-256 `f852c6e263e8ce63faabe1e81970e27c9c7f782b083b16225fbab67b14270581`；4文件集合 SHA-256 `6fc580eab63e58d16eba71be352d379a4ab8d81ce82926c35853a09ef8c6449c`，按服务相对路径排序，以 `UTF-8路径 + NUL + 原始bytes + NUL` 计算。patch相对本轮起点，排除前阶段差异、台账自引用及输出证据；[隔离重建](../output/s3.3a1a/integrity.json) `git apply --check/apply` 后4/4字节一致，反向检查／应用恢复原字节和新增文件不存在状态。首次补丁缺新增文件模式元数据、隔离检查拒绝，已保留 [失败记录](../output/s3.3a1a/rebuild-rejected.log) 和失败候选；修正后上述重建通过，未在工作区应用。回退仅逐块撤本轮增量，无数据迁移；回退会重新暴露误撤销风险，不能称保护仍有效。

**后续边界**：已修复异常大小写撤销误操作另一条绑定；存量大写 ID 的完整撤销支持及 ID 合同统一仍未完成。单绑定可信详情、编辑／撤销网页均未实施；新增时间戳 ID 碰撞、对象／期限／JIT／双层审批与元数据合同及生产验收待办保留。S3.3a2 必须承接本保护，并将可信绑定详情和既有新增消费者同批接入，不能仅改 kind 或保留 other 绕过完整审阅；其字段／隐私／支持范围仍按上文单独定界。治理收尾仅保留既有候选，无新增规则或必改过时引用，未改治理文件。

本地临时 SQLite、回环 HTTP、Python映射与隔离重建目录随测试／脚本退出释放；证据仅存 output/s3.3a1a，不是发布制品。未连接生产、读取生产数据、安装依赖、改全局配置、提交、推送、发布、部署或向其他对话发送消息。本阶段完成即停止，不进入 S3.3a2。

## S3.3a2：单绑定可信详情与既有新增接入（2026-10-03，Asia/Shanghai）

阶段状态：授权范围内本地 `completed`；关联 **OPS-07-03 / E02 / 原 S2.4**。OPS-07-03 整体和生产验收仍部分完成；40 个稳定 ID、原验收行与全部历史记录保留。本轮未建设既有绑定编辑／撤销表单。

- **响应合同与来源**：现有 detail 增加 `kind=binding`、`operation=create/edit/revoke`；`binding` 精确包含 `before`、`after`、`changedFields`，不存在侧显式 null。值白名单为 id、完整64位 subject 哈希、tenantId、roleId、基准版本完整 permissions、原 objectIds（顺序／重复保留，空规范为[]）、UTC RFC3339Nano expiresAt（无期限为null）、jit=false、bindingApproval=default。default 明确涵盖绑定自身六审批字段 false／空／无时间，与外层 two_party_v1 分开解释。before 仅来自 expectedVersion 已有快照，核验原载荷摘要、精确编码、幂等键、快照摘要／编码；after 共用原应用规范化，完整替换，不合并非空字段。撤销不伪造空记录。
- **数据授权与裁剪**：完整哈希为可关联假名标识，不是匿名信息。只返回本次目标和 before／after 涉及角色的权限；不返回邮箱、完整主体、其他绑定、完整策略、原提案或创建者／批准者额外身份。入口／返回前均保持 access.manage(access) 当前授权及 no-store；AccessControl 列表权限与裁剪未改。非ready不返回任何绑定值或权限，公共提案标识／状态／版本／摘要／reviewerHash保留。
- **支持与失败边界**：支持纯单普通绑定新增、固定 ID／subject／tenant 的角色／范围／期限完整替换及既有撤销，包括可准确解释的过期绑定、未登记精确对象、既有内置角色的 `*` 权限。当前表仅查询目标及大小写／空白别名，核查来源、bootstrap、授权字段一致性；不作为历史before，不比较已知不同的创建元数据。两次版本读取包围目标查询，跨版本为stale，不宣称原子。JIT、六审批字段非默认、特殊元数据、主体／租户迁移、通配租户、混合／多目标／重复／不存在撤销、歧义、来源或基准异常均不支持详情。此为详情／网页支持边界，未新增后端 approve/apply 强制ready规则。
- **网页兼容**：含绑定请求优先进入binding分派，混合／损坏／不支持不能借other批准；无绑定other原行为保留。严格校验操作、完整投影、相互排斥分支、引用角色权限、摘要、状态、版本和身份，批准前和公共应用前重读。现有新增表单可用，明确提案未生效及服务端真实create/edit；空范围／`*`提示广泛匹配并可能含平台资源。展示完整哈希、权限、期限清空和对象放宽。类型补齐approvedAt、secondApprovedAt、updatedAt及实际appliedPolicyVersion。读回核对目标授权字段或撤销后不存在，精确纳秒／时区比较；空列表省略沿模型omitempty解释；版本继续变化或读回不足提示“已应用，当前结果待核对”。响应丢失仅只读恢复，applied重放不依赖before、不自动重写。保留S3.2d2代次与迟到保护。
- **实际验证**：见 [验证索引](../output/s3.3a2/verification.json)／[环境](../output/s3.3a2/runtime.json)。最终web 41/41，typecheck、build通过，lint 0错误／1条原有identityGeneration警告。指定Go四包使用macOS external链接，826个含子测试通过事件；runner68.277s、store8.485s、model0.008s、web0.185s，见 [Go日志](../output/s3.3a2/go-tests-final.jsonl)。既有Python3.12仅临时映射测试子进程PATH，无安装或全局改动。覆盖普通三操作、原范围顺序／重复、期限偏移／清除、六字段、来源／保护／表行不一致、摘要／编码／幂等／版本、重复GET全持久化表内容不变、审批重放与原S3.3a1a／S3.2c／S3.2d1a保护。
- **真实浏览器与复核**：[本轮双账号日志](../output/s3.3a2/browser-eighth.log)、[结果与截图](../output/s3.3a2/browser/results.json)：两独立Cookie经合成Web认证、真实CSRF／代理／Runner／临时SQLite完成现有新增→独立完整审阅→批准→创建人应用→读回，证明创建／批准尚未生效；HTTP编辑／期限范围清除／撤销进入实际组件通过，JIT提案网页阻断。中文当前样式、1280／375px、系统浅／深偏好、长哈希、权限、键盘焦点及错误态已检查；没有新增独立深色主题。五项组件故障检查通过，含绑定身份／租户／提案切换、迟到读取、失败清除、other拒绝和批准前漂移。三个既有真实浏览器（租户／角色／角色删除）在隔离脚本及输出路径通过，见 [回归日志](../output/s3.3a2/legacy-browser.log)。[独立只读复核](../output/s3.3a2/review.txt)发现纳秒读回P2，已修正；复核代理独立4/4测试并复算patch通过，无新增阻断问题。实际并发读取交错未注入，GET／POST最终安全仍由原后端承担。
- **证据与工作区**：main / b4ec0f9268afa6f1b23259204c3544b798ad47f3；起点 [baseline.zip](../output/s3.3a2/baseline.zip)／[baseline.json](../output/s3.3a2/baseline.json) 保存字节与历史输出指纹。原27项暂存SHA `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155`、130个历史输出及scripts/local保持，无范围外基线文件变化。[增量patch](../output/s3.3a2/s3.3a2-local.patch) SHA `80e0054e82f320fd1e5e63aa0e81ef8d117420e70010e6ccc326b160fc191050`；18文件集合SHA `fe29125c05eba9e13bd428c3193ded47c0c3dd333904b9f78f19e7ccceb8f227`。口径按服务相对路径字典序 UTF-8路径+NUL+bytes+NUL；排除前阶段、台账自引用和输出证据。隔离git apply正向／反向18/18字节验证，见 [integrity.json](../output/s3.3a2/integrity.json)。中间失败日志保留，不计通过；旧截图路径经隔离映射，没有再次覆盖历史证据。
- **回退、待办与资源**：仅配套撤回本轮前后端增量，无迁移或存量改写，保留三项既有保护；回退后不能继续记为本能力通过。绑定编辑／撤销表单、大写ID完整支持、时间戳ID碰撞、JIT／特殊历史、最后管理员局限、主体／对象规则、存量清退、Python兼容和生产验收继续保留。治理候选仍为ID碰撞、历史元数据双层及旧截图固定路径；未自动改规则。自建Vite已停止，测试临时SQLite／HTTP／浏览器清理完毕，证据保留。未连接生产、读生产数据、提交、推送、安装、发布、部署或向其他对话发消息；本阶段结束。
