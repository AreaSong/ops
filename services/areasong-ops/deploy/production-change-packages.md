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
| E02 | [AccessControl.tsx](../web/src/views/AccessControl.tsx)（当前工作区源码）；[control.go](../internal/model/control.go) L117；[access_changes.go](../internal/runner/access_changes.go) L23；[access_changes_test.go](../internal/runner/access_changes_test.go) L21/L369；[tenant_idor_test.go](../internal/runner/tenant_idor_test.go) L14 | S3.1/S3.1a 新增租户／名称编辑、服务端只读差异与跨账号审批应用本地完成，证据见下文；S3.2a 角色只读差异本地完成；S3.2b 角色新增／编辑网页、真实双会话及独立复核本地 completed，见下文；S3.2c 登记权限创建／最终应用校验本地 completed，验证环境与兼容限制见下文；S3.2d1 删除合同／只读详情及 S3.2d1a 摘要专项本地 completed（历史部分完成记录保留）；S3.2d2 删除网页及双账号本地 completed，两张历史截图原件缺失已经用户接受为有限证据保全例外，见下文收口记录；S3.3a2/b/c 普通绑定新增／编辑／撤销已本地验证；特殊历史／JIT 等范围、整体权限及生产验收仍未完成；S3.4a 租户停用／删除合同核对见下文（非实现） |
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
| OPS-07-02 | 自定义角色创建/修改/移除形成可审阅变更；内置角色、引用约束不被绕过 | S3.2a/b/c/d1/d1a/d2 本地完成（d1 原摘要阻塞由 d1a 解除；d2 两张历史截图原件缺失按用户接受的有限例外收口），见下文 E02 | S3.2d1 HTTP/SQLite、S3.2d2 Go/前端、真实双会话及独立复核见下文；保留此前证据 | P0；未部署／生产未验收 | 部分完成 | apply 摘要专项及 S3.2d2 删除网页本地链已验证；普通绑定新增／编辑／撤销已本地验证（S3.3a2/b/c）；特殊范围、整体权限及生产验收仍未完成；存量角色／历史提案不清退 | S2.3（S3.2a→b→c→d1→d2）→S3.07→S5.07 |
| OPS-07-03 | 新增及编辑/撤销既有绑定均有对象范围、版本冲突、审批和生效反馈 | S3.3a2/b/c 普通绑定新增／编辑／撤销已本地验证 E02 | S3.3a2/b/c HTTP／SQLite、真实双会话与独立复核，见下文 | P0；未部署／生产未验收 | 部分完成 | 普通范围 expectedVersion、即时撤权及旧 Cookie 负向已本地验证；特殊历史／JIT 等范围、整体权限与生产验收未完成 | S2.4→S3.07→S5.07 |
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

## S3.3b：普通既有绑定编辑网页与双账号本地闭环（2026-10-03，Asia/Shanghai）

阶段状态：授权范围内本地 `completed`（实现、必要运行验证、独立只读复核与问题修正复查均通过）。关联 **OPS-07-03 / E02 / 原 S2.4**。不关闭 OPS-07-03 整体或生产验收；40 个稳定 ID 与全部历史记录保留。

- **本轮实现**：[BindingEditDialog.tsx](../web/src/components/BindingEditDialog.tsx)、[bindingChange.ts](../web/src/bindingChange.ts) 与 AccessControl 列表接线，从明确条目进入，冻结绑定 ID、完整主体哈希、租户、初值、版本与会话上下文。只提交单条绑定 id/subject/tenantId/roleId/objectIds/expiresAt、正版本、UUID 幂等键和外层双人批准标志。角色仅引用既有定义，内置 `*` 显示全部权限。未改任何后端生产实现、完整替换、授权、摘要、审批或事务语义。
- **支持边界**：规范化无别名歧义的普通绑定，具体有效租户、哈希来源、非 JIT、六审批字段默认（包括 approvedAt／secondApprovedAt），角色／对象／期限可准确解释。未知字段及已知不支持状态禁用并解释；表行 createdAt 非零不阻断，不将其当作历史快照事实。服务端可信详情仍是最终来源与支持范围依据，不创建探测性提案、不查询身份目录。
- **字段与精度**：对象采用 JSON 字符串数组，无损保留内部空白、逗号、顺序及重复项；未编辑不排序去重。`[]`／`*` 明示广泛匹配及可能包含平台资源，清空有显式按钮。期限默认保持原字符串；设置要求实际日期和 Z／固定偏移，秒至9位纳秒，沿公共精确算法比较。拒绝无效日期／无时区输入，不经本地时区或夏令时归一化；设置是主动替换。清除省略 expiresAt，含义为不设绑定到期时间；过去时间允许且提示过期。
- **生命周期与可信链**：提交前只读核对身份、管理资格、版本及原目标；消失、漂移或内容变化阻断，不换版本或转新增。无变化与取消零写入。相同身份／租户／目标／版本／载荷重试和关闭重开复用 sessionStorage 幂等键；失败留草稿，原提案 rejected 时本次不重建，下一次显式提交才换键。新响应须通过固定身份、非 null before／after 的真实 edit 校验；公共审阅／批准／应用仍沿 S3.3a2，只有 applied 且实际版本读回字段匹配才显示编辑生效。保留响应丢失只读恢复、身份／租户往返及卸载后的迟到失效保护。共享弹窗焦点选择器补齐 textarea。
- **运行验证**：[验证索引](../output/s3.3b/verification.json)。最终 web 48/48、lint 0错误／1条原有 identityGeneration 警告、typecheck/build通过。指定 Go 四包 external 链接实跑826个含子测试通过事件；[Go结果](../output/s3.3b/go-result.json)。Python3.12仅临时映射测试子进程，默认3.9兼容问题不修复。既有 S3.2c／S3.2d1a／S3.3a1a 保护均包含在回归中。
- **真实双账号**：[最终日志](../output/s3.3b/browser-review-fix.log)／[结果与原始字段](../output/s3.3b/browser/results.json)：独立 Cookie 经合成 Web认证、真实CSRF／Unix代理／Runner／临时SQLite，完成角色调整、范围收紧、显式放宽、清除期限、设置纳秒期限五项网页闭环；逐项证明创建／批准正式绑定未变、实际版本读回匹配、其他绑定／主体／租户／角色定义不变。受测主体无直接角色，仅目标绑定授权；同一 Cookie 的提案详情 GET 在角色收紧后由200变403，`/api/access`仍200，不宣称完全失权或撤销会话。取消、版本漂移、无权限和 JIT 禁用、1280／375px、浅／深偏好、完整哈希与期限、焦点循环及归还均验证。当前样式无独立深色主题。
- **回归与失败记录**：4条既有真实浏览器链（新增绑定、租户、角色、角色删除）通过；7组旧组件回归、最后焦点调整后6组相关脚本（含定时计划）通过。新故障组件覆盖目标消失、拒绝、身份／租户切换和往返、卸载迟到、重复提交、失败重试、关闭重开、撤销后重建及非edit拒绝。旧脚本均先检查固定输出路径并隔离。早期 fleet 503 夹具选择错误、采集器关闭异常／超时及工作目录错误保留为失败记录，不计通过。独立代理前两次遇速率限制，后续实际只读复核发现返回创建人未绑定冻结身份的P2；已在创建响应后立即检查 actorHash 并保留可能已保存提示。新增真实组件错位注入、五条真实链及web四项重新验证，代理定点复查确认问题消除、无新增发现，见 [复核记录](../output/s3.3b/review.txt)。Cookie网络期间A→B→A未实际重现，错位用组件内存API注入验证。
- **工作区与证据**：起点 main／`b4ec0f9268afa6f1b23259204c3544b798ad47f3`、原27项暂存 SHA `4a1385bd51f72178d8b777c7f221683c5e23112461199523bb5b846b20262155` 已保存于 [987文件基线](../output/s3.3b/baseline.json)／[原字节](../output/s3.3b/baseline.zip)。工作中观察到外部提交后 HEAD 为 `c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca`、暂存区为空；本任务未执行任何 Git 状态写入，未恢复旧状态。对起点逐字节检查，除本轮文件与本台账外无变化，234个历史输出／scripts/local受保护文件一致；详见 [外部Git变化](../output/s3.3b/external-git-change.json)。
- **增量回退**：[11文件patch](../output/s3.3b/s3.3b-local.patch) SHA `7c36ebe8bb84516fa084071450122edca27fb17039fc3b89732794a1598e3c43`；文件集合 SHA `c30989ad20a651daa36eede8c88532f8c7baed6685bd569caf9d885e87bed0da`。按服务相对路径排序，UTF-8路径+NUL+bytes+NUL；相对本轮起点，排除前阶段差异、台账自引用及输出。隔离正反向 apply 检查及11/11字节重建通过，见 [integrity.json](../output/s3.3b/integrity.json)。仅撤本轮前端／测试增量，保留既有后端保护；代码回退不恢复已应用数据，本轮只有可丢弃合成数据。
- **边界、资源与下一步**：自建Vite已停止，临时数据库、HTTP、浏览器、Python映射随测试清理；仅证据保留。未连接生产、读取生产数据、安装、修改全局配置／治理规则、提交、推送、发布、部署或向其他对话发消息。本阶段仅普通既有绑定编辑本地收口；撤销表单、大写ID支持／碰撞、JIT／特殊历史、最后管理员局限、主体／对象规则、租户停用／删除、存量清退、Python兼容和生产验收继续保留。治理收尾仅保留已有固定截图路径／元数据／ID候选，无新增规则或必改过时引用；不进入下一阶段。


## S3.3c：普通既有绑定撤销网页与双账号本地闭环（2026-10-03，Asia/Shanghai）

阶段状态：授权范围内本地 `completed`（实现、必要本地验证、独立只读复核及问题修正复查通过）。关联 **OPS-07-03 / E02 / 原 S2.4**。普通绑定新增／编辑／撤销本地闭环均已验证，不关闭 OPS-07-03 整体、完整 RBAC 或生产验收；40 个稳定验收 ID 和全部历史记录保留。

- **入口与支持范围**：[BindingRevokeDialog](../web/src/components/BindingRevokeDialog.tsx) 从明确绑定条目创建撤销提案，冻结绑定 ID、完整主体哈希、租户、角色权限、对象原数组、期限、身份及版本。复用 bindingChange 的普通判定，不套用编辑的变化检测或新期限输入限制，不复制字段白名单。仅支持规范化小写且无别名歧义、具体有效租户、可核验非 bootstrap 来源、非 JIT、六个绑定审批字段默认及角色／对象／期限可准确解释的普通绑定；普通已过期绑定允许，createdAt 非零本身不排除。列表仅排除已知问题，历史快照来源以服务端可信详情为准。
- **请求与生命周期**：[bindingRevoke](../web/src/bindingRevoke.ts) 仅发送 removeBindingIds=[固定 ID]、expectedVersion、UUID idempotencyKey、requiresDualApproval=true；无绑定、主体、角色、租户或引用证明。提交前只读核对身份、管理资格、版本、原目标和角色，消失／漂移／歧义阻断。相同身份／租户／目标／版本／载荷复用 sessionStorage 键；失败和关闭重开不换键，rejected 本次不重建，下次明确提交才重建；错位响应保留可能已保存事实，不自动补发。响应 ID、摘要、状态、actorHash 均核验；真实详情必须 binding/revoke/ready、before 完整匹配、after=null，保留对象顺序／重复与纳秒。创建和批准不乐观移除列表。
- **审阅、应用与结果**：复用原独立批准／创建人应用链，公共应用前重读详情、身份、摘要和版本；GET/POST 非原子，最终仍由原 Runner／Store 授权、摘要、版本及事务保护。公共结果区分编辑／撤销，角色删除旧返回兼容。仅 applied 且按实际版本读回目标不存在才提示撤销核对完成；响应丢失只读恢复，不重发 apply。明确 applied 后 403、管理资格变化、提案缺失／摘要错配保留“已应用，当前结果待核对”；未恢复到 applied 且无法读取时标记结果未知。applied 详情不要求重建失效 before，不引导重建或重新授权。所有后端运行源码字节保持，三项既有保护随回归保留。
- **真实 Cookie 与权限证据**：[browser-reviewed.log](../output/s3.3c/browser-reviewed.log)／[结构化结果](../output/s3.3c/browser/results.json)：合成 Cookie→Web认证／CSRF→Unix代理→Runner→临时SQLite，四条创建／另一会话审阅批准／创建人应用闭环；逐条证明创建、批准时绑定未变，应用后消失，其他绑定／主体／租户／角色不改写。唯一授权主体同一 Cookie 的详情请求 200→403；有另一条独立授权时 200→200。普通过期目标撤销而其他过期行保留。自身管理绑定撤销后创建人 Cookie 403，页面保留 applied 事实，由原独立批准人只读核验；未削弱最后管理员保护。另一条链真实丢失 apply 响应后只读恢复；所有 applied 终态详情读取零新增写入。
- **验证**：[verification.json](../output/s3.3c/verification.json) 汇总最终 Web 57/57、lint 0错误／1条既有 identityGeneration 警告、typecheck/build 通过；Go 指定四包 macOS external 链接 826 个通过事件、0失败，Python3.12仅映射测试子进程。最终六项真实浏览器测试包含本轮四循环与既有绑定新增／编辑、租户、角色新增编辑删除。十组共享组件回归含定时计划。新组件测试覆盖取消／Enter零写入、重复点击、冻结目标消失／版本／资格、失败重试／重开、rejected显式重建、错位创建人、错误投影、applied、读／写延迟、身份和租户分别往返、卸载及迟到应用通知；Cookie网络层A→B→A未模拟，明确属于真实React组件内存API注入。桌面1280、375px、明暗偏好、长完整权限／对象／哈希、焦点循环归还已验证，长正文滚动时按钮完整可见；沿原CSS，无新暗色主题。
- **独立复核**：[review.txt](../output/s3.3c/review.txt)。默认只读代理、fork_turns=none，发现3项P2：公共恢复角色删除遗漏响应身份核对、明确applied后读回不足丢失应用事实、binding终态误提示重建。前两项保留 [红灯](../output/s3.3c/review-red.log)／[绿灯](../output/s3.3c/review-green.log)，第三项加入真实浏览器终态检查；修正后全部受影响检查重跑，代理定点复查确认消除并独立测试25/25，无新增相关发现。代理未独立重跑完整Go／浏览器／视觉，相关证据由主代理验证。
- **工作区与增量**：起点与收尾 main／c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca、暂存区为空，未观察到新的外部提交。起点 [1075文件指纹](../output/s3.3c/baseline.json)／[原字节](../output/s3.3c/baseline.zip) 以现行S3.3b工作区为准，旧HEAD和原27项仅历史。S3.3b旧patch起点复算匹配，未重放／stash/reset/checkout；316个历史output与scripts/local文件字节不变。旧脚本及全部固定输出隔离在S3.3c后运行；未覆盖旧截图。S3.3b前端文件需按本轮增量逐块回退，不能整体恢复旧工作区。
- **指纹与回退**：[12文件patch](../output/s3.3c/s3.3c-local.patch) SHA-256 `d75c3ed7cbf50884bfe3789ca099b1b1ba02ac01e9da88dabee71584b192d88a`；文件集合 SHA-256 `772a46a90f113f112a1ac5f27a622d692f87c533661f22503c99815b03a3b9c0`。按服务相对路径排序、UTF-8路径+NUL+原始bytes+NUL，排除前阶段差异、台账自引用及output。隔离正反向check/apply及12/12字节重建通过，见 [integrity.json](../output/s3.3c/integrity.json)。仅撤本轮代码／测试增量，保留后端保护；代码回退不恢复已撤销绑定，本阶段全部为可丢弃合成数据，无自动重新授权。
- **边界与资源**：自建本地Vite、测试HTTP／Unix服务、临时SQLite、浏览器、Python映射和重建目录已清理，保留必要证据。未连接生产、读取生产数据、改配置／治理／权限、安装、提交、推送、发布、部署或向其他对话发消息。特殊历史／JIT、大写ID完整支持、ID碰撞、最后管理员已知局限、主体／对象规则、存量清退、租户停用／删除、默认Python兼容及生产逐项验收仍保留。治理收尾：新增候选为“权限变更成功与后续可读性须分开判断”，与已有固定截图路径／元数据／ID候选同列待后续讨论；不自动改治理规则，无本轮必改过时引用。本阶段结束，不进入其他阶段。

## S3.4a：租户停用／删除合同与引用冲突核对（2026-10-04，Asia/Shanghai）

阶段状态：本地合同核对与方案准备 **`completed`**，范围仅诊断、隔离刻画与方案整理，关联 **OPS-07-01 / E02 / 原 S2.2**。阶段核对不表示停用、重新启用、删除网页或引用保护已经实现；OPS-07-01、完整 RBAC 与生产验收仍未关闭。仅同步 E02、OPS-07-02／03 当前进展摘要：S3.3a2/b/c 普通绑定新增／编辑／撤销已本地验证，特殊范围、整体权限及生产仍未完成；不改历史阶段当时结论及40个稳定ID。

### 状态与删除现行合同

证据等级：**S**＝静态源码核对；**E**＝本轮重跑既有测试；**D**＝本轮临时 overlay 刻画。以下路径行号相对服务目录，绑定本轮 `main / c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca` 与保留的既有工作区，不能替代生产观察。

| 输入／入口 | 规范化与存储 | 授权消费／实际结果 | 来源 |
| --- | --- | --- | --- |
| 模型及配置租户状态 | `model.Tenant.Status` 是 string，没有枚举；AccessPolicy.Normalize 不规范化租户 map 的 ID／状态。默认租户必须存在且空或 active；非默认只检查引用存在，不统一限制状态 | 空、active、disabled、other、ACTIVE 的非默认租户可加载；disabled／其他值不是一套已定义生命周期。配置对象指向未声明租户时加载拒绝 | S：`internal/model/control.go:27`、`internal/config/policy.go:125`、`config.go:591`；D：TestS34ConfigTenantStates（临时JSON，Load(false)，schema3，不证明完整schema4启动） |
| API 提案租户输入 | 创建保存审批载荷，未调用租户规范化；可保存 disabled 提案。应用才 trim/lower ID、trim 名称，空状态→active，精确 disabled 拒绝；状态本身不 trim/lower，其他值未拒绝 | 无引用时 suspended／ACTIVE／带空格 disabled 能应用，但 tenantIsActive 为 false；有剩余主体／具体绑定时最终 proposed 检查拒绝。**不能将其他状态当停用替代值** | S：`runner/access_changes.go:23/83`、`access.go:120/236/469`；E：TestTenantProposalHTTPContract；D：TestS34StateContract |
| Store 直接 UpsertTenant | 只要求 ID／名称非空，空状态→active；disabled／其他原样存储。SQL status 无枚举 CHECK；直接 Store 是内部承载能力，不是用户受控入口 | Store.Authorize 只检查绑定／角色／期限／对象，不查询租户状态；当前生产调用点仅 rbac.go 两处，外层先检查操作者租户 | S：`store/access.go:20/442`、`store/schema.go:191`；D：合成表行／快照注入，三种 Runner 门控与 Store 对照 |
| 当前授权 | authorize、authorizePlatform、actorTenantID 从有效策略快照读取，操作者租户不存在／非空非active拒绝；空主体TenantID才回退默认 | tenantIsActive 有“租户map全空且ID为默认”兼容例外；schema≤3未强制策略也有放行分支。对象目标租户的存在／active没有统一复验；不能说所有入口完整解释状态 | S：`runner/rbac.go:17/46/115/164/252/354`；D：空／active允许，disabled／other阻断受测主体三个入口；Store绑定判断仍允许 |
| 新增／编辑／重新启用 | 同ID upsert完整替换，不是字段patch；名称／状态用新值，省略状态会变active。快照创建元数据按输入替换且CreatedBy改为操作者；表行冲突更新仅名称／状态／updatedAt，保留旧createdAt／createdBy | 后端没有 create/edit/disable/enable 操作区分；active upsert可恢复可写普通记录，仍受bootstrap等检查；没有可信重新启用合同。旧绑定／直接角色若保留，恢复active后仍参与授权（还受主体状态、期限/JIT等约束） | S：`access.go:120/469`、`store/access.go:20`；D：TestS34ReplacementMetadata、授权状态切换；未把注入当正式启用API验收 |
| 删除ID | normalizeIDs＝trim、lower、去空、去重、排序；不存在目标跳过；空效果仍提交新快照／收据及审计 | 大写别名可能删除小写对象而保留大写原行；尚无绑定撤销同等的租户原始ID防歧义校验。新增后删除同ID按先upsert后delete，最终不存在；非active无专门删除禁令 | S：`access.go:209/221/418`、`store/access_policy.go:212/277`；D：TestS34DeletionContract，含无目标版本+1、同请求增删、大小写与suspended删除 |
| 删除保护／混合变更 | Runner禁止默认租户、检查剩余主体和具体租户绑定，再检查最后管理员；Store事务另外检查精确bootstrap来源及全部tenant_id绑定行。空／未知非bootstrap来源未额外保护 | 已停用主体、过期具体绑定仍阻断；同请求显式移除主体及绑定可通过，默认/bootstrap仍拒绝。不是自动级联。Store表行独有引用也拒绝。最后管理员的已有局限不在本阶段修复 | S：`access.go:200–251/439`、`store/access_policy.go:239–306/415`；D：各拒绝全表不变、混合成功、空来源删除成功 |
| 审批／失败／版本／重放 | 创建／批准不生效；最终应用核验执行身份、原始摘要及事务内摘要；ExpectedVersion>0固定，否则Runner取当前版本。审批本身不作引用／版本最终校验 | 失败保留approved，不产生failed状态或成功闭合审计；新成功版本+1，策略／收据／审计／applied同事务；同执行人applied重放返回原结果，不重复写入，且走既有历史成功分支。GET ready不是后端approve/apply必需条件 | S：`access_changes.go:68–134`、`store/access_policy.go:65–170`；E：版本冲突、摘要、并发单次提交及原子回滚；D：删除成功重放全表相同、失败全表不变 |

### 引用与实际影响：不能概括为“无绑定即可删除”

| 类别 | 当前依赖与边界 | 证据／后续处理 |
| --- | --- | --- |
| A 当前授权 | 主体直接归属（无论主体是否可用）及具体租户绑定（含过期）已阻断；直接角色没有TenantID，通过主体关联。默认／bootstrap受保护。tenantId=`*`不作为某个租户的删除引用，objectIds仍可能指向该租户对象 | S：`access.go:236/241`、`rbac.go:290/327`；D：主体、过期绑定、Store独有行、通配绑定。来源不明及大小写需新保护，不能依赖网页过滤；最后管理员不代表全部可执行管理员安全 |
| B 当前对象与策略目录 | Catalog.Object合并Services和AutomaticTasks，显式TenantID删除后保留；Kubernetes声明及自动更新策略也承载归属。删除不扫描这些目录／策略 | S：`config/config.go:116/391/591/1272`、`runner/auto_update.go:24/123/155`、`configuration.go:722/740`；D：合成服务目标仍解析为原ID，删除不阻断，平台管理员仍通过authorize／Kubernetes租户辅助检查。只验证授权判断，未执行服务或Kubernetes操作 |
| B 新请求／默认回退 | 原租户主体消失／非active时Runner拒绝；在另一有效租户的平台管理员可越租户且目标active未统一检查。已有显式对象TenantID不因租户消失改成默认；空值按各入口既有默认逻辑处理 | S：`rbac.go:77–98/354`、`configuration.go:722`、`recovery_center.go:94`。D已覆盖服务对象与辅助门控；不推导所有新请求均拒绝，也不定性为未证实的跨租户漏洞 |
| B 待批／批准／计划执行 | 普通发布计划在创建、批准、ExecuteReleasePlan及调度入口重新authorize当前执行人，并检查批准摘要、运行身份、恢复点；Kubernetes创建／批准／执行复验平台授权及目标归属。尚无租户停用代次，权限恢复后旧计划不因曾停用而自动失效 | S：`plans.go:165/286/317/593`、`kubernetes_plans.go:198/226`；RBAC提案带正版本会因任何策略变更stale，旧无正版本不等价。只读调用链核对；未动态执行停用前后旧发布／Kubernetes计划 |
| B 排队／运行与恢复入口 | 普通任务start后run以后台context执行各阶段，没有租户状态复验／停用取消；远端分派也没有该统一屏障。批量每项执行会进入计划授权，但不能据此保证已启动项停止。恢复点验证检查服务／租户／服务器／摘要及期限一致，不检查租户active | S：`engine.go:375/390`、`task.go:16/90`、`batch.go:1179`、`recovery.go:140/243/281`、`recovery_center.go:65`。在途可能继续，停止、迁移和补偿均未实现／未实演；远端worker、终端等不扩展重审，归入删除支持前需证明无当前工作或拒绝的范围 |
| B 配置加载／重启 | 配置validate只与配置内租户声明核对，配置对象悬空会拒绝；与动态快照不作统一交叉校验。seedAccessPolicy先EnsureAccessDefaults，已有快照便返回，不从catalog重建普通动态租户。自动更新seed仅补缺失策略，且独立于此返回 | S：`engine.go:201–245`、`auto_update.go:123`；D：Load(false)悬空拒绝、seedAccessPolicy调用不恢复已删普通租户。完整重启／热重载未实演，默认元数据例外与无快照bootstrap不能混为普通租户恢复 |
| C 历史证据 | 历史访问快照、审批／审计、完成计划、历史任务及恢复记录不是当前删除代码的清退对象；没有以tenant为父键的级联删除。普通任务读授权沿当前对象，历史TenantID仅保留当时归属，不能由当前同ID名字倒推历史身份 | S：`store/access_policy.go:277/318`、`store/schema.go`、`rbac.go:192/200`、`store/recovery.go:148`；D：历史策略快照可读、重放全表不变。未逐类创建完成任务／恢复历史夹具；仍有效且可用于新恢复的恢复点应同时列B的候选影响，不把全部历史永久阻断删除 |
| ID复用 | 删除没有墓碑／代次预留；重新创建同ID可成功，遗留对象引用、通配范围和历史关联仍使用同一字符串；不会自动恢复已删主体／具体绑定 | D：TestS34ObjectsHistoryAndReuse；S：upsert/delete与历史快照。必须另定禁止复用或显式代次规则；不默认允许自动重建。代码回退不能恢复已删租户、授权或业务数据 |

### 最小后续方案（候选，未批准、未实施）

**推荐下阶段唯一目标：先建立单个普通租户停用／重新启用的后端安全合同及可信审阅兼容，删除保留为后续独立阶段。** 原因：仅放开disabled会与现有“剩余引用必须active”冲突，且没有目标租户／旧计划边界；先做删除网页会暴露已确认的目录悬空和ID别名问题。下阶段实施前仍须用户批准以下行为变化，不能把本次诊断授权延伸为实现授权。

1. **停用／启用候选**：只支持规范小写、可核验普通来源、非默认／bootstrap、状态明确的单目标；显式active→disabled→active，保留绑定、主体、对象和历史。现有引用完整性须区分“存在”与“可授权”，否则保留引用无法停用。建议停用拒绝新授权／新操作，同时覆盖操作者及目标租户（平台管理员如何处理历史只读见待决策）；启用恢复仍有效的原授权，不重建／续期绑定。最终apply强制状态转换、来源／别名、引用完整性、版本与管理员可用性；网页筛选不是保护。最后管理员相关增量须单独限界核验，不借本阶段扩大专项。
2. **旧计划／在途候选**：推荐未开始的旧计划不得因重新启用自动复活，需重新提案；运行中的工作不自动取消或补偿，首版可在无法完整判断有无可执行工作时拒绝停用。实现旧计划失效通常需要持久化租户生命周期代次／时间基准并绑定计划，或经明确批准原子失效相关计划；二者都不是纯详情改动。不能用全局RBAC版本假冒计划绑定，不默默清退历史计划。
3. **删除候选（以后）**：单个普通租户、无当前主体／具体绑定（包括过期）、无对象／有效策略／未闭合可执行工作引用；未知／无法完整计数拒绝。通配绑定不要求全平台撤销，但其具体对象引用必须纳入判断；保留历史证据，对可执行恢复点单列影响。建议墓碑或身份代次禁止隐式ID复用。后端最终应用需检查同一变更单元内权威来源，不能靠详情GET计数与POST之间的时间窗口。首版不接受混合主体／绑定移除，不做迁移、级联、自动撤权或业务数据删除；这些均为新支持范围及后端限制，须批准后实现。

**可信详情与消费者最小范围**：before仅来自请求expectedVersion绑定、摘要验证后的不可变访问策略快照；当前表行只用于来源／一致性核验。after对停用／启用只表示同ID／名称及明确新状态，删除显式null（不得用空对象代表不存在）。保留现有tenant create／rename响应语义；新增操作应有独立可校验分支／投影，不直接将现有tenant操作扩成旧网页会误读的值。现有租户after为omitempty且网页要求active，增加delete需明确null合同及类型，不能只改kind。

引用投影建议仅返回类别计数、阻断原因及complete标志，不返回主体标识、完整策略、业务内容或其他租户列表；版本标注至少包括RBAC expected/currentVersion、目标表行核验与catalog摘要、工作/恢复数据观察基准。现有访问版本不能覆盖独立任务状态与catalog变化，没有统一版本时必须标注非原子且最终事务／执行屏障复验；无法完整判断用unavailable，不能假零。stale、unsupported、unavailable均不输出可批准局部差异；若新增“非ready不得apply”服务端规则需显式授权。原有GET双次授权/no-store、原摘要、独立批准／创建人应用与事务保持。

最小同步消费者为 `internal/model/control.go`（若引入显式操作／代次）、`runner/access.go`／`rbac.go`与受影响执行入口、`store/access_policy.go`及确需持久化的字段、`runner/access_change_detail.go`；前端 `types.ts`、`accessChangeReview.ts`、`components/AccessChangeReview.tsx`、`accessChangeApply.ts`及App／AccessControl实际结果回调。新表单可后续单独建设，现有TenantDialog／tenantChange新增改名必须回归。任何含租户生命周期／删除或混合载荷不能落入other放行；后端approve/apply不可把旧网页禁批当边界。应用读回按实际版本核对状态或不存在，applied后失权保留成功事实、只读恢复，不自动再发或重建。

**留给主规划的少量决策**：①推荐停用同时阻断目标操作，历史只读保留平台受控查看；替代是仅冻结该租户主体（不能称目标停用）。②推荐旧未执行计划重新提案并使用持久化生命周期代次；替代允许恢复后沿原审批继续，必须接受旧授权恢复风险。③推荐删除首版拒绝ID复用并仅允许无上述当前引用；替代先只提供停用、暂不开放删除。D1配额、D2终端继续待决策。

批准范围须列目标状态、恢复授权、旧计划／在途语义、必要持久化与最小前后端接口；仅本地合成测试实施，生产另批。验证覆盖保留绑定的停用／启用、同Cookie当前权限、平台目标门控、旧计划再执行、并发状态／引用变化、失败原子性、默认／bootstrap／来源／别名／最后管理员、历史保留及create/rename回归。回退前先核对已停用状态、墓碑／代次与新计划；旧二进制不认识新字段时不能直接回退，更不能把代码回退当数据恢复。真实数据恢复、迁移或重新授权须独立批准。

### 验证、独立复核与保全

- 本轮读取仓库AGENTS及全局架构／可靠性参考、上述有限源码、S3.1/a、原S2.2、S3.3a2/b/c、版本／审批／证据例外。纯本地任务不读取生产inventory、不连接生产；未调用完整界面开发流程或安装依赖。当前可执行权限配置不能代表生产授权，Warp Profile未核验也未用于放行。
- [独立证据目录](../output/s3.4a/contract-check/) 保存 [起点1190文件摘要](../output/s3.4a/contract-check/baseline.json)、[Runner刻画源码](../output/s3.4a/contract-check/tenant_contract_test.go)、[配置刻画源码](../output/s3.4a/contract-check/config_contract_test.go)、[overlay映射](../output/s3.4a/contract-check/overlay.json)。发现既有 `output/s3.4a/baseline.json` 后保留并改用子目录；没有覆盖原输出。
- macOS arm64，已有Go1.22.12，external链接，GOPROXY=off／GOTOOLCHAIN=local仅测试子进程。最终 [既有定向日志](../output/s3.4a/contract-check/go-existing-final.jsonl) 35个含子测试通过事件、0失败（Runner／Store）；覆盖租户HTTP／详情／隔离、stale原子性、摘要、并发提交与事务回滚。最终 [新增刻画日志](../output/s3.4a/contract-check/go-characterization-final.jsonl) 21个含子测试通过事件、0失败（Runner／Config）。真实回环HTTP＋临时SQLite／合成身份，未执行真实业务命令；不重跑完整四包、前端构建或浏览器。
- 初次overlay新增虚拟文件被Go1.22 vet报文件不存在而阻断，见go-tests.jsonl；随后仅刻画关闭vet。首次刻画将未登记对象用作“active应放行”夹具，触发既有未知对象早返回，见go-tests-overlay.jsonl；修正为已声明且归属一致的合成对象后通过，没有改产品或放宽状态断言。既有测试最后独立运行、未关闭默认vet。日志中的缺陷刻画通过不代表保护完成。
- 未验证项：完整schema4配置冷启动／热重载、真正并发目录／工作变化、停用期间在途与旧计划实演、远端执行器、完整终端／扩展等外围生命周期、逐类历史任务／恢复夹具及生产行为。对于后续允许停用／删除的目标，无法排除当前可执行引用时必须保守限制范围，不能把本次有限核对写成“全量无引用”。
- [独立只读复核](../output/s3.4a/contract-check/review.txt)：默认配置、fork_turns=none，仅指定关键路径；无新增阻断事实错误／P1／P2，独立解析测试计数一致，未独立重跑测试。主代理沿已读授权、Store和前端条件交叉核对并采纳；不把复核当全量引用证明。
- [最终验证与保全](../output/s3.4a/contract-check/verification.json)：起点1190文件中只台账改变；产品源码、原测试、scripts/local与全部既有输出逐字节保持。逆向还原三条当前摘要后，历史正文（排除新增S3.4a）SHA与起点相同，40个稳定ID及新增链接通过；diff --check通过。S3.3c patch SHA `d75c3ed7cbf50884bfe3789ca099b1b1ba02ac01e9da88dabee71584b192d88a`、12文件集合 SHA `772a46a90f113f112a1ac5f27a622d692f87c533661f22503c99815b03a3b9c0` 起止一致；main／c0b4150、暂存区为空，不恢复旧Git状态。
- 资源：本轮测试子进程均退出；回环HTTP及临时SQLite由测试Cleanup关闭／移除，无自建长驻服务，未启动浏览器或Python映射。仅新增本阶段隔离证据并更新唯一台账，未提交／推送／发布／部署或生产连接。治理收尾候选：租户状态的分层差异、删除ID别名、当前对象与历史引用分类、ID复用与旧计划代次；仅报告，不修改规则。当前过时绑定摘要已同步，历史记录原样保留。完成即停止，不实现下一阶段、不向其他对话发消息。

## S3.4b1：租户生命周期持久化与原子转换基础（2026-10-04，Asia/Shanghai）

阶段状态：**`partially_completed`——A 方案已就绪，B 实施待专项批准**。关联 **OPS-07-01 / E02 / 原 S2.2**。本节仅追加已确认合同、源码核对与具体批准候选，不表示生命周期基础、工作准入、旧计划阻断、停用网页或生产验收已经完成。40 个稳定 ID、原验收行及全部历史正文保留；下述代码、字段、SQL 均为拟实施内容，尚未写入产品或执行迁移。

### 已由用户确认的目标合同

1. 停用保留主体、绑定、对象和历史；阻断该租户主体的新授权及以该租户为目标的新操作，其他有效租户的平台管理员也不能绕过目标停用。
2. 其他有效租户内、已有相应权限的平台管理者可以查看历史审计与已结束记录；不新增跨租户权限，不把实时检查、执行或恢复归为历史读取。
3. 重新启用只恢复仍符合当前主体状态、期限及其他规则的原授权；不续期、不重建绑定、不自动执行任务。
4. 使用持久化的租户生命周期代次；停用前尚未执行的计划在重新启用后仍须重新提案审批，保留原记录；全局 RBAC 版本不能代替租户代次。
5. 首版发现排队、运行或无法可靠判断的工作即拒绝停用，不自动取消、终止或补偿；最终检查须与新工作准入协调，不能只查一次数量。
6. 删除另立阶段，目标为无当前引用且禁止隐式 ID 复用。本阶段无删除、清退、业务数据迁移授权。

以上不再请求语义确认。用户明确要求 **A 不改产品实现或迁移文件、不运行新迁移；B 具体安全实现及本地合成库迁移另批**。本轮采用 areasong-development 的方案准备路径及架构、工程交付、可靠性参考，不加载 UI 流程，不改治理文件。

### 有限核对与结构决策

- 当前 47 条迁移位于 [schema.go](../internal/store/schema.go)，不是独立 SQL 文件；[Open / migrate](../internal/store/store.go) 自动建基础表、逐迁移事务更新 `PRAGMA user_version`，并 chmod 目录和文件，不能拿现有开发库试跑。`OpenExisting` 不迁移且要求版本精确匹配。`SetMaxOpenConns(1)` 只限制一个 Store 实例，不证明不同进程的互斥。
- 状态权威继续为最新 `access_policy_snapshots.policy_json` 中的 `AccessPolicy.Tenants[id].Status`；`tenants.status` 是现有持久化镜像，不能成为另一套独立状态机。[effectiveAccessPolicy](../internal/runner/rbac.go:17) 已按此读取；[seedAccessPolicy](../internal/runner/engine.go:206) 有快照便返回，不从 catalog 覆盖普通动态租户。
- **只增加 `tenants.lifecycle_generation` 一列，SQLite INTEGER / Go int64，非负，0 表示未知，已核验基准为 1。** 状态仍用现有字段。代次以此列为唯一权威，不存第二份可独立写入的 status，不向旧快照补代次。单事务读取快照和目标行，返回独立 `TenantLifecycle{TenantID, Status, Generation, PolicyVersion}`；状态取快照，代次取行；镜像不一致拒绝，不选择其中一份覆盖另一份。
- 不向 [model.Tenant / AccessControlUpdateRequest](../internal/model/control.go:27) 或 [config.AccessPolicy](../internal/config/policy.go:99) 加字段。采用独立模型文件，历史 payload、快照、摘要、签名／审批载荷保持字节不变。新生命周期快照仍用现有策略编码，仅修改目标状态／更新时间；代次与该快照的关联由同事务行更新、原审批载荷、成功审计及 applied_policy_version 固定。历史快照本身不能回答当时的代次；缺失即未知，不能拿当前行反填。
- 现有快照与行的 createdAt/createdBy 并不总相等（S3.4a 已刻画）。一致性核对用目标 ID、displayName、状态和来源类别；普通来源要求行与快照 createdBy 均为规范的 64 位小写十六进制身份，不要求两个合法历史编辑者相等；任一为空／未知、bootstrap 与普通来源矛盾即不初始化、不转换。创建元数据保持原值，不借本阶段修复历史差异。
- [applyAccessPolicyMutationTx](../internal/store/access_policy.go:161) 已覆盖行、快照、收据及策略审计；外层 [ApplyAccessChangeMutation](../internal/store/access_policy.go:65) 同事务完成审批终态及闭合审计。复用这套事务边界与两账号规则，不能先提交生命周期行再写审批。

### B 候选数据迁移与兼容矩阵

拟追加第 **48** 条迁移。以下 SQL 只作为审阅文本，本轮未执行：

```sql
-- migrate 的第 48 条事务内执行，现有 1–47 条原样保留。
ALTER TABLE tenants ADD COLUMN lifecycle_generation INTEGER NOT NULL DEFAULT 0
    CHECK (typeof(lifecycle_generation) = 'integer' AND lifecycle_generation >= 0);

-- 随后在同一事务中运行 Go 初始化检查：读取最新快照及候选行。
SELECT version, digest, policy_json FROM access_policy_snapshots
    ORDER BY version DESC LIMIT 1;
SELECT id, display_name, status, created_by, lifecycle_generation
    FROM tenants WHERE lifecycle_generation = 0;

-- 仅对通过下述完整检查的单行执行；参数为该行原始 ID/status/created_by。
UPDATE tenants SET lifecycle_generation = 1
    WHERE id = ? AND lifecycle_generation = 0 AND status = ? AND created_by = ?;

-- 初始化检查完成后由原迁移器写版本并提交；任一 SQL 失败整体回滚。
PRAGMA user_version = 48;
```

初始化 helper 在 `migrate` 第 48 条的同一 `sql.Tx` 中调用，不使用独立自动提交。先核验最新快照 SHA-256 与现行 AccessPolicy 精确编码，拒绝将损坏／重复键／未知字段的局部解析用于初始化；然后核验 map key 与行 ID 一致、规范小写非空无首尾空白、无大小写别名冲突、名称一致、状态语义一致、来源符合上节。空状态只按现有语义视为 active，不改原空字符串。每个候选 UPDATE 要求恰好一行。快照缺失／不兼容或个别行不可判定时保留 0，不转 active、不删除；SQL/IO 错误则迁移失败回滚。初始化不创建快照、收据或审批记录，不更改 updated_at，不读取计划时间猜历史。

| 数据情形 | 第 48 条／新写入处理 | 生命周期转换／后续计划行为 |
| --- | --- | --- |
| 新空数据库 | 旧迁移链＋48，空表无回填；新 active 租户 INSERT 显式写 1，空状态沿既有规则为 active | 初次策略 seed 后状态与行匹配才可读取已知代次；无快照不放行转换 |
| 普通历史 active／空状态 | 仅上述检查通过者从 0 建立基准 1，原状态及所有历史字节不变 | 1 是新合同的起点，不代表证明历史没有停用；旧计划仍没有代次 |
| 默认及 bootstrap | 可核验 active/空状态可记 1；row=bootstrap、快照来源空或 bootstrap 仅作为受保护配置来源处理；不冒充普通来源 | `default`、policy.DefaultTenant、bootstrap 任一命中均禁止转换；默认保护不依赖调用方布尔参数 |
| 已有 disabled | 保留 disabled＋0，即使当前名称／来源可核验也不推测历史循环 | 禁止直接启用，后续另行定界的历史处理才能改变，普通 upsert 也不能替代启用 |
| other / ACTIVE / 空白异常状态、ID 别名、来源异常 | 保留原值＋0；不 lower/trim 状态、不将异常值转 active | 生命周期入口返回 unsupported/unknown，无成功写入 |
| 缺快照、快照损坏、租户行／快照不一致 | 保留 0；无快照的旧库不能仅靠表行初始化既有租户 | seed 也不把既有 0 提升为 1；新造的、可核验 active INSERT 与旧行必须区分 |
| 旧历史快照、审批、计划缺代次 | 不补字段、不改摘要、不迁移或作废历史计划 | 本阶段保留旧任务的现行执行路径；后续接入执行门禁时，缺代次按未知拒绝新执行、要求重新提案，须在该阶段批准中明确兼容影响 |

新 INSERT 只能为可判定 active 建立 1；非 active 的 bootstrap 历史配置保持原状态＋0，不能当成支持的停用。冲突 upsert 永远保留已有 generation；改名、其他租户变更、重复 seed／重复打开不会提升 0 或重置正代次。迁移后 ordinary active＋0 的名称编辑仍不替它补代次。代次正值只由新建、一次迁移初始化或专用转换推进，不使用 RBAC 版本或时间。

### B 候选内部请求、事务与普通写入边界

独立模型 `TenantLifecycleTransitionRequest`：`kind="tenant_lifecycle_v1"`、`tenantId`、`expectedStatus`、`expectedGeneration`（int64，必须 >0）、`targetStatus`、`expectedVersion`（必须 >0）、`requiresDualApproval=true`、`idempotencyKey`。仅允许规范单目标 active→disabled 或 disabled→active；同状态请求拒绝，只有相同已成功请求的重放返回历史成功。执行上下文 `tenantLifecycleExecution{ChangeID, Actor, RequestDigest, IdempotencyKey}` 绑定原 access_changes 记录；不接收客户端候选快照、当前代次、来源标记或审批人覆盖值。

新增 **Store 包内私有** `applyTenantLifecycleChange(ctx, execution)`，测试同包调用；B 不导出、不加 Engine/HTTP/CLI/调度适配器。它是事务基础，**没有已完成的工作准入保护，也不是可用的生产停用入口**。不设置固定 true、空计数、进程内开关或假的 checker；未来只有准入与授权集成通过独立阶段验收后才能增加实际调用方。

事务步骤：

1. BeginTx；从事务内读取审批记录与原载荷。核对 idempotencyKey、批准摘要与执行上下文；沿现有 applied 分支：同执行人返回原收据事实，不重算新策略、不重新校验当前代次或重复审计，不同执行人拒绝。新转换只接受 two_party_v1、requires_dual_approval、approved、创建人执行且已有独立批准人，保留现有审批时间／身份规则。
2. 未成功请求重算原 payload 摘要并精确解码独立类型，拒绝额外字段、重复键、不兼容 kind、无正版本／代次；不得把普通访问审批载荷当生命周期授权。普通历史审批语义不因新分支改变。
3. 在同事务读取最新快照、全局版本、目标租户行和代次；核对预期版本／状态／代次、来源／默认保护、行镜像及严格 active/disabled 转换。已知 disabled 必须来自正代次的本合同；0 一律拒绝。对目标具体绑定核对行与快照的共同持久化字段，来源不完整／不同步拒绝；通配绑定不据此声称目标影响已完整统计。
4. 从已验证的当前策略克隆候选，只改目标状态与 updatedAt，保留 ID、名称、创建元数据、其他租户、全部主体／角色／绑定。只验证保留引用仍存在，不要求该目标在转换后 active；不复用会把保留引用误判为删除的通用最终循环。保留既有最后管理员检查，不借此重构其已知局限，也不声称完整管理员可用性已解决。
5. CAS（比较后更新）目标行：`UPDATE tenants SET status=?, lifecycle_generation=lifecycle_generation+1, updated_at=? WHERE id=? AND status=? AND lifecycle_generation=? AND lifecycle_generation<9223372036854775807`；条件使用事务内读取的原始 status，空状态的 expectedStatus 用 active 语义比较但不丢掉原始 SQL 条件。要求影响一行，溢出／0／冲突拒绝。每次成功只 +1，故 1→停用2→启用3，后续绑定1的计划与3不相等。
6. 在同一事务写新策略快照（RBAC version+1）、既有 access_mutation_receipts、`access.policy.updated`、`tenant.lifecycle.changed` 审计（目标/from/to/前后代次/策略版本与摘要）；再按现有逻辑更新 applied 身份／时间／策略版本与摘要并写 `access.change.applied`，最后 Commit。没有生命周期第二收据表；审计和载荷保留代次与本次快照版本的对应关系。
7. 目标不存在返回 ErrNotFound，错误执行人返回 ErrActorMismatch，键／摘要冲突沿 ErrIdempotency，版本冲突沿 ErrAccessVersion，状态／代次冲突用专用错误，未知／受保护／不支持状态单列拒绝原因。任何失败回滚全部行、快照、收据、审计、applied，原请求仍 approved；SQLite_BUSY/快照锁冲突也属于未提交，不自动重试写入。调用方重新读取后只可按原请求显式重试，不能换版本或代次偷偷续批。

实现上从 `access_policy.go` 提取审批核验／终态闭合的私有事务 helper 至 `access_change_apply.go`，由原路径与专用路径共用；普通公开 Store 方法拒绝生命周期 kind，防止伪造普通 mutation。事务由调用路径持有并仅提交一次，不在 helper 内开嵌套事务。

**普通路径必要保护与兼容变化（包含在 B 批准候选）**：

- 普通租户新增仅接受 active／省略状态；不再接受 other、ACTIVE、带空白 disabled 等字符串。已有租户 upsert 只做名称等原有合法元数据处理，不能改变有效状态或代次；空→active 仅沿既有等价语义，不能把 disabled／未知恢复为 active。Runner 与 Store 行／快照写入边界都检查，含 `SaveAccessPolicySnapshot`，不能只靠网页过滤。普通请求仍可保存原 disabled 提案，但 apply 拒绝；不把普通 upsert接入专用方法。
- 普通策略中保留引用的最终检查仅允许**当前已经存在、内容未改、指向代次已知且快照/行一致的 disabled 租户**的主体／具体绑定继续存在；新增或修改的主体／绑定仍走原 active 校验。角色不存在、租户缺失、来源未知、异常状态仍拒绝。验证另一 active 租户改名及普通角色/绑定变更不会因无关已停用租户的原引用整体受阻；不将所有引用检查改为仅判存在。
- 通用 `Engine.ApplyAccessChange` 在现有 applied 重放之后、普通 request 解码应用之前明确拒绝 lifecycle kind（含携带生命周期字段的混合载荷）。原因：现有 json.Unmarshal 忽略未知字段，候选独立载荷会被解成保留幂等键的空变更。HTTP 严格解码只能阻止新外部输入，不能代替 Store 载荷防护。旧详情精确编码分支应保持 unsupported；不新增可批准投影。
- 当前普通删除／ID 复用治理仍是未完成依赖；本阶段不新增删除操作、不重写既有删除合同。代次合同仅覆盖同一持久化租户记录，不能宣称已经解决删除再建造成的身份混淆；外部生命周期入口必须等相关边界另行定界后开放。

### 后续安全屏障接口（本阶段只记录，不接入）

- 新计划应在生成并持久化不可变审批摘要前，在一致读取中捕获目标租户及其正代次，写入计划／审批摘要；多目标保存去重、稳定排序的 tenantId→generation 集合，覆盖每个实际对象／服务／服务器／恢复目标及执行主体归属，不能只写创建者租户。通配范围必须在固定目录证据下解析为完整目标集合，否则拒绝。
- 普通发布的最早执行副作用在 [ExecuteReleasePlan](../internal/runner/plans.go:450) 的 `prepareMaintenanceSilence`，早于 [StartPlanTaskWithEvent](../internal/store/plans.go:695)。未来必须在创建维护静默前获得持久化工作准入登记；再在任务入队事务复验状态／代次、摘要和登记。仅在 INSERT tasks 前检查会漏掉前面的外部副作用。
- [StartTaskWithEvent](../internal/store/previews.go:104)、StartPlanTaskWithEvent、[MarkRunningOwned](../internal/store/tasks.go:154)、本地 enqueue/run、远端 assignment 派发/认领都需纳入统一登记协议。后续建议同一 SQLite 写事务中复验全部目标并登记 queued/admitting/running，停用事务取得相同写串行化边界后检查未结束登记；先准入则停用拒绝，先停用则准入拒绝。多目标一次事务全部成功或全部回滚。
- “无工作”的证据必须来自所有真实入口完备登记及权威表/执行器状态的一致性核对：tasks、task_assignments，以及批量、定时、Kubernetes、扩展、终端、文件、恢复和 Runner 更新各自的在途记录。当前 ActiveTask 只按 service 检索且包含 waiting_confirmation/queued/running/rolling_back，不是完整租户证明；远端租约过期／失联不能视作结束。未登记旧工作、归属缺失、来源读错、无法确认外部副作用结束、目录变化时都拒绝停用。不能用 LIMIT 列表或零条 tasks 代表全量无工作。
- 上述工作登记表、所有执行器接入、授权主体/目标门控、历史读取分类和旧计划缺代次拒绝均**未实现**。B 只通过私有且无调用方的转换基元测试原子性；不宣称其覆盖排队／运行竞争或外部执行器。下一阶段宜先定界共同准入登记与普通发布链（包含维护静默），其他入口未覆盖前继续关闭外部停用。

### B 唯一候选变更单元：文件、验证、回退

时间窗口：用户明确批准后当前本地实施会话；若目标／范围变化再定界。只准本地代码及 `t.TempDir()` 合成库；现有开发数据库、生产数据库、远端资源、提交推送、安装发布与全局配置均不在单元内。备份/保全为本阶段起点文件指纹与必要原字节，以及 B 开始时再次保存的增量起点；无真实业务库备份或读取操作。

路径以下以服务根目录为基准，另标仓库路径。文件集合为拟修改或新增，不代表已经存在：

| 实际候选文件 | 限定内容 |
| --- | --- |
| `internal/model/tenant_lifecycle.go`（新增）及 `tenant_lifecycle_test.go`（新增） | 独立状态读取值／转换请求／kind常量、编码和 int64 边界测试；control.go 与旧 JSON 不加字段 |
| `internal/store/schema.go`、`store.go`、`tenant_lifecycle_migration.go`（新增） | 追加48列、同事务保守初始化 hook；打开未来未知 schema 拒绝，不改旧迁移；无降级迁移 |
| `internal/store/access.go` | INSERT 初始代次、冲突 upsert 不重置／不转换、默认 seed 保留；ListTenants 原 JSON 不加字段 |
| `internal/store/access_policy.go`、`access_change_apply.go`（新增）、`tenant_lifecycle.go`（新增） | 共用审批事务 helper、私有单目标 CAS、快照/行一致性与通用路径防绕过、原子审计收据 |
| `internal/runner/access.go`、`access_tenant.go`（新增）、`access_changes.go` | 普通租户状态边界、仅对未改且已知停用的原引用作窄兼容、明确拒绝生命周期通用apply；不增加可调用转换入口 |
| `internal/store/tenant_lifecycle_test.go`、`tenant_lifecycle_atomic_test.go`、`tenant_lifecycle_migration_test.go`（均新增） | 临时磁盘库、旧结构夹具、事务故障、重开、确定性并发与全表内容证据 |
| `internal/runner/tenant_lifecycle_boundary_test.go`（新增）、`tenant_change_test.go`、`internal/store/backup_snapshot_contract_test.go` | 普通访问回归、HTTP/旧详情拒绝、seed/配置来源隔离、真实schema48备份校验回归 |
| 仓库 `scripts/backup/areasong_ops_snapshot.py` | 直接受影响的只读备份验证器：BACKUP_SCHEMAS增加48，继承47关键列并要求tenants.lifecycle_generation；RESTORE_SCHEMAS明确保持原{4,5,45,47}，不随备份集合扩大；不改恢复执行逻辑 |
| `deploy/production-change-packages.md`、`output/s3.4b1/` | 仅本唯一台账追加批准/实绩；本阶段隔离测试证据、增量patch与指纹 |

必须验证（B 未执行，不写“通过”）：

1. 空库全链建库、真实47旧结构升级、重复打开；含普通active/空、默认/bootstrap、disabled/异常/未知来源/别名/快照不一致夹具。断言列类型/默认/版本、0/1分类、迁移失败不半完成；租户、绑定、历史快照原字节、摘要、审批/计划载荷与既有审计逐项保留。
2. 内部停用/启用保持主体、绑定、对象引用和历史；代次1→2→3，每次全局版本仅+1，成功审计与收据次数准确；改名、其他租户、原请求applied重放不推进目标代次，历史缺代次不补章。
3. 错误身份、自批/未批、错摘要/键、无正版本/代次、不存在、状态/代次/全局版本冲突、来源/默认/bootstrap/异常记录、int64溢出均拒绝且实际持久化内容不变。新生命周期载荷不能经通用 Store/Runner apply 变成空成功，原两账号及失权后applied重放不回归。
4. 对CAS行、快照INSERT、收据INSERT、策略审计、生命周期审计、applied更新及闭合审计逐处注入失败（如临时库触发器），比较全表与版本，无残余成功。失败重试只提交一次，数据库重开后仍一致。
5. 使用两个独立 Store/SQLite 连接、受控屏障和不同已批准请求竞争相同目标/预期代次；不能靠单实例连接池证明互斥。恰好一个新转换提交，失败者保留approved且不留收据/审计，接受明确CAS/版本冲突或SQLite忙错误；同请求竞争最终只一个applied事实。不使用 sleep 猜时序。
6. cold start、重复seed、不同catalog内容及配置入口重新取有效策略，均不能重置状态/代次；普通租户新增/改名、角色/绑定审批回归，保留未知数据的拒绝行为；未来旧计划门禁未实现保持明示。
7. HTTP PUT/POST拒绝生命周期字段、通用apply拒绝合成已存生命周期载荷、详情unsupported；源码/AST检查私有转换仅有定义及同包测试调用、无生产调用点、无路由/CLI/调度注册、web/src逐字节不变。B不强制浏览器回归，独立类型仍需旧请求、旧快照、租户/角色/绑定详情精确编码消费者测试。
8. 备份校验用Go真实48临时库验证：自包含合法快照可只读validate，缺代次列拒绝，45/47保留；allow_legacy恢复模式仍拒绝48。仅为必要测试子进程映射已安装Python3.12，不改全局环境、不修范围外恢复脚本。
9. 最终相关编辑后运行 `go test -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web -count=1`；包级必需回归、迁移/并发定向证据均须完成。默认配置只读子代理、fork_turns=none，提供批准单元、原数据夹具、实际增量/SQL/事务与日志；复核不修改、不派生，主代理修正后重跑受影响验证。

回退：A没有产品/数据库回退需求。B代码只可按本阶段增量逐块撤回，保留前阶段差异。临时库可随测试清理；不写降级SQL、不清除正代次、不恢复真实数据。**旧47二进制的 Open 未拒绝更高版本，能忽略新列并通过旧upsert破坏生命周期；OpenExisting反而会因版本不符拒绝。不能承诺代码回退即可。** 有生命周期数据时禁止直接降级；只能在独立、另行批准且验证的数据/程序配对方案下讨论，生产恢复未设计或授权。新的 Store 对未来schema的拒绝也不能约束已发布的旧二进制。

### A 已执行证据与退出

- 起点/收尾保持 `main / c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca`、暂存区为空。阶段目录创建前确认不存在，保存 [1201文件起点指纹与台账原字节](../output/s3.4b1/baseline.json)，覆盖原代码/测试、scripts/local与全部旧output；不恢复旧b4ec0f9或27项暂存，不重放patch。
- [合成检查源码](../output/s3.4b1/probe/contract_test.go) 与 [执行日志](../output/s3.4b1/contract-probe.log)：macOS arm64、已安装Go1.22.12，external链接，3个顶层测试/5个含子测试通过事件。仅导入现有model/config：旧请求/快照精确编码、严格HTTP解码与宽松apply解码差异、Go AST确认当前47条迁移及旧模型未加字段。无 Store.Open、数据库、候选迁移执行或网络服务；这不是新迁移、原子性或准入保护通过的证据。
- [最终保全与隔离重建](../output/s3.4b1/integrity.json)：仅唯一台账追加，原正文逐字节保留、40个稳定ID不变、新增引用存在、diff --check及1201起点文件保全检查。排除台账自引用、所有output与前阶段差异后，本轮产品/测试增量为0文件；[增量patch](../output/s3.4b1/s3.4b1-local.patch)为空，patch及空文件集合SHA-256均为 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`。空patch不作git apply成功声明；隔离重建校验台账原字节＋本节追加及文件集合不变，台账完整指纹另载integrity以避免自引用。
- 历史 S3.3c patch SHA-256：`d75c3ed7cbf50884bfe3789ca099b1b1ba02ac01e9da88dabee71584b192d88a`；12文件集合 SHA-256：`772a46a90f113f112a1ac5f27a622d692f87c533661f22503c99815b03a3b9c0`，本轮起止匹配。A未进行B的数据库/四包/独立复核；必需验证保留待批准，不把以前日志或本轮静态检查冒充实现验收。
- 无自建数据库、映射、HTTP服务或浏览器；合成检查进程已退出，保留必要证据。生产未连接、未读取，运行态未改；无安装、提交推送、部署、全局配置/治理修改或对其他对话发消息。Warp Profile未核验且不用于推导授权。
- 治理收尾候选（仅报告）：①schema新增须同步只读备份兼容白名单并区分恢复允许版本；②通用宽松解码会把新kind误读为空变更；③维护静默早于任务入队，准入屏障必须覆盖最早外部副作用。未发现本轮必须替换的历史引用，未写治理规则。A结束后仅申请上述一个具体B单元批准，未获批准即停在本状态；不自动进入执行门禁或网页阶段。

### B 获批实施与本地交接（2026-10-04，Asia/Shanghai；当前状态）

**B 状态：`completed`——生命周期基础本地完成。完整租户生命周期安全合同仍为 `partially_completed`。** 本段更新上文 A 退出时的状态；A 方案、批准候选、历史正文和证据均按原字节保留。用户已在本次 S3.4b1 本地窗口明确批准唯一 B 单元及兼容影响；没有生产连接、真实数据迁移、系统安装、全局配置、提交、推送、发布或部署授权。本轮实施和验证只使用自行创建的合成临时数据库，没有打开现有开发库或生产库试迁移。

**已实现的边界**：

- 迁移由 47 增至 48，仅追加 `tenants.lifecycle_generation`，保留旧 1–47 条。初始化在第 48 条同一事务内核验精确快照编码、摘要、ID/别名、名称、状态语义及来源类别；仅可核验 active/空状态建立基准 1，其余保留 0。SQL 错误回滚列、初始化和 user_version。新 Store 拒绝未来 schema；这不能保护旧二进制。
- 独立 `TenantLifecycle`／`TenantLifecycleTransitionRequest` 不改变旧 Tenant、AccessPolicy 或普通请求编码。`GetTenantLifecycle` 在同一读取事务返回当前权威状态与行代次；不向历史快照、计划、审批或摘要反填代次。规范来源的新 active INSERT 可记 1；普通冲突 upsert、重复 seed/打开不重置或提升代次。
- `applyTenantLifecycleChange` 保持 Store 包内私有，仅同包测试调用；没有生产调用方、HTTP／CLI／网页／调度注册或虚假准入检查。显式 active→disabled→active 使代次 1→2→3，每次全局策略版本只加 1；同一事务覆盖审批核验、CAS、目标状态/时间、新快照、原收据、`access.policy.updated`、`tenant.lifecycle.changed`、applied 和 `access.change.applied`。同执行人成功重放只返回历史成功事实，不重新推进代次或重复审计。
- 普通 Runner／Store upsert、快照保存及通用 apply 已加生命周期边界。新增租户只走 active/省略状态；普通 upsert 不能恢复 disabled/未知状态。普通变更只窄兼容内容未改、已知代次且镜像一致的 disabled 原引用；新增/修改主体或绑定仍须通过原 active 校验。明确缺租户/角色拒绝，包括首次快照；旧无租户目录且未声明归属的 Store 格式保留兼容，非 active 配置初始化仍保留 0。
- 私有路径保留既有最后管理员状态、期限和 JIT 主体筛选，再复用原 Store 检查；没有扩展管理员模型，也没有宣称解决原算法的租户状态及绑定分支局限。
- 只读备份校验器支持 schema48，并要求代次列和继承的 47 关键列；恢复允许集合明确保持 `{4,5,45,47}`。直接受影响的既有 Python 校验测试同步更新“48 不支持”的旧断言，继续拒绝伪标48/缺列；恢复执行脚本未改。

**验证与证据**（均为本地合成场景，不是生产验收）：

| 验证 | 已执行证据与结论 |
| --- | --- |
| 修改前复核 | [B 起点](../output/s3.4b1/b/baseline.json)、[源码及台账原字节归档](../output/s3.4b1/b/source-baseline.zip)、[候选文件存在性](../output/s3.4b1/b/candidates.json)、[静态起点探针](../output/s3.4b1/b/baseline-probe.log)：main/c0b4150、暂存区为空、47迁移；1207个原文件指纹。A目录原文件不覆盖，B单独使用子目录。 |
| 迁移与历史保留 | `tenant_lifecycle_migration_test.go` 覆盖空库、真实47结构、active/空/default/bootstrap/disabled/异常/未知来源/别名/行快照不一致、缺失/损坏/重复键/未知字段快照、初始化失败回滚、重复打开、seed及未来schema拒绝。比较全部旧表内容和租户原列；绑定、历史快照、审批/计划载荷与摘要不改写。 |
| 原子性、重放与竞争 | 私有转换往返及错误身份/批准/摘要/键/版本/状态/代次/来源/保护对象/溢出拒绝；CAS、快照、收据、两类变更审计、applied、闭合审计逐点故障注入，失败比较全表和重开结果。两个独立 Store/SQLite 连接经通道屏障读取后竞争，不使用 sleep；不同请求恰好一次提交、失败者仍 approved；同请求最终一个 applied 事实。 |
| 真实入口与冷启动 | Runner HTTP PUT/POST拒绝生命周期字段；已存独立或混合载荷经通用 apply拒绝、旧详情unsupported。普通租户新增/改名及角色/绑定审批回归；实际schema4合成配置Load(false)、NewEngineChecked冷启动/重复seed/不同catalog均不重置持久化状态或代次。AST检查无生产转换调用点；web/src保持逐字节不变。 |
| 最终四包回归 | [go-final.jsonl](../output/s3.4b1/b/go-final.jsonl)：`go test -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web -count=1 -json`，4包、366个顶层测试、921个含子测试通过事件、0失败。6个浏览器开关测试按原条件跳过；B未改网页，不把跳过写成浏览器验收。 |
| 竞态与复核修正 | [完整生命周期race](../output/s3.4b1/b/go-lifecycle-race-reviewed.jsonl)：17个顶层、74个含子测试通过事件；最后首写修正后补跑[首写/seed定向race](../output/s3.4b1/b/go-first-snapshot-race.jsonl)：2个顶层、6个含子测试通过事件；均有包pass终态、无竞态失败。[最终复核反例组](../output/s3.4b1/b/go-review-fixes-final.jsonl)：19个含子测试通过事件。 |
| 备份/恢复兼容 | Go四包内真实45/47/48迁移库导出自包含快照：48可备份校验、缺代次列拒绝、allow_legacy恢复模式拒绝48。Python [快照8项](../output/s3.4b1/b/python-snapshot.log)及[原恢复12项](../output/s3.4b1/b/python-restore-regression.log)通过；后者仅证明原支持版本回归，未实现48恢复。 |
| 独立只读复核 | 默认配置、fork_turns=none，两个代理分别核验持久化兼容及事务/入口；不改文件、不打开库、不自行运行测试。初次发现实体ID误判、缺失引用零值绕过、管理员筛选遗漏；[受审原增量](../output/s3.4b1/b/review/input.patch)上的[overlay反例](../output/s3.4b1/b/go-review-counterexamples.jsonl)实际检出三类问题，修复后通过。后续发现首写引用缺口，同样补测修复。主代理沿出处取舍，最终[只读复核记录](../output/s3.4b1/b/review/final-review.json)无待修阻断项。 |

工具环境为现有 macOS arm64、Go1.22.12、Python3.12.12；GOPROXY=off／GOTOOLCHAIN=local只用于测试子进程。必要的python3→已安装3.12映射只在临时测试环境内，退出后移除。race日志的external linker LC_DYSYMTAB警告已保留，测试实际通过；没有把macOS本地结果当成Linux发布架构验收。

中途失败证据保留：新增冷启动夹具最初使用了不存在的类型、随后缺少必需服务声明，修正为完整合成schema4目录后通过；通用载荷保护曾改变既有“审批载荷损坏”错误文案，回归检出后恢复原契约；复核发现的上述实际缺陷均修复并重跑。没有删除失败日志或削弱产品规则换取通过。

**B 增量保全与交接**：

- [产品/测试patch](../output/s3.4b1/b/s3.4b1-b-local.patch)仅包含相对B起点的19个文件，不含前阶段改动、台账或output。SHA-256：`feeaa227d713c4e585f9eceac42afffd734977ca81f73cd2e606784e7928af47`；逐文件和集合指纹见[delta](../output/s3.4b1/b/delta.json)。台账B追加另有[ledger.patch](../output/s3.4b1/b/ledger.patch)。
- [最终保全检查](../output/s3.4b1/b/integrity.json)由[验证脚本](../output/s3.4b1/b/verify_b.py)实际执行：基线中仅本单元授权文件与台账变化；全部A及前阶段证据、网页与原变更保留；台账历史前缀、40个稳定ID和旧1–47迁移保持；增量在独立临时源码树正向应用/反向还原逐字节一致，新增链接和diff --check通过。S3.3c patch SHA `d75c3ed7cbf50884bfe3789ca099b1b1ba02ac01e9da88dabee71584b192d88a`、12文件集合 SHA `772a46a90f113f112a1ac5f27a622d692f87c533661f22503c99815b03a3b9c0` 保持原值。
- **必须保留的后续依赖**：工作登记与新工作准入、操作者/目标授权统一门控、旧计划缺代次阻断、外部执行器接入、删除及ID复用边界、schema48恢复兼容与发布架构验收均未完成。历史计划保持原载荷与现行执行路径；本阶段不得声称停用期间无可执行工作、旧计划已失效或外部停用已可用。
- **回退限制**：有生命周期数据时不能直接降级旧二进制。旧47程序可忽略新列并破坏状态；本轮没有降级SQL、清除正代次或真实数据恢复方案。回撤源码只能按B增量逐块处理，不能覆盖前阶段成果；程序/数据配对回退及生产变更另行批准。
- 治理收尾仅报告候选：README“只增列即可回退”的泛化表述不适用于本合同，发布前需明确修订；schema48备份可验证不代表可恢复；类型字段识别必须区分协议字段与实体ID字典；维护静默早于入队的准入缺口继续保留。未改治理规则、inventory或范围外恢复脚本。
- 本轮测试及重建子进程已结束，临时数据库/映射/回环测试资源按测试清理，无自建长驻服务或浏览器。没有生产/远端连接、真实数据迁移、安装、全局配置、提交、推送、发布、部署或对其他对话发消息。Warp Profile未核验且未用于推导权限。**本地阶段到此停止，不自动接入下一阶段。**

## S3.4b2a：持久化工作准入核心 A 方案（2026-10-04，Asia/Shanghai）

**状态：`partially_completed`；A 合同准备就绪，B 尚未批准、未实施。** 关联 **OPS-07-01 / E02 / 原 S2.2**。40 个稳定 ID、原验收行、S3.4a 已确认语义和 S3.4b1 全部历史保留。当前仅能提出“已登记工作与私有停用互斥”的实施方案；普通发布、其他执行器、目标授权、旧计划阻断、全局无工作证明及外部停用仍未完成。没有运行新迁移或打开任何数据库。

### A 核对来源与适用要求

本轮按仓库 AGENTS、本机已发现 areasong-development 的共同规范／新功能准备路径，以及架构、工程交付、可靠性约定执行。重点是持久化契约、事务并发、幂等、身份所有权、失败保守处理和备份兼容；无 UI、新语言或布局改动，不触发浏览器验收。以下路径以服务根为基准，仓库级文件另标。

- `internal/store/store.go:186`：migrate 逐条事务提交；第48条在本条事务内初始化代次。**“迁移整体回滚”指失败的第49条建表／索引／user_version 全回滚，不承诺从空库或47起步时撤销已经提交的1–48条。** 当前实际迁移数48。保留1–48字节，49不初始化租户、不回填旧工作。
- `internal/store/tenant_lifecycle.go:19/60`：私有 apply→transition 同一 `*sql.Tx` 覆盖审批、目标镜像、CAS、快照、收据及审计；`access_change_apply.go:22` 保留同执行人成功重放。现有 BeginTx 为延迟事务，单实例 `SetMaxOpenConns(1)` 不证明跨连接互斥。
- `tenant_lifecycle_migration.go:32/82`：复用 lifecyclePolicyTx（精确编码／摘要）、tenantRowsTx、lifecycleMirror（规范ID、别名、名称、状态、来源类别），不自造第二套镜像规则。bootstrap/default可作为已知active工作目标，但仍不得私有转换；代次0拒绝。
- `store/plans.go:704` 与 `previews.go:113`：入队事务先查幂等任务，再查已批准计划／preview，最后任务、计划状态、事件及审计同事务；尚无工作登记。`tasks.go:154/277` 的运行认领和终态也尚未登记。
- `runner/plans.go:317–479`：当前顺序为授权／审批检查→定时计划激活→既有任务重放→执行前适配器检查／摘要复算→恢复复验写入（恢复分支）→目标Runner检查→维护静默→任务UUID→StartPlanTaskWithEvent→enqueue。`alerts.go:100` 内真实 CreateSilence 早于任务。
- `runner/engine.go:414` 的 inspect 会创建／删除本地预览目录并调用应用与流量适配器，不能从“inspect”名称推导任意适配器无写入；定时激活、无效计划审计和恢复复验也早于静默。`runner/task.go:16` 在 MarkRunningOwned 之前创建目录和任务合同；`task.go:333` 在终态提交之后才尝试失败计划静默清理。成功计划由 `plans.go:576/671` 收口时解除静默并持久化释放结果。清理失败仅日志不等于安全结束。
- 对应现有回归来源：`store/tenant_lifecycle_{test,atomic_test,migration_test}.go`、`store/control_flow_atomic_test.go`、`store/task_terminal_atomic_test.go`、`runner/control_flow_atomic_test.go`、`store/backup_snapshot_contract_test.go`。本轮没有重跑这些数据库测试；它们不是本阶段新增能力的通过证据。

### B 数据结构候选（未写入产品；未执行 SQL）

建议新增独立 `model.WorkAdmission`／`WorkAdmissionTarget`，不修改 Tenant、ReleasePlan、Task、ApprovalSummary 的旧编码。首个种类只接受 `release_plan_v1`；该种类并不表示发布调用方已经接入。每个工作（kind + plan ID）永久保留一个登记事实，同工作换请求键也拒绝，符合现有单计划单次执行语义。后续执行需新计划和新批准，不自动换键。

候选 migration49 的全部 DDL 如下；由现有 migrate 包裹事务并设置 `PRAGMA user_version = 49`，不在SQL串中嵌套 BEGIN／COMMIT。无 IF NOT EXISTS，遇到冲突结构必须报错，不能掩盖半迁移或外来同名表。

```sql
CREATE TABLE work_admissions (
    id TEXT PRIMARY KEY NOT NULL CHECK (length(id) > 0),
    kind TEXT NOT NULL CHECK (kind = 'release_plan_v1'),
    work_id TEXT NOT NULL CHECK (length(work_id) > 0),
    idempotency_key TEXT NOT NULL UNIQUE CHECK (length(idempotency_key) > 0),
    approval_digest TEXT NOT NULL CHECK (length(approval_digest)=64 AND approval_digest NOT GLOB '*[^0-9a-f]*'),
    actor_hash TEXT NOT NULL CHECK (length(actor_hash)=64 AND actor_hash NOT GLOB '*[^0-9a-f]*'),
    request_digest TEXT NOT NULL CHECK (length(request_digest)=64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
    request_json TEXT NOT NULL CHECK (length(request_json)>0),
    owner_token_hash TEXT NOT NULL CHECK (length(owner_token_hash)=64 AND owner_token_hash NOT GLOB '*[^0-9a-f]*'),
    state TEXT NOT NULL DEFAULT 'admitted' CHECK (state IN ('admitted','preparing','task_bound','uncertain','closed')),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (typeof(revision)='integer' AND revision>0),
    task_id TEXT REFERENCES tasks(id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    closed_at TEXT,
    close_kind TEXT NOT NULL DEFAULT '' CHECK (close_kind IN ('','no_work','settled')),
    close_digest TEXT NOT NULL DEFAULT '' CHECK (close_digest='' OR (length(close_digest)=64 AND close_digest NOT GLOB '*[^0-9a-f]*')),
    close_evidence_json TEXT NOT NULL DEFAULT '',
    UNIQUE(kind,work_id),
    CHECK (task_id IS NULL OR length(task_id)>0),
    CHECK (state NOT IN ('admitted','preparing') OR task_id IS NULL),
    CHECK (state<>'task_bound' OR task_id IS NOT NULL),
    CHECK ((state='closed' AND closed_at IS NOT NULL AND close_kind<>'' AND close_digest<>'' AND close_evidence_json<>'')
        OR (state<>'closed' AND closed_at IS NULL AND close_kind='' AND close_digest='' AND close_evidence_json='')),
    CHECK (close_kind<>'no_work' OR task_id IS NULL),
    CHECK (close_kind<>'settled' OR task_id IS NOT NULL)
);
CREATE UNIQUE INDEX idx_work_admissions_task ON work_admissions(task_id) WHERE task_id IS NOT NULL;
CREATE TABLE work_admission_targets (
    admission_id TEXT NOT NULL REFERENCES work_admissions(id) ON DELETE RESTRICT,
    tenant_id TEXT NOT NULL CHECK (length(tenant_id)>0 AND tenant_id=lower(trim(tenant_id))),
    expected_generation INTEGER NOT NULL CHECK (typeof(expected_generation)='integer' AND expected_generation>0),
    PRIMARY KEY(admission_id,tenant_id)
);
CREATE INDEX idx_work_admission_targets_tenant ON work_admission_targets(tenant_id,admission_id);
```

不加 tenants 外键或级联删除：目标记录是不可变的历史代次证据，现有删除不在本阶段改造；租户存在性在准入事务验证，ID复用/删除另立阶段。task外键与唯一索引保护关联，不创建任务。非空完整目标集合、Unicode规范ID、时间解析、JSON规范编码、摘要一致性及合法状态边由Store执行，不能只靠DDL证明。时间统一沿用 store.now/timeText 的UTC RFC3339Nano；不以时间进行释放。

request_json 固定结构含协议版本1、kind、workId、idempotencyKey、approvalDigest、actorHash、完整 targets[{tenantId,expectedGeneration}]；Store负责规范排序、相同ID同代次去重、同ID不同代次拒绝及SHA-256重算。不能接受空集合、通配租户、缺/零/负代次或自动把不规范ID改成另一身份。owner不进入请求摘要：它是具体执行尝试的能力，非批准事实。ID由可信服务端预生成，原始owner token采用随机256位，仅内存传递，库中存SHA-256，不记录原token或提供HTTP回传。内部读结果不泄露owner token。

Store可以验证传入集合中每个目标当前存在／active／代次匹配／来源和镜像一致，也能校验请求重放、任务关联和状态。**Store不能证明传入集合完整、审批真实、操作者当前权限有效或外部副作用已经结束。** 后续可信Runner必须从批准摘要、有效服务端目录、执行身份与实际目标解析完整集合；不得接受客户端自报集合，不得只登记创建者租户。主体租户的新授权门控仍是另一项未完成依赖。

### 内部接口、状态与所有权合同

候选具体方法如下，均无HTTP／CLI适配；私有事务helper仅同包测试调用，未来接入时才嵌入真实入队事务。

| 方法／输入 | 返回与限制 |
| --- | --- |
| `AdmitWork(ctx, WorkAdmissionInput)` | 输入请求合同、服务端生成AdmissionID和OwnerToken；返回记录及Created。只有首次提交Created=true；重放不返新执行权。读取旧记录先对比完整规范载荷而非仅摘要；同键异actor／摘要／目标／代次或同工作不同键均冲突。 |
| `GetWorkAdmission(ctx, id)` | 内部历史事实读取，无新权限、无续期、无重新准入。读取错误不能解释为无记录或无工作。 |
| `BeginWorkPreparation(ctx, WorkAdmissionMutation)` | 输入id、kind/workId、key、actor、approval/request摘要、owner token、ExpectedRevision与ExpectedState=admitted；CAS admitted→preparing。仅本次CAS真正提交者返回Advanced=true，才可开始外部副作用。响应丢失后重试只返回当前状态／冲突，绝不再给Advanced=true。 |
| `bindRegisteredWorkTaskTx(ctx, tx, mutation)` | 同一现有写事务内 preparing→task_bound，绑定唯一taskId；核验任务plan_id=work_id、plan_digest、actor、idempotency_key、request_hash=HashConfirmation(planID+NUL+approvalDigest)、queued状态及原状态/revision/owner；再次验证完整目标代次与镜像。不得提供“先入队提交再另开事务关联”的公开捷径。B仅合成任务和事务验证此helper。 |
| `MarkWorkUncertain(ctx, mutation)` | admitted/preparing/task_bound→uncertain；完整所有权和CAS条件，保留task及目标。迟到task关联不允许从uncertain转回；不自动恢复。 |
| `CloseRegisteredWork(ctx, WorkAdmissionClose)` | 仅admitted→closed/no_work或task_bound→closed/settled；保存原状态/revision及关闭输入的规范证据JSON和摘要，原时间/目标不改写。其他边拒绝；uncertain及preparing在B不提供解封接口。 |

所有推进事务检查id、工作身份、执行actor、两个摘要、请求键、owner token哈希、预期原状态及revision；UPDATE的WHERE重复这些条件且RowsAffected必须为1，revision+1溢出拒绝。规范JSON严格重算，以防损坏行掩盖载荷变化。taskId必须精确匹配原关联（包括NULL），错误所有者不能取得推进权；actor相同也不代表并发尝试有相同所有权。无租约、心跳转移或自动接管。只有可信进程内协议协调者能持有token；新进程没有旧token时不自动换owner。

no_work：原状态admitted、尚无task，并由同owner确认从未取得preparing执行权；CAS与BeginPreparation竞争，恰好一个成功。证据类型为 `never_started_v1`，绑定工作、身份、摘要、预期revision和NULL任务。零条tasks、HTTP结束、超时、退出都不是此证据。preparing中哪怕后来发现“似乎未发请求”，B也保守留存，允许标uncertain，不冒险释放。

settled：要求绑定task存在且为succeeded/failed/failed_recoverable/rolled_back之一，finished_at有效；needs_attention、paused及未知状态均拒绝。仍须同owner提供 `execution_and_cleanup_settled_v1` 证据：精确task、原revision、结果证据摘要、执行器已停止且不会再产生副作用的确认、所有相关清理已确认完成及清理证据摘要（无清理也要明确记录依据）。**这些外部确认是可信调用方的证明义务，传入布尔值/摘要本身不构成验证。** Store只核验格式、绑定、当前task终态及CAS，B测试只证明合成证据合同；后续必须在执行与清理实际得到确认后才调用。若调用者丢失token、仍有外部请求在途、静默创建结果不明、清理失败或无法核验，一律保持阻断，后续治理另定。

重复关闭：完整关闭载荷、actor、owner及原revision与持久化close_digest相同，返回已有closed事实，不再更新revision、时间或审计；不同原因／task／摘要／owner／原状态拒绝。迟到Begin／bind／uncertain不能覆盖closed；closed请求永久不可再次获准执行。推进返回错误或COMMIT响应不明时只读核对；不得自动重试外部副作用。owner token不是持久化任务runner_owner的替代品；后续进程内协调者向本地runner交接能力须显式实现，远端owner转移不在B。

### SQLite事务互斥位置

建议新增窄helper `beginRegisteredWorkTx`：`db.BeginTx`后，在任何事实SELECT之前执行以下首条DML以获取SQLite写事务；不改变数据行，不触发行级更新触发器，无全局DSN或连接池调整：

```sql
UPDATE tenants SET lifecycle_generation=lifecycle_generation WHERE 0;
```

随后在同一个 `*sql.Tx` 内读最新快照与targets、查幂等事实、写登记主表和完整目标集合并提交；任一目标失败或任何SQL错误均回滚。B必须在实际modernc驱动、两个独立连接上验证此零行DML确实占用写锁，不能以文档推演代替锁证据。失败则停止该方案，不能擅自改全局事务设置。

私有 `applyTenantLifecycleChange` 用该helper替代原BeginTx，成功applied重放仍在原分支返回，不重复检查新状态。新的未执行停用在 `transitionTenantTx` 镜像／代次／绑定校验后、tenants CAS之前调用 `assertNoUnfinishedRegisteredWorkTx`：

```sql
SELECT a.id
FROM work_admission_targets AS t
JOIN work_admissions AS a ON a.id=t.admission_id
WHERE t.tenant_id=? AND a.state<>'closed'
LIMIT 1;
```

只用存在性LIMIT，不把分页列表当完整任务盘点；查询失败即拒绝。检查不按当前代次过滤，历史未结束登记也阻断。方法名与注释明确仅检查已登记工作，不命名为“租户无工作”。启用保持原合同，不因空登记表产生新授权。

串行化证明义务：准入取得写锁并提交→停用取得锁后看到未结束记录而拒绝；停用先提交→准入随后看到disabled/代次变化而拒绝。事务全程持锁、没有先查后另事务写；多目标全在一个事务。SQLite BUSY/取消是显式失败，不能转为成功或空结果。关闭与停用亦用同一边界，只有确认关闭提交之后才能解除已登记阻断。已有其他未接入写路径仍可能运行，因此完全不能推出全局无工作。

### 迁移、恢复与文件变更单元

B窗口：收到本A具体批准后的本地会话，只修改以下文件和自行创建的临时合成库。开始时重新核对基线、保存B起点原字节；发现并行改动先辨认，不重置旧工作区。

| B候选文件（新增者明确标记） | 动作 |
| --- | --- |
| `internal/model/work_admission.go`、`work_admission_test.go`（新增） | 独立值类型、枚举、请求／关闭证据规范化和摘要；不触碰旧计划编码。 |
| `internal/store/work_admission.go`、`work_admission_state.go`（新增） | 上述登记、只读、写锁、CAS、关闭及同事务关联helper；不接真实调用。 |
| `internal/store/schema.go` | 只追加第49条，两张表和两个显式索引，保留1–48。 |
| `internal/store/tenant_lifecycle.go` | 私有入口采用首写锁helper，新增已登记工作阻断；继续包内私有、无实际入口。 |
| `internal/store/work_admission_test.go`、`work_admission_atomic_test.go`、`work_admission_migration_test.go`（新增） | 迁移、重开、状态／身份、多目标、失败回滚、跨连接竞争及旧工作未覆盖证据。 |
| `internal/store/tenant_lifecycle_atomic_test.go`、`tenant_lifecycle_migration_test.go` | 原竞争屏障迁到首写之前／持锁之后，不能再等待两个竞争者同时到达写锁内；保留原成功重放和1→2→3断言。当前版本期望改49，schema48专项夹具仍保留。 |
| `internal/store/backup_snapshot_contract_test.go` | 真实合成49快照、48回归、缺表/列拒绝及49恢复拒绝。 |
| 仓库 `scripts/backup/areasong_ops_snapshot.py`、`scripts/backup/tests/test_areasong_ops_snapshot.py` | BACKUP_SCHEMAS加49，继承48要求并检查两表全部字段；RESTORE_SCHEMAS保留{4,5,45,47}，恢复脚本不改。 |
| `deploy/production-change-packages.md`、`output/s3.4b2a/b/` | 唯一台账追加、B起点与实际增量／测试／独立复核证据。 |

本候选不需修改store.go迁移循环，不引入依赖、定时器、自动清理、自动补偿或持久化owner接管。若实际需要改变上述合同、文件范围或放开不确定记录，重新定界。

旧数据：49只建空登记表，任务/计划/批准摘要/历史快照全不改写，不按当前代次补造旧批准。空表只表示无登记，未登记旧任务绝不被证明结束。重开保持admitted/preparing/task_bound/uncertain，不根据tasks数量或时间自动释放；closed仅可历史重放。

备份与恢复分别记录：48可备份校验但未支持恢复；49实施并验证后才可列备份允许集合，49恢复也未支持。旧48程序虽拒绝49，仍不能概括所有旧程序安全：更旧程序可能忽略新表和代次；禁止有生命周期／准入数据时直接降级，也不清空登记或降user_version。A回退只涉及台账追加；B源码回退按B增量逐块处理，保留旧成果。临时库可随测试清理，真实程序/数据配对回退或恢复须另行设计和批准。

### B验收及独立复核计划（尚未执行）

1. 空库、按实际1–48迁移构建的合成旧48库、47→49链及重复Open；核对默认值、CHECK/FK/唯一索引。对49第二表/索引失败注入，版本保持48且无部分新结构；全部旧表内容、载荷、摘要不变。已提交的48生命周期初始化不误记为49回滚失败。
2. active+正匹配代次成功；disabled、未知/错代次、缺目标、未知来源、镜像/别名冲突拒绝；多目标全成功或全回滚；同ID冲突代次拒绝。默认/bootstrap的已知active工作目标与“不可转换”分开测试。
3. 两个独立Store连接、channel屏障验证首写占锁及两种先提交顺序；竞争方先发起、持锁方显式释放，不用sleep。并发同键最终只有一个准入事实及一次preparing成功权；BUSY仅记失败，后续只读／原键核对，不重发副作用。
4. admitted无任务仍阻断停用；任务终态而未settled仍阻断；关闭之后可停用。错误owner、actor、摘要、工作、task、revision、原状态拒绝；重复关闭只读、迟到响应拒绝、closed旧请求不复活；代次变化后的重放不给新执行权。
5. 准入主表INSERT、每个目标INSERT、preparing CAS、合成task INSERT／关联CAS、uncertain CAS、close CAS以及私有生命周期各原写点分别故障注入；比较全表，无部分任务／登记／状态／审计。COMMIT错误路径至少验证未获执行权；不能只测单表计数。
6. 重开保留全部阻断状态；旧任务无登记仍保留且无“全局空闲”标记。原生命周期1→2→3、原原子性与成功重放保持。AST／调用搜索确认新Store能力未接Runner，私有转换仍无生产调用，HTTP/CLI/web无新增停用入口。
7. 最终相关编辑后执行 `go test -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web -count=1`；新增并发与原生命周期补race。仅测试子进程沿既有Python3.12映射，运行备份校验Python测试并保留恢复允许集合拒绝证据；不装运行时、不修范围外恢复脚本、不启动实际服务或Alertmanager/远端执行器。
8. B验证后默认配置只读子代理、fork_turns="none"，提供原需求、批准单元、实际增量/SQL/事务时序与日志；重点独立核验互斥、完整目标、所有权/关闭、未知副作用、迁移、真实入口隔离和回退限制；不修改、不派生，主代理沿出处复查并修正回验。A没有实施差异，不把A静态检查当B独立复核。

### S3.4b2b最小接线清单（只交接，不在本阶段修改）

1. 新普通发布计划从可信目录解析完整目标，捕获当前正代次，**在审批摘要固定前**绑定；摘要复算沿同一集合，未知旧计划缺代次拒绝新执行，不在执行时补章。目录变化或目标集合变化拒绝；主体／目标授权分别完整核验。
2. 执行先返回经过身份/计划/请求键校验的既有任务重放，不新增授权或副作用；调整当前定时激活早于重放的顺序。新请求基于持久化批准摘要取得准入，再通过一次性的preparing CAS。仅赢得CAS的当前调用可继续；同请求重放只有查询权，准入或CAS响应丢失不能重复创建静默。
3. 准入应在定时激活及适配器inspect等执行相关写入之前建立，最迟必须覆盖最早外部副作用。inspect仍可在获准后做当前摘要复验，失败导致preparing保守阻断/uncertain；不能把适配器读意图当副作用证明。普通分支之外的恢复复验另接，不自动纳入普通发布验收；拒绝审计需区别安全记录与工作启动。
4. prepareMaintenanceSilence受preparing覆盖；新建task UUID宜提前。创建静默响应不明不得重发。创建成功但入队失败：保留阻断，按已存在清理逻辑确认外部状态；B核心不会自动补偿，也不会因“没task”释放。下一阶段如需精确恢复这类悬挂记录，应显式补充证据接口而非绕过核心。
5. 入队事务再次校验批准摘要、执行身份、完整目标代次、owner/revision；task、plan状态、事件、审计与 `bindRegisteredWorkTaskTx` 原子提交。持久化前不要enqueue；Created=false重放不启动新的静默或任务。B当前没有这样的真实接线。
6. 本地enqueue/run须显式交接token和工作引用；任务合同目录写入前核验关联，MarkRunningOwned与准入所有权协同；终态先保留task_bound，待实际执行停止和静默清理确认后再settled。失败清理异常、成功计划观察/收口未完、迟到外部响应均继续阻断。进程重启无自动接管；远端dispatch分支没有接入时不能宣称普通入口所有执行模式覆盖。
7. 启用外部停用前，另行全量盘点未登记旧工作、归属缺失、远端失联、未知副作用和独立目录变化；明确各执行器准入覆盖与权威核对，无法证明即拒绝。已定位的独立入口包括 `runner/engine.go:329/390` preview直启／远端分派、`store/assignments.go:197`认领、`runner/kubernetes_plans.go:226`、`runner/batch.go:715`、`runner/terminal_shell.go:107`、`runner/extension_plans.go:163`、`runner/recovery_center.go:65`、`runner/runner_update.go:145`。这些入口各有独立目标和副作用，未接入共同协议；文件操作等其他类别也仍未覆盖，本A不全面探索。

### A证据、保全与批准请求

起点 `main / c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca`，暂存区为空。阶段目录创建前不存在；[baseline.json](../output/s3.4b2a/a/baseline.json)覆盖1250个既有文件（含旧output和scripts/local）的字节指纹，[source-baseline.zip](../output/s3.4b2a/a/source-baseline.zip)保存源码/台账原字节，旧output原地保留并逐项核验。S3.4b1的19文件逐项匹配：patch SHA-256 `feeaa227d713c4e585f9eceac42afffd734977ca81f73cd2e606784e7928af47`，集合 SHA-256 `055925febc933605818d9f92977de0e1322cd88963d9a6be37b00ccc7421c2a6`。A只追加本台账和本阶段证据。

[static-check.json](../output/s3.4b2a/a/static-check.json)记录实际静态核对；[integrity.json](../output/s3.4b2a/a/integrity.json)记录收尾指纹、40个ID、历史前缀、链接和隔离正反向重建。A产品增量为0，[空增量patch](../output/s3.4b2a/a/s3.4b2a-a-local.patch)和空集合SHA均为 `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`；空patch不声称git apply通过。B起点patch只能在B获批并实施后产生，本次不伪造B证据。

未运行候选SQL、未创建数据库、未启动服务、未连接生产/远端；Warp Profile未知，未用其推导任何权限。没有安装、全局配置、提交推送、发布部署或向其他对话发消息。A检查子进程退出；只保留本阶段必要证据。治理收尾候选仅记录：①inspect的本地写入/适配器调用早于静默；②旧读屏障测试不能直接复用到先取写锁的实现；③48/49备份可校验不等于恢复可用。未改治理文件，未清退历史引用。

**申请一次B变更单元批准：仅按本节文件、migration49、所有权／状态和事务合同实施本地准入核心及合成临时库验证、备份校验兼容、独立只读复核和增量证据。** 备份为B起点源码字节归档；失败回退只撤本阶段增量及自建临时资源；无数据库降级或生产操作。未经该具体批准保持partially_completed，A到此停止，不进入S3.4b2b。

### S3.4b2a B：获批实施与本地交接（2026-10-04，Asia/Shanghai；当前状态）

**`completed`——仅持久化工作准入核心本地完成。** 本段更新上文A退出时的状态，A合同与“当时未批准”的历史原文保持。用户已明确批准本节14文件、schema49、状态／所有权和事务合同及合成验证；没有真实库、生产连接、发布或后续阶段授权。关联OPS-07-01／E02，40个稳定ID及全部原未完成依赖保留。

已实现：

- schema48→49，只追加A所列两张登记表、两个显式索引和约束，SQL逐字匹配批准候选；迁移1–48未改。49不回填任何任务、计划或批准，不初始化代次。既有迁移按条提交：49失败回滚仅覆盖49，不能写成此前1–48也被撤销。
- 独立model和Store实现AdmitWork、GetWorkAdmission、BeginWorkPreparation、MarkWorkUncertain、CloseRegisteredWork及私有同事务任务关联helper。完整目标集合排序去重，冲突代次拒绝；事务内验证目标存在、active、正代次匹配、来源及策略镜像。请求规范JSON及摘要与表字段、目标行交叉核验；同工作换键拒绝，重放不更换owner或产生新执行权。
- 每次推进绑定工作、执行身份、两类摘要、请求键、owner token哈希、task、原state及revision，CAS重复这些条件；只有本次preparing提交成功返回Advanced=true。原token不写库或证据。admitted/no_work仅关闭尚未取得preparing权且无关联任务的登记；task_bound/settled要求精确任务关联、允许终态和可信协调者的执行停止／清理完成证据。preparing／uncertain无关闭、自动过期、接管、补偿或恢复路径；closed永久不能复活。
- 私有生命周期转换在事实读取前取得同一SQLite写边界，在目标CAS前阻断未结束登记（不按当前代次过滤）；成功applied重放保持原事实。首写使用批准的零行DML，没有调整全局DSN／事务配置。`beginRegisteredWorkTx`固定sql.Conn并承载sql.Tx；发现当前modernc Commit错误不自动回滚后，在同一固定连接显式ROLLBACK，清理失败则丢弃连接，防止未提交事务返回池中。这是本单元失败回滚合同的具体落实，未扩大外部操作。
- Runner、普通发布、维护静默、任务执行器、远端分派及HTTP／CLI／网页均未接入；私有转换和关联仍无生产调用。仅已登记工作有互斥证据，空表或旧任务无登记绝不代表系统空闲。

验证与独立复核：

| 验证 | 实际证据与范围 |
| --- | --- |
| B起点保全 | [baseline](../output/s3.4b2a/b/baseline.json)、[原字节归档](../output/s3.4b2a/b/source-baseline.zip)：1258个原文件，48迁移，候选新增文件不存在；main/c0b4150、暂存区为空。A及旧output不覆盖。 |
| 首写锁 | [实际驱动探针](../output/s3.4b2a/b/lock-probe.log)：modernc v1.31.1，零行DML期间独立写连接SQLITE_BUSY，提交后成功；临时磁盘库清理。核心测试再次验证原表内容未改变。 |
| 并发与状态 | `work_admission_atomic_test.go`用两个独立Store及通道屏障，验证准入先提交／停用先提交、同键准入、一次preparing权、no_work关闭竞争、逐写点回滚；不使用sleep猜测。错误身份／owner／摘要／task／状态／revision及未知目标拒绝；多目标全成功或全回滚。 |
| COMMIT失败 | [提交清理定向日志](../output/s3.4b2a/b/go-core-commit-cleanup.jsonl)与最终四包：合成延迟外键使COMMIT失败，Advanced=false，原Store可重用、全表无部分结果，独立重开仍保持admitted。未把此案例扩大为所有连接故障覆盖。 |
| 迁移与重开 | 空库、实际47／48合成旧结构升级49、重复打开；49第二表／索引冲突后保持48且全表历史一致；旧任务／计划不补章，四类未结束状态重开不释放。原生命周期1→2→3、历史成功重放及原子性继续通过。 |
| 指定Go四包 | [go-four-packages.jsonl](../output/s3.4b2a/b/go-four-packages.jsonl)：`go test -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web -count=1 -json`，4包、387个顶层测试、995个含子测试通过事件，0失败。6个原浏览器开关测试跳过；前端未改，不声称浏览器验收。 |
| race | [go-race.jsonl](../output/s3.4b2a/b/go-race.jsonl)：Store／Model的WorkAdmission及TenantLifecycle定向race，2包、41顶层、163个含子测试通过事件，无失败。macOS external链接器警告原样保留，不冒充Linux发行验证。 |
| 备份／恢复边界 | [快照9项](../output/s3.4b2a/b/python-snapshot.log)、[原隔离恢复12项](../output/s3.4b2a/b/python-restore-regression.log)，以及Go真实45/47/48/49迁移快照合同通过。49缺表／列拒绝，48/49恢复模式拒绝；RESTORE_SCHEMAS仍{4,5,45,47}，范围外恢复脚本不改。仅测试子进程使用已有Python3.12，映射退出即清理。 |
| 独立只读复核 | 默认配置、fork_turns=none，[复核记录](../output/s3.4b2a/b/review/final-review.json)核验批准合同、[受审增量](../output/s3.4b2a/b/review/input.patch)、当前14文件指纹、SQL/事务及测试日志；未发现可确证的阻断问题。代理不修改、不派生、不重跑数据库，主代理沿出处抽查。 |

明确未覆盖：普通ROLLBACK失败，以及COMMIT补偿ROLLBACK再次失败时的丢弃分支未做故障注入；busy_timeout=0证明排斥与失败后核对，不证明所有等待／取消时序；未做进程强杀、磁盘故障或提交响应丢失实验。外部执行停止／清理完成仍是后续可信调用方的证明义务，本阶段合成证据不是实际服务验证。上述范围限制不改变preparing／uncertain保守阻断合同。

兼容与后续：schema48和49均可备份校验但尚不支持恢复；有生命周期或准入数据禁止直接降级旧程序。本B没有降级SQL，没有读取现有开发库或生产库。下一阶段普通发布仍需依上文清单，在审批摘要固定前绑定可信目标代次、先处理安全重放、覆盖最早执行相关写入及维护静默、入队事务原子关联、运行所有权交接与执行／清理完成后关闭；其他执行器、主体/目标授权、旧计划缺代次阻断、全局无工作证明及外部停用保持未完成。此处停止，不进入S3.4b2b。

最终增量与保全：

- [B产品／测试patch](../output/s3.4b2a/b/s3.4b2a-b-local.patch)只含相对B起点的14文件，排除前阶段差异、台账自引用和全部output。SHA-256：`86b030f9cf0610b253ed71d9ac903294b387d00249b38214c8c55fc682e8602a`；文件集合SHA-256：`4401d81a992f442db05581799cc7bf3fd44df68396f762103b3588348bb1798f`，逐文件见[delta](../output/s3.4b2a/b/delta.json)。
- [verify_b.py](../output/s3.4b2a/b/verify_b.py)实际核对[最终integrity](../output/s3.4b2a/b/integrity.json)：原文件仅批准范围变化，A／旧证据、网页、Runner及scripts/local保持，台账历史前缀与40个稳定ID保留。产品patch和单独[台账patch](../output/s3.4b2a/b/ledger.patch)在隔离目录正向应用／反向还原逐字节一致；迁移1–48、链接和diff检查通过。临时重建目录清理。
- B起点清单SHA-256：`8eaae81239bd441096027b71f9af3987939ecb6b30652665b59c503669e0a50b`；原字节归档SHA-256：`045c9dc1421d681d27dde02f622b0e0488af89ac7d7de72bcab3adde8062a5cf`。历史S3.4b1 patch仍为`feeaa227d713c4e585f9eceac42afffd734977ca81f73cd2e606784e7928af47`；其原19文件集合指纹`055925febc933605818d9f92977de0e1322cd88963d9a6be37b00ccc7421c2a6`保留为历史基线，不冒充当前已改文件集合。
- 治理收尾仅报告候选：驱动COMMIT失败不能假定自动回滚；首写锁改变旧测试的双读屏障时序；inspect／维护静默／任务终态后的清理都需要后续完整接线。未自动修改治理规则或清退历史引用。
- 已有macOS arm64／Go1.22.12／Python3.12运行时；测试、复核与重建已结束，自建临时数据库／映射按测试清理，无长驻服务或浏览器。无生产／远端连接、真实数据迁移、系统安装、全局配置、提交、推送、发布、部署或向其他对话发消息。Warp Profile未知，未作为权限依据；运行态保持未触及。

## S3.4b2b：普通发布生命周期代次与工作准入接线 A（2026-10-04，Asia/Shanghai）

**状态：partially_completed；A 方案与静态／纯合成证据就绪，B 未批准、未实施。** 关联 OPS-07-01／E02／原 S2.2；40 个稳定 ID、原验收表、S3.4b1 与 S3.4b2a A/B 历史全部保留。本节是待批合同，不是现行实现。当前产品仍为 schema49，普通发布尚未接入；本轮只追加本台账与独立 A 证据，未运行新迁移。

### 取证结论与边界

按适用 AGENTS、areasong-development 的方案准备路径及架构／工程交付／可靠性参考执行。本地任务不读取无关 inventory、不连接生产；Profile 未读取、状态未知，不推导权限。起点 main／c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca、暂存区为空。前阶段 14 文件逐项及其原聚合算法均匹配，包含仓库级备份脚本：

- S3.4b2a B patch：86b030f9cf0610b253ed71d9ac903294b387d00249b38214c8c55fc682e8602a。
- S3.4b2a B 文件集合：4401d81a992f442db05581799cc7bf3fd44df68396f762103b3588348bb1798f。聚合口径为排序后的路径、NUL、文件原字节、NUL；不能换成“路径＋文件哈希”算法。
- [本轮起点](../output/s3.4b2b/a/baseline.json)覆盖 1286 个已有文件；[原字节归档](../output/s3.4b2b/a/source-baseline.zip)保存 817 个源码／文档文件，旧 output 原地逐项指纹保护。网页、scripts/local、先前源码和 A/B 证据均纳入保护。

主代理亲读指定 Model、Store、Runner 及必要直接依赖；只读子代理 /root/release_entry_scope（默认配置、fork_turns=none，无写入／派生）补充调用方映射，主代理抽查 HTTP、批量、恢复、配置和旧入口测试。不是 B 实施后的独立复核。

| 已核实事实 | 来源及其对接线的影响 |
| --- | --- |
| 创建前已有活动 | [CreateReleasePlan](../internal/runner/plans.go:95) 的 inspectForAction 在摘要与计划 ID 形成前；[inspect](../internal/runner/engine.go:414) 创建临时目录、调用应用及可选流量适配器，defer RemoveAll 错误未传播。需独立创建检查协议，不能借未来批准。 |
| 执行前已有活动 | [ExecuteReleasePlan](../internal/runner/plans.go:317) 先定时激活，再任务重放，再 inspect／摘要复验、静默、任务入库。必须前移安全重放与执行权取得。 |
| 摘要格式不一致 | [approvalDigest](../internal/runner/plans.go:778) 输出 sha256: 加 64 位；[CanonicalWorkRequest](../internal/model/work_admission.go:84) 及 schema49 只接受纯 64 位。任务 request_hash 又绑定计划摘要原字符串；不能随手 TrimPrefix 一处接线。 |
| 入队与运行未关联 | [startPlanTask](../internal/store/plans.go:714) 普通事务写任务／计划／事件／审计；[run](../internal/runner/task.go:16) 在 MarkRunningOwned 前创建目录与合同；[enqueue](../internal/runner/engine.go:372) 可绕去远端。 |
| 终态后尚有工作 | [CompleteTaskWithDesired](../internal/store/tasks.go:277) 成功可能变 observing；[completeTask](../internal/runner/task.go:333) 终态提交后才清理失败静默；[CloseReleasePlan](../internal/runner/plans.go:576) 解除静默、持久化、查告警、再 inspect，最后提交收口。 |
| 执行器返回不自动证明结束 | [CommandExecutor.Execute](../internal/runner/executor.go:70) 等待命令，但没有通用子进程／外部异步作业结束证明；超时、断连及迟到回调必须继续阻断。 |
| 目标可能超出发布应用 | [areaforge.sh](../adapters/areaforge.sh:18) 引用共享备份；[backup-postgres.sh](../../../scripts/backup/backup-postgres.sh:14) 列出三个应用数据库，[backup-volumes.sh](../../../scripts/backup/backup-volumes.sh:167) 还涉及 JadeAI、应用卷与控制面备份。Compose 的 inspect／update 又会委派 runtime 中的程序。服务名、adapterRef 或 backup:global 锁均不能证明完整租户集合。 |

### 本阶段范围表与兼容决定（须随 B 批准）

建议首版谓词为：可信 HTTP 人工单对象来源；对象确实来自 Catalog.Services 且 metadata.type=service；启用的 action.Name=update；TargetMode 为 signed_release_tag 或 allowlist 且目标通过原验证；无恢复／自动更新／批量上下文；engine.remoteDispatch=false；目标服务、服务器、全部实际资源与执行实现均能由下述闭包解析器核实。允许显式到期后人工执行的 scheduleAt，不新增自动调度。模板可以是 custom 或 compose-service-v1，模板名本身不授予准入；其他动作不悄悄纳入。

来源采用服务端内部枚举，不接受 HTTP 自报。HTTP 调用新增私有 createManualReleasePlan；现有公开 CreateReleasePlan 的内部旧调用只允许权限校验后的既有计划重放，否则明确返回未接入错误，避免把批量／恢复重试的 update 误分类成人工发布。可用性收缩是明确兼容取舍。

| 计划来源／动作 | 模式与创建→批准→执行→收口入口 | 拟接入或保留方式 |
| --- | --- | --- |
| 人工单应用 update，含显式 scheduleAt | server.createPlan／createManualReleasePlan → ApproveReleasePlan → ExecuteReleasePlan → run／CloseReleasePlan | 上述完整谓词满足才创建 v2；最终 B-R 覆盖创建至实际清理。serverId 非空不代表远端模式。 |
| 人工其他动作：inspect/check/backup/start/stop/restart/rollback 等 | 同 HTTP／ReleasePlan 链 | 首版拒绝新的创建、批准和执行；保留既有记录与安全重放。尤其 check 的发布发现不获本阶段新准入，创建 update 只能读取已持久化的有效发现证据，缺失即拒绝。 |
| 任务恢复 inspect/retry | [CreateRecoveryPlan](../internal/runner/plans.go:787) → CreateReleasePlan | 不是人工来源；不得把旧任务重试改造成 v2 或补当前代次；拒绝共用普通链的新操作，另阶段接入。 |
| 恢复中心 restore/restore-drill | [recovery_center.go](../internal/runner/recovery_center.go:158) → CreateReleasePlan → 普通批准／执行 | 拒绝共用普通链的新操作；恢复点验证、备份恢复合同本身不变，不声称其专属前置步骤获得准入。 |
| 自动更新策略 | [auto_update.go](../internal/runner/auto_update.go:248) → CreateReleasePlan；已有计划可由 HTTP 执行 | 不自动升级来源；拒绝普通链的新建／批准／执行。原策略评估历史保留，其独立评估写入仍未覆盖。 |
| 批量子计划 | [batch.go](../internal/runner/batch.go:1166) → approveBatchChildPlan → ExecuteReleasePlan | 不升级父／子审批；子计划新工作拒绝，父批量的独立状态写入未覆盖。已有任务关联与历史可核对，不重启子任务。 |
| 普通计划远端 dispatch／已分派 Worker | enqueue → dispatchRemote → CreateTaskAssignment；[worker.go](../internal/runner/worker.go:165) 独立执行 | 新格式在创建、执行、分派与领取处均拒绝远端；不传 owner。遗留已分派工作属未覆盖存量，不能接管或宣布结束。 |
| Preview 直启 | Engine.CreatePreview／StartTask → Store.StartTaskWithEvent → enqueue | 当前 HTTP 已移除此入口；仍须拒绝这条链启动新的普通任务，保留身份／键一致的纯任务重放，不能作为 v2 失败后备用路径。 |
| Kubernetes、固定终端／紧急 shell、扩展、Runner／Fleet 更新、Compose revision | 独立计划／会话／更新表及执行器 | 保持现有路径并明确未覆盖；不接生命周期准入，不因共享权限或函数而宣称覆盖。文件操作等未列独立执行器同样未覆盖。 |

本次搜索 cmd 未发现普通计划 CLI 客户端；Web api.ts 的 /api/plans 经 Unix Socket 代理到 /v1/plans。仓库外客户端未知，须适配新格式后另验。未读真实运行配置；仓库示例仅证明存在两类模板及共享目标风险，不证明生产已满足首版谓词。**当前没有已确证的真实同步 profile／可放行应用，生产允许集合按未证实处理；不能据本 A 放行 AreaForge 或 Sub2API。** B 的合成正例不补足真实 profile 审计，真实目录／委派边界需另行核验并批准接入。

### 目标闭包、计划编码与批准合同

1. 新增窄的服务端 releaseScope 声明／解析合同：version=1、mode=local、profileId、resources[]（kind、规范 selector、objectId）、implementationDigests（相关适配器及委派程序 SHA-256）。解析器以受支持的同步执行 profile 为前提，把应用、数据库／卷、备份实际选择器、流量站点／影响范围、维护静默匹配范围映射到已登记对象及租户；再并入创建人所属租户。已解析目录、操作定义、runtime、流量／告警策略、文件内容与委派依赖形成 scopeDigest。声明必须与已核实的实际选择器逐项相等，不能只信一份手写 tenant 列表。
2. 不支持的 profile、任一外部 helper 无法核验、动态环境覆盖与固定目标不一致、全局匹配器无可枚举范围、资源缺归属、缺服务器绑定、镜像／来源／别名异常均在最早活动前拒绝。不能把缺失归属补成 default 或 production；不能凭路径哈希证明行为封闭。共享备份的控制面全库对象若不能映射全部受影响租户，同样拒绝。现有示例不补造资源归属，也不在 B 编辑生产配置；实际适配器／部署映射的完整验收留待后续批准。
3. 创建人来自最新有效 AccessPolicy.Principals；沿原 DefaultTenant 语义解析，再要求存在、active、正代次及生命周期镜像可核验。所有资源租户同理。目标集合排序、去重，同 ID 冲突代次拒绝。创建检查登记事务读取一个一致快照，固定完整集合；最终保存计划再次验证同集合，期间登记阻断私有停用。
4. ApprovalSummary.SchemaVersion=2；末尾新增可选 lifecycle 指针（旧值 nil，omitempty 保持 v1 历史 JSON），其 version=1，含 source=manual_single、executionMode=local、preparationId、creatorTenantId、targetObjects[{objectId,tenantId,serverId}]、targets[{tenantId,expectedGeneration}]、scopeDigest。expectedGeneration 在这个新 JSON 内为规范十进制字符串，严格转换到正 int64，避免 Web 对大于 2^53 代次舍入；准入核心 targets 仍用原 int64。
5. **v2 Plan.Digest 为规范摘要 JSON 的 SHA-256 纯 64 位小写十六进制。** v1 保持 sha256: 前缀和原编码；不改旧摘要／审批。新任务 PlanDigest、准入 approval_digest 同为 v2 原字符串，request_hash 仍为 HashConfirmation(planID + NUL + plan.Digest)，因此不更改 schema49 的摘要约束或伪造旧任务哈希。未知版本、v2 缺字段、错前缀及不规范 JSON 拒绝新写；历史读取不重新编码入库。实际 JSON 字段顺序／摘要由唯一版本化编码器及 golden fixture 固定，不能由 UI 重算。
6. release_plans 继续使用 approval_summary_json，无代次回填列；tasks 不增加 owner 字段。新计划事务要求对应创建检查记录、同一个预留 planID、创建人、请求摘要、scopeDigest 和目标集合，计划＋创建审计＋创建检查关闭原子提交。旧 CreateReleasePlan／Idempotent 不能直接保存一个绕过该证明的 v2。
7. 批准与执行使用已存的同一个不可变集合；每次只复验当前目录／对象归属／正代次及权限，不从现在的目录给旧计划补章。无关全局 RBAC 版本不是计划代次；每次操作的授权凭据可携带当前 policyVersion/digest，Store 事务对比以发现授权检查后的变动，但不将其用作租户代次替代物。
8. 审批人数、C2 历史例外及 AllowsExecutor 原身份规则保留。首版新高风险 update 沿现行两方审批，由创建人执行；不开放换执行人。其他策略若未来允许换人，仍须原 AllowsExecutor 成立且其当前租户在已批准集合内、代次一致，不能临时扩集合。批准人须当前有权且自身租户 active，但其一次批准不追加执行目标；执行及收口仍各按原身份规则，平台管理员不能绕过目标停用。

### 创建 inspect：独立登记方案与候选 migration50

选择独立 release_plan_preparation_v1 协议。权限依据是“当前操作者有权提出该动作并完成受控检查”及服务端已解析请求，不是 release_plan_v1 的批准摘要。保留 schema49 原表／kind／关闭边；新建下述两表，复用固定连接首写锁、生命周期镜像校验、owner 哈希及 CAS 方法，不复用假审批字段。

以下是 B-P 唯一候选第 50 条 SQL，A 未执行。迁移由现有循环单事务设置 user_version=50；不改 1–49，不回填旧计划、任务或代次。

    CREATE TABLE release_plan_preparations (
        id TEXT PRIMARY KEY NOT NULL CHECK (length(id)>0),
        kind TEXT NOT NULL CHECK (kind='release_plan_preparation_v1'),
        plan_id TEXT NOT NULL UNIQUE CHECK (length(plan_id)>0),
        idempotency_key TEXT NOT NULL UNIQUE CHECK (length(idempotency_key)>0),
        actor_hash TEXT NOT NULL CHECK (length(actor_hash)=64 AND actor_hash NOT GLOB '*[^0-9a-f]*'),
        authority_digest TEXT NOT NULL CHECK (length(authority_digest)=64 AND authority_digest NOT GLOB '*[^0-9a-f]*'),
        request_digest TEXT NOT NULL CHECK (length(request_digest)=64 AND request_digest NOT GLOB '*[^0-9a-f]*'),
        request_json TEXT NOT NULL CHECK (length(request_json)>0),
        owner_token_hash TEXT NOT NULL CHECK (length(owner_token_hash)=64 AND owner_token_hash NOT GLOB '*[^0-9a-f]*'),
        state TEXT NOT NULL DEFAULT 'admitted' CHECK (state IN ('admitted','preparing','inspection_done','uncertain','closed')),
        revision INTEGER NOT NULL DEFAULT 1 CHECK (typeof(revision)='integer' AND revision>0),
        result_json TEXT NOT NULL DEFAULT '',
        result_digest TEXT NOT NULL DEFAULT '' CHECK (result_digest='' OR (length(result_digest)=64 AND result_digest NOT GLOB '*[^0-9a-f]*')),
        produced_plan_id TEXT REFERENCES release_plans(id) ON DELETE RESTRICT,
        created_at TEXT NOT NULL,
        updated_at TEXT NOT NULL,
        closed_at TEXT,
        close_kind TEXT NOT NULL DEFAULT '' CHECK (close_kind IN ('','no_work','inspection_settled')),
        close_evidence_json TEXT NOT NULL DEFAULT '',
        close_digest TEXT NOT NULL DEFAULT '' CHECK (close_digest='' OR (length(close_digest)=64 AND close_digest NOT GLOB '*[^0-9a-f]*')),
        CHECK (produced_plan_id IS NULL OR (produced_plan_id=plan_id AND state='closed' AND close_kind='inspection_settled')),
        CHECK ((result_json='' AND result_digest='') OR (length(result_json)>0 AND length(result_digest)=64)),
        CHECK (state<>'inspection_done' OR result_digest<>''),
        CHECK ((state='closed' AND closed_at IS NOT NULL AND close_kind<>'' AND close_evidence_json<>'' AND close_digest<>'')
            OR (state<>'closed' AND closed_at IS NULL AND close_kind='' AND close_evidence_json='' AND close_digest='')),
        CHECK (close_kind<>'no_work' OR (produced_plan_id IS NULL AND result_digest='')),
        CHECK (close_kind<>'inspection_settled' OR result_digest<>'')
    );
    CREATE TABLE release_plan_preparation_targets (
        preparation_id TEXT NOT NULL REFERENCES release_plan_preparations(id) ON DELETE RESTRICT,
        tenant_id TEXT NOT NULL CHECK (length(tenant_id)>0 AND tenant_id=lower(trim(tenant_id))),
        expected_generation INTEGER NOT NULL CHECK (typeof(expected_generation)='integer' AND expected_generation>0),
        PRIMARY KEY(preparation_id,tenant_id)
    );
    CREATE INDEX idx_release_plan_preparation_targets_tenant
        ON release_plan_preparation_targets(tenant_id,preparation_id);

请求 JSON 规范化绑定协议／kind、预留 planID、键、actor、权限依据、source/mode、目标版本／时间、scopeDigest、targetObjects、完整代次集合；authority_digest 只摘要权限依据投影。owner 为独立随机 256 位能力，只有哈希入库，绝不进请求／任务合同／日志／HTTP／审计。相同键重放返回原事实；换 actor／载荷冲突；预留 planID 不能换键重做。plan_id 是预留工作标识，不伪造一条已批准计划。

拟增 AdmitPlanPreparation、BeginPlanInspection、RecordPlanInspectionDone、FinishPreparedReleasePlan、ClosePlanPreparationNoWork、MarkPlanPreparationUncertain；内部同 owner、原状态／revision 和全部绑定条件 CAS。只有本次 admitted→preparing 提交成功的调用得到一次检查权。inspection_done 必须有实际执行完成和清理收据，仍阻断停用；成功时保存新计划及审计并关闭，已证明安全的检查失败则以失败结果关闭、不造失败任务。只允许 admitted→closed/no_work、preparing→inspection_done→closed/inspection_settled；uncertain 无出口，没有 preparing→closed 捷径。

检查顺序：权限／输入／安全历史重放 → 纯解析与读取已存发现证据 → 多目标一致快照与登记 → 本次 preparing 提交成功 → 创建临时目录 → 应用 inspect／可选流量 inspect → 等待全部已发调用收敛 → 显式清理并确认目录消失 → 保存检查收据 → 重新校验绑定 → 摘要固定、计划／审计／关闭同事务。清理错误不能被 defer 丢掉。响应不明、未知后继、丢 owner、进程退出或迟到结果保持 preparing/uncertain，不重新检查，不按时间释放。

完成收据来自受支持的同步 profile、已核实的委派边界、每次调用的开始／返回／等待结果、临时目录清理结果与作用域指纹；不是 Executor.Execute 返回 nil、一个布尔值或零个本地 goroutine。对默认 CommandExecutor，单独 command.Run 成功不够；未知外部 updater、脱离进程的工作、无法核验的实际目标均不能签发收据。B 只用完整可控执行器证明该协议；真实实现的缺口保持拒绝，不把合成收据冒充生产证明。

私有 assertNoUnfinishedRegisteredWorkTx 增加同事务对 preparation_targets JOIN preparations、state!='closed' 的存在性检查，与原 work_admissions 检查共同阻断；不按代次过滤，任一查询失败即拒绝。外部停用仍关闭；无新增公开转换方法。新增表检查与原首写锁相同，保留 COMMIT 失败在固定连接 ROLLBACK／清理失败丢弃连接的处理。

### 已批准执行、入队与本地所有权（B-R，待另批）

1. 先验证请求形状、当前访问权限和原执行身份，再按持久化任务匹配 planID、actor、键、PlanDigest、request_hash，安全返回既有任务；不要求重放重新取得代次或执行权。仅 plan.State 判定不足。不存在任务才进入新执行；未知／缺代次／v1／不支持来源或模式直接拒绝并提示重新提案，保留历史批准事实。
2. 准入前可做纯 JSON／摘要／目录／权限／目标／发现证据／到期时间及数据库事实读取。安全拒绝审计是独立记录，不触发计划执行；授权失败不能借机 invalidation。计划失效若需写入按明确校验结果处理，不把拒绝新执行伪造为任务失败。
3. 新增 Store.AdmitReleaseWork，首写锁事务核验持久化 v2 摘要、创建证明、完整目标、原审批身份和最新授权快照后，复用内部准入写入 helper。不能在 Runner 校验后调用一个只信客户端 plan 对象的入队 API。原 AdmitWork 保留核心合同；它本身不能取得 Runner lease 或绕过新入队校验。
4. 先预生成 task UUID 和进程内 owner。仅 Admit Created=true 的协调者进入 BeginWorkPreparation；只有本次 Advanced=true 才能执行后续操作。失败或结果不明只读核对、不重新执行。尚未取得执行权且已证明无活动时可原 owner no_work 关闭；preparing 后的复验／激活／检查失败维持 preparing，无法确认则 uncertain，首版不扩展其关闭边。
5. preparing 之后才可定时激活、执行 inspect、当前摘要复验、目标 Runner 复验、维护静默查询／创建。摘要必须带原代次重算，不补当前代次。创建静默发生一次，未知响应不重发；已知静默 ID 而入队失败的既有清理也须受原 owner 保护，失败仍阻断，不能“没有 task 所以结束”。
6. 新 StartRegisteredPlanTaskWithEvent 使用 beginRegisteredWorkTx，同一事务重新读取计划／授权快照、审批、完整目标与代次，写 tasks、更新计划／静默关联、写 queued event 与 audit，调用原 bindRegisteredWorkTaskTx 后提交。返回 Created=true 且提交确认后才 enqueue；COMMIT 失败不留部分关联，不自动再次启动。旧 StartPlanTask／WithEvent、CreateTask 相关普通入口必须对新格式或已登记工作拒绝降级，也不能启动缺代次旧普通计划。
7. 私有 releaseExecutionLease 绑定 admissionID、kind/workID、taskID、actor、两类摘要、键、scopeDigest 及 owner。协调者把它作为非序列化参数交给 enqueueRegistered/runRegistered；Engine 的有锁表只做 insert-if-absent／同引用比较，不能以 taskID 覆盖另一请求的 owner。任务结果、持久化 admission、runner_owner 都不能重建 token；不放入 model.Task（即使 json:"-" 也不新增 token 字段）。
8. run 在创建目录／合同前，先独占该 lease 并核对已持久化 task_bound、任务与全部绑定；新的 MarkRegisteredRunningOwned 在同一固定连接写事务校验准入能力再更新 queued→running，仍用 engine.owner 写 runner_owner，二者职责分开。失权时不调用 failBeforeRun 伪造另一人的任务失败。旧 MarkRunningOwned／远端分派与领取不能把 v2 绕回无准入路径。
9. 后续 phase、回滚、终态及清理由同 lease 协调。现行动作定义内的已证明收敛失败仍沿原 rollback 语义；若调用可能仍在途，不叠加新的自动回滚／补偿来掩盖未知，记录 needs_attention/uncertain 并保留阻断。不新增租约、自动取消、接管或恢复线程。

### 实际结束、观察与清理合同

run 返回、终态提交、观察期与清理分别记录。run 的 finally 收集每次调用结果；通知并等待 heartbeat 退出，释放本地资源锁；所有外部／适配器回调进入同一 lease 的阶段检查。任务目录／合同与备份产物按既有历史留存，不因“清理”删除；结束证明要求没有未来写者。检查临时目录必须实际删除成功。

成功且有观察期：任务 succeeded、计划 observing，登记仍 task_bound；原 owner 能力留在进程内。CloseReleasePlan 必须先取该 lease 的独占收口权，再沿原创建者身份／当前权限、观察时间、持久化任务检查，处理静默解除及确认、持久化释放收据、告警检查、收口 inspect 与临时目录清理、身份复验。无阻断才提交计划收口。零观察期属于候选支持范围：B-R 对 v2 成功任务也先原子写 plan.observing，开始／结束时间均为终态提交时刻；由 run 已收敛后的同 owner finalizer 走适用收口／清理，再原子提交 completed＋登记关闭。v1 读取不改写，不走其旧 Completed 快捷分支来完成新 v2。

收口尝试按 lease 内的关闭请求键串行化并缓存已结束结果。同键只返回原结果；新的有意尝试仅在前次已经确认结束、没有在途／不明副作用时允许。现有 [api.closePlan](../web/src/api.ts:358) 永久复用 planID 的键，B-R 必须同步最小修改：保留现有 error 文案，仅 /close 增加 code=plan_closure_blocked、attemptId、attemptState=finished、newAttemptAllowed=true 的明确阻断响应；需要的 blocker 审计成功后才可返回此凭据。进行中／响应不明／持久化失败／失权不返回该许可。APIError.payload 已保留响应，api.ts 仅在匹配本次键的明确结束阻断时清除键，App.tsx 提示可再次人工收口，下一次用户点击才建新键；网络错误、无效响应和未知结果保留旧键，不自动重发。进程重启无 lease 时只读原事实或拒绝，不利用旧审计重新颁发收口能力。

静默采用精确 ID：新增只读确认方法核验 DELETE 后已经 expired／确实不存在，再保存释放事实及审计；HTTP 成功但持久化失败不等于完成。若仍有原 owner、明确只失败在本地提交，可在同一关闭尝试内只读确认并补交本地记录，不重发外部写；响应不明或调用方丢失能力则留阻断。不得把 EndsAt、自动到期或“当前无告警”当作解除证据。

最后仅在执行器与后继调用均已收敛、心跳／回调停止、观察和收口检查结束、所有相关清理及其持久化确认完成时，构造实际结果／清理收据摘要调用 settled。普通成功收口建议新 ClosePlanAndRegisteredWork 复用私有同事务关闭 helper，把 plan.completed、收口审计和 task_bound→closed 原子提交。不提前提交计划再靠另一请求猜测清理；COMMIT 响应丢失可只读返回完成事实，不再调用 inspect／静默。

| 场景 | 登记及后续行为 |
| --- | --- |
| preparing 后当前摘要／环境复验失败 | 原执行不进入任务；保持 preparing 或 uncertain，不能假定 no_work。 |
| 创建静默响应丢失／不明；入队事务失败 | 不重建静默／任务；原 owner 可保守标 uncertain，记录已知事实，不能自动释放。 |
| 任务终态事务失败 | 不宣告执行完成、不重跑任务，保留 task_bound；未知则 uncertain。completeTask 的错误须传播到 finalizer，不能仅打日志。 |
| succeeded 但 observing／收口存在告警 | 保持 task_bound；可在同进程、同 owner 下用新的明确收口尝试复验；重放同一个关闭请求不得重复外部调用。 |
| failed／failed_recoverable／rolled_back，已确认收敛与清理 | task 与计划保留真实终态／needs_attention；仅登记可按原 settled 关闭，不冒充发布成功。 |
| Task.State=needs_attention／paused／未知终态，或清理失败 | 原核心不允许 settled；持续阻断，不新增解封边。仅 Plan.State=needs_attention 不排除上一行已确认安全的失败收尾。 |
| 静默解除成功但本地持久化失败 | 保留阻断；保留原调用收据，不重发 DELETE；失去收据／owner 则不接管。 |
| 迟到调用／关闭后回调 | phase／revision 与 owner 门控拒绝；有任何未完成调用就不得先关登记。禁止 closed 复活。 |
| 进程重启、owner 丢失、未关联旧任务 | 只读历史／返回已有任务，拒绝 run、收口 inspect 或清理接管；不重新颁发 token。 |
| 旧计划无代次且无既有任务 | 新批准／执行拒绝，提示重新提案；不补章、不清批准、不伪造 task.failed。 |
| 旧任务或 closed 工作安全重放 | 原权限允许且身份／计划／键／摘要匹配时返回原记录；不重新入队。禁止旧任务恢复快捷路径把“重放”变成新执行。 |

权限检查与 lifecycle 检查是两条边：HTTP／Runner 沿当前 RBAC 检查操作者，Store 在关键事务内核验这次授权所对应快照及主体归属、全部目标 active／代次；平台角色也受目标检查。执行内部已经启动的必要清理沿同 owner 既有责任完成，不因角色撤销把清理丢给一个新主体；任何新人工执行／收口仍须当前权限。历史读取范围不扩展。

### 具体 B 拆分、文件与验收

完整链涉及创建前登记、两种计划编码、公共 Store 拒绝旧路、进程内能力和任务后的清理，按约 18k 上下文上限拆为两个紧邻且独立核验的单元。**本次只申请 B-P；B-R 必须另批。B-P 结束仍是部分完成、不可发布中间态，不宣称普通发布全链完成。**

| 单元 | 具体文件及动作（均相对服务目录，仓库脚本另标） |
| --- | --- |
| B-P：创建检查／新计划合同／兼容屏障 | 新 internal/model/release_lifecycle.go、plan_preparation.go 及对应测试；model.go 的可选 lifecycle/releaseScope 字段；internal/config/config.go 及定向测试校验声明。新 internal/store/plan_preparation.go、plan_preparation_state.go、release_lifecycle.go 及迁移／原子性测试；schema.go 仅追加50；tenant_lifecycle.go 中登记检查扩到两类；plans.go 增新格式保存／批准门控。 |
| B-P：Runner 与消费者 | 新 internal/runner/release_scope.go、plan_preparation.go、release_lifecycle.go 及测试；plans.go、engine.go、server.go 接人工创建／明确来源与拒绝旧新工作；executor.go 只补可核验检查结果／收敛证据接口，不把未知实现升级成可信；store/plans.go、previews.go、tasks.go、assignments.go 和 runner/worker.go 增新格式／普通旧任务新启动拒绝屏障。Runner 与 Store 的 CloseReleasePlan 同时封住新的收口活动，仅保留已完成记录的身份／键一致纯重放；不得在 B-R 前调用旧 observing 计划的静默解除或 inspect。 |
| B-P：网页与备份 | web/src/types.ts、components/ConfirmationDialog.tsx 仅展示完整目标代次／新旧计划限制与必要错误提示；沿现有中文文案和布局，不加停用按钮。internal/store/backup_snapshot_contract_test.go、schema 版本受影响的 migration tests，以及仓库 scripts/backup/areasong_ops_snapshot.py 和对应 tests：备份校验加50及两表全部字段；RESTORE_SCHEMAS 仍 {4,5,45,47}。不修改生产 catalog 或恢复脚本。 |
| B-R：获批执行至收口完整链 | 新 internal/runner/release_execution.go、release_settlement.go 及对应测试；plans.go、engine.go、task.go、alerts.go、alertmanager.go、executor.go、traffic_lifecycle.go、server.go 的 /close 结构化错误；Store 新 release_execution.go／tests，plans.go、tasks.go、work_admission.go、work_admission_state.go 提取同事务 helper，复用 bindRegisteredWorkTaskTx；assignments.go／worker.go 保持拒绝新格式远端。web/src/api.ts、App.tsx、types.ts 及对应测试最小适配收口键与结果语义。必要旧 fixture／测试改为明确旧拒绝或受支持 v2，不削弱审批与原子性断言。B-R 计划不新增 schema；确需新增必须另列 SQL 再批。 |
| 两单元证据 | 唯一 production-change-packages.md 追加实际交接；output/s3.4b2b/ 下分别保存新的单元起点、日志、增量、指纹和复核。开始前核对并行改动，不覆盖 A 或旧证据。 |

B-P 的对外普通执行及新收口活动保持关闭，包括新 v2 与旧 observing 计划；无有效关联／本次 owner 时在静默或 inspect 前拒绝。B-R 必须连同观察／实际清理全部完成后才放行支持谓词。旧 observing 计划不因此被取消、清理或伪造终态，其后续处置需另批，不能将这个中间态部署到生产。原独立执行器不因本中间态被声明已迁移。B-P 的能力收缩包含人工非 update、缺可信 profile 的应用、自动更新、批量子计划、恢复和远端普通计划；用户批准需包含这些兼容影响，不把它称作无破坏兼容升级。

适用验收：公共 JSON／Store 契约、主体及多目标 lifecycle 门控、一次性 CAS、事务失败／COMMIT 清理、外部不确定性、资源与心跳生命周期、既有身份规则及历史读取。网页只验证新增阅读信息、长代次、现有主题／窄屏和关键键盘操作；不扩语言或重做风格。

- B-P：真实本地 HTTP／Store＋临时 SQLite＋可控检查执行器，验证服务端目标集合、共享目标遗漏／未知来源拒绝、摘要固定前取得代次、最大 int64 无损、1→2→3 后旧计划拒绝、创建重放与并发只检查一次、inspect 临时目录清理失败／迟到响应、权限和停用双连接竞争、逐 SQL／COMMIT 故障及 plan＋审计＋preparation 关闭原子性。验证旧摘要／批准不改、v2 直接 Store 保存／旧入队／远端均拒绝；旧 observing /close 的静默／inspect 调用计数均为零，已 completed 的合法关闭重放不写入。新50迁移失败只回滚50，旧1–49和历史不变；合成50备份可校验但恢复拒绝。
- B-R：同一真实产品 HTTP／Store 链配可控 Executor 和回环 Alertmanager，调用计数／持久化记录／channel 屏障证明：并发同键仅一个 preparing 权；重放／响应丢失不重复 inspect、静默或入队；入队故障无部分任务关联；run 目录前验权；不同 owner 不覆盖；终态后观察／清理继续阻断；清理确认及持久化后才 settled；关闭后迟到回调不能复活；失权／重启不接管；原审批人数、身份、摘要、幂等与历史读取回归。另验证零观察期清理前不能 completed／closed、任务 failed 但计划 needs_attention 的合法 settled，以及网页“明确告警阻断→告警解除→人工新尝试成功”和“丢响应→原键重放零重复调用”。
- 不用 sleep 猜竞争；时钟与观察边界通过现有可控时钟／同步屏障构造。仅合成执行器／Alertmanager 证明调用序列和不确定性处理，真实产品链证明 HTTP／Store／Runner 接线；都不能证明生产 adapter／目标 profile 已验收。默认 CommandExecutor 的未知子进程／外部异步收敛仍须拒绝或另阶段证明。
- 每单元最后跑 go test -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web -count=1 -json；相关准入／计划／终态／生命周期 race；有前端改动则跑其现有类型／测试／构建及必要页面验证。沿既有工具链与测试子进程 Python3.12 映射，不安装、不改全局。受批准兼容收缩影响的原成功用例保留原身份／摘要断言，并改为对应拒绝用例或 v2 夹具，不简单删测试。
- 两单元验证后分别安排默认配置、fork_turns=none 的只读独立复核，提供该单元批准、实际差异、顺序／状态表、日志与未覆盖项；检查最早活动、同一目标来源、Store 降级、owner 泄漏／接管、终态后行为和不确定释放；不修改、不派生。主代理沿出处核验、修正并回验。

### A 实际验证、回退与退出

[Model 探针](../output/s3.4b2b/a/model-probe.json)实际使用现行 Model，确认 v1 前缀摘要被准入拒绝、纯64位接受、请求不含 owner，Task 无 OwnerToken 字段；Go AST 确认49条迁移。候选字符串代次的 int64 最大值往返／变更摘要检查只证明编码样例，不是 v2 产品实现。[定向 Model 日志](../output/s3.4b2b/a/model-tests.jsonl)为 Go1.22.12 本机回归，16 个含子测试通过事件、0失败；没有跑 Store／Runner 数据库或适配器测试、race、前端或新迁移，不能记为 B 验收。

[A 方案只读复核](../output/s3.4b2b/a/plan-review.json)指出旧 observing 收口门禁的生效单元、网页关闭键重用、Task／Plan 的 needs_attention 区分、零观察期提交状态四处缺口；主代理沿实际调用方核对并补入本节，复查确认四项在方案层面解决，未发现新增合同矛盾。错误响应校验、并发关闭、owner、真实清理仍待 B 实现／运行验证；不将此记录作为 B 独立实施复核。

[静态核验](../output/s3.4b2b/a/static-check.json)和[保全／重建结果](../output/s3.4b2b/a/integrity.json)列出真实调用顺序、当前 schema、历史指纹、40个ID、引用检查及台账隔离正反向重建。[本轮台账增量](../output/s3.4b2b/a/ledger.patch)相对 A 起点；[产品空增量](../output/s3.4b2b/a/s3.4b2b-a-local.patch)及空集合 SHA-256 均为 e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855，不对空patch声称 git apply 成功。完整指纹见 [delta](../output/s3.4b2b/a/delta.json)。真正相对 B 起点的产品 patch 只能在 B 获批、重新记录当时起点并实施后生成，本轮不伪造 B 证据。

A 回退只撤本次台账追加及自建证据；B-P 源码回退仅按其独立增量，保留前阶段所有改动。schema48／49以及候选50恢复兼容仍未完成；有任何生命周期／准入／检查登记数据都禁止直接降级旧程序，不删除登记、不降 user_version。50只新增表不等于旧程序可安全运行。后续真实程序／数据恢复、安装、发布及生产部署单独批准。

治理短检查仅记录候选：摘要前缀不一致、inspect 清理错误被忽略、共享备份目标不能按单服务推定、任务终态后仍有静默／检查、serverId不等于远端模式。没有改治理规则或清退历史引用。S3.4b1 当时19文件指纹与旧 A/B 当时状态是历史口径，未改写为当前集合；此前已接受的历史截图缺失例外未扩大。本阶段未发现需要本轮改动的失效引用。

**待批准 B-P 变更单元：** 收到本节具体批准后的本地窗口内，按上述文件实现独立创建检查、schema50、新计划v2及兼容拒绝屏障、最小计划阅读信息、合成验证／独立复核／增量证据。备份是该窗口新的源码起点归档；失败只处理该单元增量和自建临时资源，真实库不迁移或降级。仅 B-P，不包含 B-R、真实适配器／外部服务、生产配置、安装、提交、推送、发布、部署或开放租户停用。本次 A 到此停止。

### S3.4b2b B-P：获批实施与本地交接（2026-10-04，Asia/Shanghai）

**B-P 状态：completed——仅独立创建检查、v2 合同与兼容拒绝屏障本地完成。普通发布生命周期整体仍为 partially_completed；B-R 未批准、未实施，当前是不可发布中间态。** 用户已明确批准本单元、候选50及其兼容收缩。本段更新当前实绩，保留上文 A 的当时状态、全部历史正文及40个稳定 ID；关联 OPS-07-01／E02，不关闭其整体或生产验收。

实际实现：

- schema49→50 只追加上文批准的两张 preparation 表及索引，SQL逐字匹配；迁移1–49及原三份工作准入核心文件保持原字节。50不回填旧计划、任务或代次。只在本轮自行创建的临时库验证，未打开现有开发库或生产库试迁移。
- [release_scope.go](../internal/runner/release_scope.go:57) 把主服务及资源对象映射到可信目录，绑定完整对象／租户／服务器集合、声明、动作和实现指纹；额外纳入不参与 ServiceDefinition JSON 的 AdapterContractVersion。声明与哈希本身不授予准入：私有 profile 能力仅有测试实现，默认 CommandExecutor 无该能力，真实允许集合为空。
- [v2 模型](../internal/model/release_lifecycle.go) 固定完整代次集合、来源、模式和作用域；JSON代次为规范十进制字符串，内部严格转正int64。v2摘要为纯64位SHA-256，v1保留原前缀／编码，黄金摘要及最大int64往返已验证。人工创建在摘要／请求固定前把 scheduleAt 规范到UTC，等价时区同键重放且批准后回读一致。
- [创建协调者](../internal/runner/plan_preparation.go:32) 在临时目录和检查调用前完成权限、闭包与代次捕获、独立登记和本次 preparing 提交。权限依据不借未来批准，Store在关键事务复验最新权限快照、操作者状态、对象权限和完整目标代次。只有本次提交获得一次检查权；owner原值只在进程内，任务、HTTP、审计、请求与关闭JSON均不携带它。
- 应用／流量检查必须有实际收敛信号；失败结果也检查 OK，不把 Err=nil 当作检查成功。显式清理并确认目录消失后才能登记 inspection_done；[最终事务](../internal/store/plan_preparation_state.go:136) 原子保存计划、创建审计和检查关闭。检查失败不造任务／计划；不确定、清理失败、撤权导致最终提交拒绝或丢失owner均保留阻断，不自动释放、接管或补偿。
- 私有生命周期转换在同一首写锁事务中检查原 work_admissions 和新 preparations，任何未关闭登记均阻断；原核心的固定连接COMMIT失败回滚／丢弃连接处理未改，外部停用仍关闭。
- [普通入口](../internal/runner/release_lifecycle.go) 与Store拒绝新执行、新收口、定时激活、Preview直启和远端分派／领取；旧observing不解除静默、不inspect。身份、键、摘要及关联匹配的既有任务／已完成关闭可纯重放；内部来源不能借人工v2的请求键继续批量批准或自动关联。新HTTP人工update创建与批准仅在已证明的profile下成立，本轮正例使用合成profile。
- 新发现的同范围调用点也落实拒绝边界：auto_update_observation.go／auto_update_receipt.go 不启动观察回滚或补交旧回执，store/auto_update_observation.go 不重新认领回滚，store/maintenance.go 不凭重启伪造旧任务终态。auto_update.go 的陈旧计划分支改为只记录独立评估拒绝，不再清空旧批准。这些补充文件对应已批准的旧工作／自动来源约束，没有接入新的执行器。
- 原执行实现保留为当前普通公共入口不可达的私有方法，未把它们或测试开关用作放行旧链的捷径。历史准备改为明确的测试夹具；原需要新执行成功的用例按批准后的拒绝语义调整，身份、摘要、读取与原子性断言保留。批量父级状态、策略评估记录及其他独立执行器仍属未覆盖范围，不能扩大成“整个系统冻结／无工作”。
- 网页仅在 ConfirmationDialog／types 增加目标代次、对象、作用域摘要与阶段限制；v2可审阅批准，执行／新收口按钮禁用，旧计划提示重新提案。未新增停用按钮、语言或设计体系。

实际验证（日志属于本地合成环境，不代表真实profile或生产验收）：

| 检查 | 证据与结果 |
| --- | --- |
| 最终Go四包 | [go-four-packages-review-followup.jsonl](../output/s3.4b2b/b-p/checks/go-four-packages-review-followup.jsonl)：runner／store／model／web，414个顶层测试、1068个含子测试通过事件，0失败；6个既有可选浏览器开关跳过，不冒充这些旧网页复验。 |
| 配置与编码 | [go-config.jsonl](../output/s3.4b2b/b-p/checks/go-config.jsonl) 28顶层、55通过事件；新声明只证明结构，未知真实profile仍拒绝。Model覆盖v1兼容、v2黄金摘要、完整集合、正代次／溢出及JSON往返。 |
| 事务、迁移与生命周期 | plan_preparation_test／atomic_test验证逐写点与COMMIT故障、全表回滚、50表／索引冲突、重开保留、双连接私有停用竞争、no_work与开始检查互斥、同owner关闭重放及迟到CAS拒绝。原1→2→3、摘要、身份及终态事务回归仍通过。 |
| 当前权限及迟到完成 | [补充race](../output/s3.4b2b/b-p/checks/go-review-followup-race.jsonl) 9通过事件：真实SaveAccessPolicySnapshot＋双Store覆盖撤权/准入/最终提交先后顺序；检查中撤权不生成计划；真实异步goroutine在取消后写原目录并结束，登记/revision/结果/关联保持uncertain；另验证旧批准保护和等价时区。 |
| 相关race | [Store race](../output/s3.4b2b/b-p/checks/go-race-store.jsonl) 55顶层、190通过事件；[Runner／Model来源门禁](../output/s3.4b2b/b-p/checks/go-race-origin-fence.jsonl) 37顶层、58通过事件；[回环HTTP race](../output/s3.4b2b/b-p/checks/go-http-race.jsonl) 1项通过。均0失败。原三分钟Store race在合成迁移构造时超时，日志保留；按实测成本分包、600秒有界预算重跑，实际506.9秒通过，未删断言或延长竞争sleep。 |
| 产品HTTP链 | TestPreparationLoopbackHTTPContract实际监听本机回环HTTP，走产品路由／Runner／临时SQLite，验证创建、重放、独立批准、执行与收口拒绝；另有响应丢失、COMMIT故障、并发一次性检查、清理失败及未接入来源借键负例。身份与执行器为受控合成输入。 |
| 前端与页面 | [57项前端测试](../output/s3.4b2b/b-p/checks/web-test.log)、[类型与构建](../output/s3.4b2b/b-p/checks/web-build.log)、[定向lint](../output/s3.4b2b/b-p/checks/web-lint.log)通过；[页面结果](../output/s3.4b2b/b-p/browser/result.json)及[桌面](../output/s3.4b2b/b-p/browser/review-desktop.png)／[390px](../output/s3.4b2b/b-p/browser/review-narrow.png)／[深色偏好](../output/s3.4b2b/b-p/browser/review-dark.png)验证长ID/代次不溢出、键盘批准和执行／收口禁用。页面使用真实React组件＋合成页面，未冒充完整网页HTTP集成。 |
| 备份及恢复边界 | [Python快照10项](../output/s3.4b2b/b-p/checks/python-snapshot.log)及Go真实45/47/48/49/50迁移快照合同通过。50缺表／列拒绝，48/49/50恢复仍拒绝，RESTORE_SCHEMAS保持{4,5,45,47}；未修改恢复脚本或扩大恢复允许集合。 |
| 独立只读复核 | 默认配置、fork_turns=none，/root/bp_final_review按[首轮增量](../output/s3.4b2b/b-p/review/input.patch)及[最终增量](../output/s3.4b2b/b-p/review/followup-input.patch)、原始批准、状态/顺序与日志核验。[复核记录](../output/s3.4b2b/b-p/review/result.json)：首轮两项证据缺口已补齐，最新61文件指纹一致，未发现可确证阻断，本B-P本地范围可交付。代理无修改／运行／派生，主代理核对并回验。 |

最终增量与保全：

- [B-P产品／测试patch](../output/s3.4b2b/b-p/s3.4b2b-b-p-local.patch)相对B-P起点，共61文件，排除前阶段、台账与output差异；SHA-256：ee3cefe4a44eedea1c9e16201c0e8f666db7e5da52806b2de44196257a078427。
- 完整文件集合SHA-256：50a384e96dbbb491bbfb7b067a551d21fa348fbe26cd1ec06924eb6dd8ec5498；逐文件见[delta.json](../output/s3.4b2b/b-p/delta.json)。[台账patch](../output/s3.4b2b/b-p/ledger.patch)单独保存；[verify_bp.py](../output/s3.4b2b/b-p/verify_bp.py)实际执行源码／台账隔离正向应用和反向还原逐字节检查，结果见[integrity.json](../output/s3.4b2b/b-p/integrity.json)，临时重建目录已清理。
- [B-P起点](../output/s3.4b2b/b-p/baseline.json)1300文件，SHA-256：ae0dc6259d995403e2e8bd47e1ff1f1de59264ae4bfc5133cfb077371250f655；[原字节归档](../output/s3.4b2b/b-p/source-baseline.zip)SHA-256：d84f3e455898eacd80688c9e663e71ae22fa0cd451673b64050a569263870b40。起点main／c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca，暂存区为空。
- A及此前output、未涉及的前阶段源码／网页、scripts/local和全部原台账前缀保持。未stash/reset/checkout、提交、推送或重放旧patch。61文件为本单元增量，不冒充整个脏工作区的来源。
- macOS arm64／Go1.22.12沿既有external链接参数；Python3.12只映射到测试子进程PATH，退出即清理；复用已有Playwright/Chromium，没有安装或全局配置改变。[资源收尾](../output/s3.4b2b/b-p/cleanup.json)记录自建失败/超时临时目录清理，测试／浏览器／Vite均结束，无长驻资源。无生产／远端连接、真实适配器或Alertmanager调用、真实库迁移、发布或部署；Profile未知，未作为权限依据。

回退与后续：源码只能按本B-P增量撤回，并保留旧成果；隔离patch反向重建不等于数据恢复。有生命周期／准入／检查登记数据不得直接降级旧程序，不删登记、不降user_version。schema48／49／50恢复兼容、真实profile证明、B-R执行至实际清理、其他执行器、全局无工作证明及外部停用继续未完成。

治理短检查仅记录候选：共享备份闭包与真实执行收敛仍需独立证明；启动恢复／回执补交也是所有权边界；跨来源幂等键不能借用人工批准；时区应在摘要固定前规范化。未修改治理规则或清退历史引用。B-P到此停止，不进入B-R或真实profile接入。


### S3.4b2b B-R：获批执行至实际清理接线（2026-10-04，Asia/Shanghai）

**状态：completed——仅 B-R 首版人工单应用本地 update（含到期后人工执行）在本地合成范围完成。** 本段更新 B-P 交接中的“B-R 未实施”，保留其当时状态、全部历史正文及40个稳定ID；关联 OPS-07-01／E02，不关闭整体租户管理、全局无工作证明或生产验收。真实 profile 允许集合仍为空，当前不是生产可发布声明。

实际接线：

- [执行协调者](../internal/runner/release_execution.go)先沿当前权限、执行身份、键、摘要及任务关联安全重放；新执行必须具备 v2 创建证明、原目标代次及私有执行能力。Store 从持久化事实核验完整目标／批准／权限，准入及 preparing 提交成功才授予本次一次性权；失败／响应不明不重获 owner，不降级旧链。
- [Store 入队与认领](../internal/store/release_execution.go)在首写锁事务内核验原 owner、revision、完整目标与当前授权。定时激活、任务、计划、事件、审计及 bindRegisteredWorkTaskTx 同事务提交，确认成功才 enqueue；认领在目录／合同之前，并再次事务性复验所有目标对象权限。
- [运行](../internal/runner/release_run.go)用进程内 lease 明确交接 owner，映射只能首次插入／同引用核对。每次执行／回滚均等待可信调用收敛证据；不确定调用不叠加回滚，不从 runner_owner、任务或数据库重建能力。原始 token 不进入 Task、合同、数据库、日志、HTTP 或审计。默认 CommandExecutor、真实 profile、旧普通入口和远端降级仍被拒绝。
- [收口](../internal/runner/release_settlement.go)区分任务终态、执行收敛、heartbeat 退出确认、锁释放、观察、检查目录清理和精确静默ID确认。成功即使零观察也先 observing，随后 finalizer 处理真实清理；任务证据及备份保留。静默 DELETE 后须 GET 确认 expired／不存在，并提交释放事实；只失败在本地提交时，原调用收据下只读确认并补交一次，不再 DELETE。
- [关闭事务](../internal/store/release_settlement.go)原子提交 plan.completed、收口审计及登记 closed；合法已收敛失败保留 plan.needs_attention，仅关闭登记。preparing／uncertain、任务 needs_attention／paused、终态提交失败、清理失败及 owner 丢失继续阻断。关闭响应丢失只读重放。
- /close 的明确结束阻断，在原 owner、当前主体／完整目标权限及阻断审计同事务确认后才返回匹配请求键的 plan_closure_blocked／attemptState=finished／newAttemptAllowed。网页仅在这种响应后允许下次人工点击换键；普通网络错误保留原键、不自动重发。ConfirmationDialog 根据服务端能力投影开放符合合同的 v2；沿原中文、样式和身份限制。

验证与独立复核：

| 项目 | 实际证据 |
| --- | --- |
| 最终 Go 四包 | [go-four-packages-delivery.jsonl](../output/s3.4b2b/b-r/checks/go-four-packages-delivery.jsonl)：runner／store／model／web，-count=1，1134个含子测试通过事件，0失败；6个既有按需浏览器未重跑，本轮浏览器在独立开关下实跑。 |
| 状态、事务及 race | [需求映射](../output/s3.4b2b/b-r/coverage.json)、[结果汇总](../output/s3.4b2b/b-r/checks/summary.json)：同键一次性权、定时前拒绝／到期准入、旧入口／错owner、目标变更、迟到调用、零观察中间态、失败安全收尾、静默确认／补交、入队／终态／关闭SQL和COMMIT故障、响应丢失及私有停用竞争。Store广域race588.435s通过；Runner最终相关race176.176s通过；复核后定向race Runner104.349s／Store75.171s、补充阻断race4.455s／14.044s均通过。首轮响应丢失测试缺少WaitGroup同步证据，已加channel并回验，失败日志保留。 |
| 网页与前端 | [真实网页HTTP日志](../output/s3.4b2b/b-r/checks/browser-observation.log)及[页面结果／六张截图索引](../output/s3.4b2b/b-r/browser/run-1791121591308/result.json)：实际 App→Web代理→Runner→临时SQLite，双合成Cookie创建／独立批准／执行，真实1秒观察、告警阻断、人工新键及最终完成；执行3阶段、检查3次、静默创建／解除／确认各1次。桌面／390px／深色偏好、键盘提交和滚动后按钮完整可见。58项前端测试、类型检查、构建通过；lint0错误，两条未改动文件的既有警告。 |
| 配置、备份与环境 | [运行环境](../output/s3.4b2b/b-r/runtime.json)、[配置日志](../output/s3.4b2b/b-r/checks/go-config.jsonl)、[Python快照10项](../output/s3.4b2b/b-r/checks/python-snapshot.log)通过。macOS Go1.22.12沿既有external链接；Python3.12.12只映射测试进程，未安装。schema.go和备份脚本保持B-R起点字节；未迁移任何既有数据库，恢复允许集合未扩大。 |
| 独立只读复核 | [复核记录](../output/s3.4b2b/b-r/review/result.json)：默认配置、fork_turns=none。首轮发现运行认领完整授权及收口失权签发凭据两项，已修正；真实SaveAccessPolicySnapshot＋channel／双Store验证撤权、事务各写点与COMMIT失败、无新键资格。最终复查产品代码／27文件指纹／日志，无新增阻断，后续证据缺口已补齐。 |

增量与保全：

- [本轮patch](../output/s3.4b2b/b-r/s3.4b2b-b-r-local.patch)仅相对B-R起点，27文件，排除台账、output与前阶段差异；SHA-256：43fdf8a731fe93d9d5aead4f78ef64d431856d18137f5aa414895250c1d8a50c。
- 文件集合 SHA-256：bcab158ef570e7d282e670cbfa66cfab67a06c24f513bf8565e22892163c1d30；口径为排序路径、NUL、原字节、NUL。[delta.json](../output/s3.4b2b/b-r/delta.json)含完整27文件指纹，隔离正／反向重建逐字节通过。
- [起点](../output/s3.4b2b/b-r/baseline.json)含836份非忽略源码／台账；原字节归档SHA-256：5e0dba80d7daeeadfdfa9646fba96bbb1d7f5d76b5cadb76e097f0e5d880afb1。起点集合SHA-256：e01665595d07bd2378d5c16852d8ca0fd2e56c872a5262c6e81c2c52debff1ec。[保全](../output/s3.4b2b/b-r/preservation.json)确认564份历史证据／scripts/local及未涉及源码保持；A、B-P及此前输出未覆盖。全部指纹及日志／截图完整性见[integrity.json](../output/s3.4b2b/b-r/integrity.json)。
- HEAD保持main／c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca。起点暂存区为空，过程中观测到106项外部暂存（操作者未核实），主代理未执行暂存或更改索引，保留原样；见[外部Git状态](../output/s3.4b2b/b-r/external-git-state.json)。本轮未stash、reset、checkout、提交或推送。

剩余边界：真实profile、其他执行器、全局无工作证明、外部租户停用和schema48／49／50恢复兼容仍未完成；有生命周期／准入／检查数据不得降级旧程序。源码patch反向重建不证明数据库可降级。没有生产连接、真实适配器调用、系统安装、全局修改、发布或部署。测试HTTP／Unix socket／Chromium、临时SQLite、Python映射及重建目录均随测试／脚本退出清理，必要证据保留。

治理短检查仅记录候选：运行认领与“允许新尝试”均属于完整授权复验点；不把任务终态当作清理证明。既有通用拒绝文案仍保留B-P阶段名称，可在后续文案整理中统一；不影响本轮门禁。未新增治理规则、清退历史引用或打开后续能力。B-R到此停止。

## S3.4b3a：首个真实执行 profile 候选核验与实施方案 A（2026-10-04，Asia/Shanghai）

**状态：completed，仅候选核验与下一阶段方案准备完成；真实接入仍 blocked。** 关联 OPS-07-01／E02，保留全部40个稳定ID。选择 **Sub2API**，不表示任何现有真实 profile 已通过；真实允许集合仍为空。B-P／B-R只在原本地合成范围完成。本轮仅追加本台账与 `output/s3.4b3a/` 证据，没有产品、适配器、配置或权限实现改动。以下实施规格均待另批，不能当作现状。

### 候选比较与首版范围

| 候选 | 人工单应用本地 update | 调用／资源与动态委派 | 已有隔离基础 | 最小缺口与选择 |
| --- | --- | --- | --- | --- |
| AreaForge | 入口支持，但不具备可信 profile | `adapters/areaforge.sh:12-29,97-106,197-205,247-268` 引用外部 updater/config/smoke；inspect 已 source updater，后续下载／迁移闭包不能只靠本仓证明 | `test_areaforge_adapter.py` 有 updater、Docker替身 | 尚需外部 updater 来源、配置及全部后继行为证据；本轮不继续展开 |
| Sub2API | 支持七阶段 update 和同次失败 rollback | 内层 `scripts/deploy/update-control/adapters/sub2api.sh` 在库；共享备份与环境覆盖可定位；容器内部迁移仍缺来源证据 | `test_sub2api_adapter.py` 有3项外层参数合同测试 | **选定**：直接委派源码可审阅，仍需服务限定备份、严格执行协议、迁移／清理证明 |
| 通用 Compose | 本身只是委派路由 | `adapters/compose-service.sh:12,40-78` 从 catalog runtime 选择 inspect/update/backup helper；不能整体放行全部项目 | `test_compose_service_adapter.py` 有委派替身 | 不作为泛化profile；只保留选定Sub2API链中必需的固定前缀 |

实际示例并非直接执行 `sub2api.sh`：`config/services.example.json:428-513` 为 `adapterRef=compose-service-v1`；`internal/config/config.go:946` 解析 adapterRef，Compose再委派Sub2API。拟支持的完整路径：人工B-P创建（应用inspect＋Nginx inspect）→独立批准→B-R准入／再次检查／维护静默→Compose入口→Sub2API入口→legacy七阶段→同次失败rollback→观察／静默释放／收口inspect与检查目录清理。七阶段为 preflight、backup、migration、apply、health、smoke、identity；rollback仅为该update的内部恢复阶段。保留到期后人工执行；不开放单独rollback、prepare、check/discover、restart、恢复、自动／批量／远端或其他应用。已存在的发现／prepared证据是输入依赖，不借此开放新类别。

### 实际资源与依赖证据

以下“源码”只证明脚本选择器和行为；“示例”不证明当前生产对象／归属。所有真实对象ID、租户、服务器、端点及挂载解析仍需未来获批只读核验，不采用示例 `production/losangeles` 填空。

| 范围 | 已检查的事实与出处 | 接入前缺口／最小映射 |
| --- | --- | --- |
| 应用与检查 | `adapters/sub2api.sh:90-123` inspect读取app镜像／OCI标签、app/PG/Redis状态、`/health`及 `schema_migrations` count；容器名与URL可环境覆盖 | daemon身份＋容器ID/名称＋镜像、PG实际数据库、健康端点逐项对应已登记对象；GET及SQL查询的服务器端后继仍须源码证明 |
| Compose与文件 | legacy `:129-147,225-239,259-267,287-300` 两份Compose先后替换，写同目录 `.update-control.tmp`，操作目录留快照；`up -d --no-deps --force-recreate sub2api`，未显式 `-p`；rollback用root属主/0644 install | 绑定project、service、网络、路径父目录、两份文件及权限；禁止额外include/extends/helper/挂载、外部Docker context和未解析环境；部分替换不是原子双副本提交 |
| 数据与网络 | `services/sub2api/compose.yml:60,68,87,100,181,209,251,270` 声明app数据、PG/Redis bind目录、AUTO_SETUP、DB及Redis DB和网络；实际值受env影响 | 已登记应用/数据对象，精确PG库及cluster、Redis实例及BGSAVE的实例全量范围、三个挂载根、Compose网络与端口；Redis DB编号不能缩小BGSAVE范围；app配置文件/镜像也可能改变访问目标 |
| 共享PG | `scripts/backup/backup-postgres.sh:6-20` 经wrapper，枚举sub2api-postgres、account-vault-postgres-1、areaforge-postgres；存在则pg_dumpall，非仅单库 | 现链跨应用且包含cluster globals/全部库；不能只映射Sub2API。首版拒绝此默认共享行为，建议新增明确service选择模式（见实施单元），保留原默认调用 |
| Redis备份 | `backup-redis.sh:13-35,47-98,101-107` env/helper选目录；固定sub2api-redis，BGSAVE并轮询状态／时间／mtime；归档RDB及可选users.acl密码哈希 | 正常路径已有部分完成证据；异常时daemon后台任务可能未结束。需实例独占/调用关联和全实例归属，敏感ACL副本受控保存与保留期限 |
| 共享卷／控制面 | `backup-volumes.sh:13-43,107-182,201-204` Sub2API数据、jadeai-data、AreaForge上传/ops-state、AreaSong Ops快照与整个operations；包含绝对 `/usr/bin/python3` 调用 | 原链会读取其他应用及跨租户控制面历史，不能归给发布租户，也不能由backup:global锁替代。新限定模式必须证明这些分支根本不进入 |
| wrapper及环境 | `run-backup-job.sh:6-8,24-35,42-56` 动态JOB_SCRIPT_DIR、timeout/nice/flock；锁与node_exporter指标更新；`executor.go:97-99`继承环境 | 纳入helper递归版本、解释器、Python导入路径、PATH、TMPDIR、Docker endpoint、锁文件、指标文件、日志和临时根；禁止OPS_BACKUP_JOB_WRAPPED等绕过变量 |
| 备份元数据 | `sub2api.sh:47-66` 从共享日志仅挑Sub2API产物；`restore_point_metadata.py:113-153`完整复制两份Compose及env至configs归档，写runtime JSON，前后读取容器/迁移；导入backup_manifest及restore_contract | 日志过滤不限制真实备份范围。完整env副本属于敏感数据处理；显式纳入未来批准和权限，不在本轮读取。绑定五类artifact及来源；失败的半成品保留隔离标记，不误当成功恢复点 |
| 更新来源 | `sub2api.sh:72-87,181-211` 每阶段可重读static/prepared并生成effective-releases；legacy `:35-46,249-257`依赖catalog镜像和演练路径/SHA256SUMS | 固定选定tag、旧/新镜像ID、源码commit、prepared记录和演练内容；manifest条目限制在演练根内。现库v0.1.168是completed，非可更新目标；它与当前Compose也非同一版本基线 |
| smoke／凭据 | legacy `:161-167,193-221,274-277,298`读取admin/user key，缺admin key时INSERT，EXIT的DELETE失败被忽略；访问管理API和/v1/models，接受200/402/403 | 当前“read-only smoke”文字不足以证明只读。需目标版本路由／中间件证据、凭据读取授权及等价认证覆盖；不得静默降级公共健康检查 |
| 流量 | 示例 `:452-459` 的direct.cpa.areasong.top站点、traffic/maintenance conf；`nginx-traffic.sh:125-199,402-406` inspect核对文件链/marker/hostname/模板并读摘要 | 首版只授予inspect，不执行nginx -t/reload/drain；需证明站点与include影响的全部hostname/租户及实际文件绑定，不能因单个hostname存在就断言唯一归属 |
| 静默 | `alerts.go:100-131`在七阶段外创建；`alertmanager.go:105-140`等值matchers＋转义锚定告警名regex；示例只有service=sub2api | 需endpoint＋精确标签全集＋告警名映射。相同service标签可跨主机/租户；未证明唯一时拒绝。静默ID、期限、DELETE后GET确认及本地释放收据沿B-R，网络不明不重发 |

### 最小 resource / scopeDigest 合同（待实现）

沿现有 `ReleaseScopeDefinition`（`internal/model/release_lifecycle.go:19-36`）使用 version=1、mode=local，候选ID可为 `sub2api-local-update-v1`，**只作为方案名称**。resources用规范 kind/selector/objectId：application（daemon/project/sub2api）；container（app、PG、Redis）；database（PG cluster/库、Redis全实例）；mount/network；file（Compose双副本、env、app配置、catalog、prepared、演练根、Nginx三文件）；artifact-root／operation-root／temporary-root；backup-lock／metric／log；endpoint（health、经过审阅的smoke与Alertmanager matcher范围）。读、写、替换及保留策略在可信解析manifest中分别列明，并纳入其规范摘要，不能用Kind隐含授权。

每项必须映射唯一真实 objectId，再从目录核实tenantId/serverId，跨租户共享资源拆成可核验资源条目；缺对象表达能力就先拒绝、另批最小模型扩展，不能虚构对象或给控制面全库强塞一个租户。现共享链的Account Vault、AreaForge、JadeAI和控制面仅列为已发现风险；**拟首版采用显式限定备份后不包含它们**，必须用负例证明确实排除。单PG容器若仍有其他库/角色，pg_dumpall依然不满足单应用；建议限定数据库dump并核对现有恢复消费者，无法等价恢复时先阻断，不能擅自改变备份格式。Redis无法限定到逻辑DB，须证明整个实例专属或拒绝。

`resolveReleaseScope`现将实际/声明规范比较、implementationDigests、对象定义、action及合同版本聚合为scopeDigest；文件只做Lstat/hash，不证明行为封闭或消除检查到执行间替换。下一阶段增加内部版本化解析manifest：绑定完整helper/import树、工具版本、受控运行环境、解析后的Compose/挂载/DB/端点、选定目标、资源归属和可允许状态转换；manifest本身摘要通过固定只读文件纳入implementationDigests，最终仍用现有WorkDigest的64位小写十六进制及原B-P/B-R摘要路径。

**必须处理合法自变更**：不能把当前可变Compose原字节直接当永远不变的implementationDigest，否则apply后的health/rollback/close都会因自身修改拒绝；也不能把旧/新配置无条件并集放行。私有resolve接口需显式接收选定target和可信阶段上下文（创建仅before，已认领lease持有本次转换收据），绑定不变资源拓扑与批准的before→target→允许rollback状态。两份Compose、镜像、迁移状态分别比对该阶段的预期，第三种值一律拒绝。为此修改 `release_scope.go`、`release_execution.go`、`release_run.go`、`plan_preparation.go` 的私有输入/调用点及合成夹具；不得由HTTP传阶段或为旧计划补章，不改v1历史摘要。新增模型/schema若最终确需，另列后再批，不默认包含。

旧计划失效条件包括helper或导入模块、工具、环境及配置版本、目标tag/image/prepared/rehearsal、路径/挂载、project/daemon、租户/服务器映射、静默范围、动作/phase semantics变化。正常业务数据变化不是自动使scope失效；本次合法配置转换由原批准＋单次执行收据约束。声明核验后还需执行期间不可替换的只读bundle/固定打开文件及受控写目录；hash独自不是信任根。

### 收敛与清理：现状和拟证明

现有 `planInspectionExecutor` 与 `releaseExecutor` 是包内私有能力；CommandExecutor只有Execute，合成夹具用channel和合成digest（`plan_preparation_test.go:35-64`、`release_execution_test.go:30-75`），不是生产证明。`plan_preparation.go:175`在B-P登记进入inspecting后建检查目录、逐调用等Settled、校验digest，清理并Lstat确认不存在；异常保持uncertain。`release_run.go:19-50`逐次等停止证明，`release_settlement.go`另等执行、heartbeat、锁、静默及检查清理；持久化任务终态不会补出这些事实。

拟受控实现分开产出两种能力：

- 检查从开始前记录唯一callId、owner关联、输入/实现/资源摘要开始，固定inspect命令协议；返回结果与是否真正停止分开。闭合进程组/已回收后代、Docker exec服务端结束及限定只读查询无后继写者全部可观察后才关闭Settled；先持久化脱敏回执，再赋EvidenceDigest。检查的返回nil不能直接关闭channel，更不授予update能力。
- 执行每阶段都须在返回成功前证明本阶段后继结束。尤其apply的`up -d`只说明提交，必须在**apply内部**等待本次启动/迁移完成回执，不能把此证明推迟给下一health阶段。migration阶段目前只验演练，不执行生产迁移；迁移count相等不能证明迁移集合、事务结束或锁释放。需所选源码commit/image的启动机制及权威完成/失败信号，映射本次容器ID/启动代次/迁移批次；拿不到则执行能力继续关闭。
- Linux专用有界子进程组控制、TERM→等待→KILL及wait/reap、stdout/stderr关闭只解决宿主后代；另需受限cgroup/隔离边界防setsid逃逸，权限/平台能力不足即拒绝。Docker exec应使用固定API/exec ID和服务端退出事实，不能仅等docker CLI；Docker daemon创建/重建、应用migration、Redis BGSAVE均需各自完成和无后继证明。Linux隔离环境另批提供，不在macOS安装基础设施或新增无限制shell包装器。
- 正常业务进程可以持续提供服务；需要停止的是本次更新引发的配置写入、迁移、备份、回滚与延迟后继。应用镜像的restart/entrypoint可能再次迁移，需要源码和重启代次协议证明；不能靠健康200或进程树空掩盖daemon工作。临时断连、取消、deadline、主进程死亡、回执丢失、后台写者、清理失败均保持uncertain，不自动重试副作用或叠加rollback。
- Redis正常路径有BGSAVE轮询，但还须绑定本次请求、实例身份和独占边界；失败／超时不能宣称后台结束。wrapper的timeout退出也不证明远端/daemon退出。未在所读shell中看到显式后台`&`作业，不等于helper/镜像没有后台工作。curl无连接/总时限，health循环有次数上限也不构成整体有界。
- rollback只允许本次owner且上一调用已收敛；细分未修改、单份/双份Compose已改、daemon变更提交、迁移进行中、目标稳定。legacy固定要求target_migrations，对apply早失败并不适用，需按明确状态恢复双副本及镜像；基线/完整目标各验，部分迁移或未知schema绝不自动回滚。只恢复应用产物，不回退数据；旧镜像兼容演练是必要输入。

EvidenceDigest拟覆盖版本化call回执：callId、plan/preparation关联、scope/实现/运行参数摘要、开始/结束、退出结果、宿主进程回收、外部exec/迁移/BGSAVE完成身份、无后继断言的证据引用、临时资源清单及清理结果。不保存owner token、env全文或key。回执先完成原子保存和摘要核验，再关Settled；保存失败保持阻断。接收者仍沿原关联/owner/revision验证，不能用任意合法64位字符串冒充证明。

清理清单包括 `.plan-inspection-*`、health/http临时体、Redis/卷临时根、Compose同目录tmp、metric tmp和本次创建的临时凭据（拟新profile禁用创建）、所有专属执行容器/进程。未知归属不清理；检查目录只在无写者后清理并确认消失。操作目录task-contract、legacy/effective请求、before快照、依赖/身份、smoke摘要、阶段回执和必要失败日志、已提交备份/恢复点及演练证据均按历史证据保留；不能为了目录空删除。原失败临时文件可能含敏感数据，应受限隔离并阻断，不自动清退历史文件。

### 下一阶段具体批准单元（本轮不申请笼统放行）

| 单元 | 文件／动作、影响、验证与回退 |
| --- | --- |
| B1：本地窄能力与参数合同 | 新 `internal/runner/sub2api_profile.go`、`sub2api_profile_linux.go`（非Linux拒绝实现及定向tests），仅该实现满足两私有接口；按上节修改scope/调用输入和夹具；`engine.go`/启动组合点仅在测试显式注入，默认继续CommandExecutor且真实允许集合空。固定Compose→Sub2API→legacy及import树、只读bundle、清洁环境、资源解析与阶段回执；不增加HTTP/catalog自报授信。新增scope正反例/B-P/B-R合成回归；本地窗口起点差异保全，失败只撤本单元增量，不降级schema/删登记。 |
| B2：Sub2API真实脚本合同收紧（需明确批准敏感处理及兼容影响） | `adapters/compose-service.sh`、`adapters/sub2api.sh`、`scripts/deploy/update-control/adapters/sub2api.sh`固定选定目标和两层环境、显式project、严格路径、调用ID/回执、无重试不确定结果、apply等待迁移和部分成功回滚。`scripts/backup/backup-postgres.sh`、`backup-redis.sh`、`backup-volumes.sh`、`run-backup-job.sh`新增明确服务限定模式与回执，保留旧默认共享作业，不能用环境变量假装范围已收紧；定向更新上述适配器测试、`scripts/backup/tests/`及`restore_point_metadata.py`相关合同测试。限定模式的数据库dump格式/恢复兼容、完整env/ACL归档及锁/指标命名需验证；不能删旧共享备份职责或放宽恢复验证。smoke禁止临时key创建，缺已批准验证身份直接拒绝，保持原认证覆盖并查明路由副作用。若要新凭据方案或改备份格式，须独立补充批准。共享脚本不是无关内部实现，须旧调用方回归；回退仅源码增量，既有敏感副本/数据不得自行删除。 |
| B3：未来Linux隔离验收 | 经另批提供隔离Linux、已存在所需工具/私有daemon与合成数据；目标Sub2API镜像及对应源码/演练已离线提供，禁止访问生产端点。实测process/daemon/迁移/BGSAVE、备份恢复格式和临时资源全链；真实HTTP产品链使用该窄profile、双合成账号与隔离静默服务。此前B-P/B-R合成测试复用但不代替此层。失败保留uncertain及证据，不补造settled；环境资源撤除仅按该单元创建清单与已批清理执行。 |
| C：真实配置、真实操作和启用分别批准 | 在B1/B2/B3通过并独立复核前不修改允许集合；未来先获批只读确认真实daemon/工具/镜像来源、Compose/env解析（不输出秘密）、DB/Redis专属、挂载、站点/标签映射、现有prepared证据及所有者；再提交具体真实配置差异、实际操作单元、静默影响和启用单元。本轮不提供生产放行建议，不安装/提交/发布/部署。 |

配置匹配只能选择声明；真正能力由编译在受控Runner内的私有实现和经验证装配提供。声明不相等、未知helper/import、动态目标、缺归属、环境覆盖、资源多出/遗漏、流量写动作、未获授信平台、迁移/清理不可证明均拒绝；不能回落CommandExecutor或旧普通入口。B-P最早活动前登记、独立批准、B-R准入/所有权/任务原子关联/最终关闭不变，重放不获得执行权。本地批准并不包括真实数据/凭据处理和生产启用。

### 可执行验证分层、复核与保全

本轮已检查 `test_sub2api_adapter.py` 全文及实际触发分支。原测试继承环境且两个负例依赖早拒绝；本轮用 [check_adapter.py](../output/s3.4b3a/check_adapter.py) 清空环境、固定HOME/TMPDIR至自建临时根、固定PATH，外部命令deny替身、helper/生产路径覆盖为本地哨兵。既有测试自行提供fake docker、static本地catalog和fake legacy；所选三个分支不调用绝对生产路径，不读真实env，不接触socket或网络。没有运行真实legacy、共享备份或Nginx命令。

[adapter-tests.log](../output/s3.4b3a/adapter-tests.log)：3/3通过，覆盖completed目标拒绝、非当前发布来源rollback拒绝、动态prepared跨阶段委派；[checks.json](../output/s3.4b3a/checks.json)确认临时根已删除。此为**真实外层适配器＋替身命令的参数合同层**；另有shell语法与方案引用/差异静态验证，见[static-checks.json](../output/s3.4b3a/static-checks.json)。未重跑Go四包或浏览器，不借用历史通过声明本轮真实工具已验收。**未来Linux真实工具隔离层尚未运行**。

后续必需正反例：①精确资源匹配成功，漏/多资源、错误租户/daemon/project、共享备份回退、环境覆盖、helper/import替换拒绝；②合法before→target→rollback状态不使批准失效，第三值/单份漂移阻断；③inspect/update/各失败点rollback的明确成功和失败，回执与调用身份匹配；④返回后写者、setsid/管道后代、客户端退出而docker exec仍在跑、BGSAVE超时、取消与完成竞争、migration部分成功/后台重试、清理失败均阻断；⑤网络不明不重试写入、不自动关闭，不用task/plan终态证明收敛；⑥完整B-P创建→独立批准→B-R执行→观察→精确静默确认→检查目录清理→原子关闭，失权/错误owner/重启不接管；⑦未知profile/其他应用/动作始终拒绝；⑧备份限定模式确实不访问另外三应用和控制面，旧共享调用兼容；⑨敏感备份/临时体权限、认证smoke等价性；⑩Linux GNU date/stat/timeout/flock、root install、cgroup与macOS差异。macOS这3项未执行成功preflight时间生成、root rollback或真实工具，不证明这些路径通过。

独立只读复核 `/root/profile_review` 按默认配置、fork_turns=none完成，未修改/运行/派生。六组结论及主代理取舍见 [review.json](../output/s3.4b3a/review.json)：递归helper与TOCTOU、敏感元数据/全量PG、部分Compose失败回滚、Redis异常收敛、迁移/smoke外部源码、七阶段外静默。主代理沿legacy 129–167/193–221/259–300、wrapper 24–56、metadata 113–153及Runner收口复核关键出处，均纳入上述规格；并未将复核视为测试通过。

起点[baseline.json](../output/s3.4b3a/baseline.json)记录1456份源码／历史证据指纹，含scripts/local及既有输出；本轮台账修改前已保存原长度/摘要，不复制第二份报告。main／HEAD=`c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca`；已有暂存106项，`git diff --cached --binary` SHA-256=`1088fadfa396ffa326b7e87bb0f6e467b3bcd13a92473ae55034bd461906497e`。B-R patch SHA-256=`43fdf8a731fe93d9d5aead4f78ef64d431856d18137f5aa414895250c1d8a50c`及27文件集合=`bcab158ef570e7d282e670cbfa66cfab67a06c24f513bf8565e22892163c1d30`已按delta原排序路径＋NUL＋原字节＋NUL口径复算一致。收尾[ preservation.json](../output/s3.4b3a/preservation.json)核验台账历史前缀、稳定ID、所有其余既有文件、索引与本轮新增文件边界。暂存与工作区版本分别记录，本轮未暂存、stash/reset/checkout或重放patch。

缺失最小输入：选定新prepared目标及离线源码/镜像/演练；迁移和smoke后继行为协议；获批Linux隔离设施；真实配置解析与资源/租户映射（留未来只读单元）。未获取真实凭据、生产env或数据，无生产/远端/Alertmanager连接；Warp Profile未查询且未知，本地权限不当作Test或生产授权。本轮检查子进程已退出，只有必要证据保留。

治理短检查只报告候选：smoke临时key清理吞错、共享备份范围超过展示恢复点、部分apply回滚的目标迁移数假设；`ErrReleaseNotIntegrated`仍带B-P历史阶段文案。未改规则、清退存量登记或旧引用。其他执行器、全局无工作证明、外部租户停用、schema48／49／50恢复兼容、删除／ID复用、特殊绑定及全局验收全部保留未完成；有生命周期数据不得直接降级。A到此停止，不向其他对话发送消息。


### S3.4b3b-B1：Sub2API 私有 profile 与阶段回执合同（2026-10-04，Asia/Shanghai）

**状态：completed——仅 B1 规范化资源 manifest、私有上下文、合法阶段转换与回执协议的本地合成实现及验证完成。真实接入仍 blocked，不可据此发布或部署。** 关联 OPS-07-01／E02，保留全部40个稳定ID和上述历史。用户明确批准本轮私有合同与合成验证；B2／B3及真实配置启用仍另批。

实际增量共20份Go源码／测试，完整集合见 [delta.json](../output/s3.4b3b/b1/delta.json)。新增 `sub2api_manifest.go`、`sub2api_bundle.go`、`sub2api_resources.go`、`sub2api_profile.go`、Linux／非Linux拒绝文件、`sub2api_receipt.go`、`release_context.go` 及三份定向测试；调整 `release_scope.go`、`plan_preparation.go`、`release_execution.go`、`release_run.go`、`release_settlement.go`、`release_lifecycle.go`、`executor.go` 与两份既有合成profile夹具。未改任何适配器、共享备份／smoke／迁移／rollback脚本、model、schema、配置或启动组合点。

**私有合同与资源边界：** `releaseScopeInput` 提供选定target和包内上下文；创建登记后生成检查上下文，执行准入后生成lease上下文，包含 preparation／plan／task／lease ID、target、scope摘要及profile自己推进的前置回执。owner不复制进入上下文，阶段不由HTTP提供。创建和批准只接受before；执行逐阶段及收口传递同次上下文；旧v1编码和已保存v2摘要语义不变，其他合成profile仍使用原单状态要求。`releaseCoordinatorVerifier` 将实际Engine的Alertmanager私有端点能力、检查根、操作根和备份根与manifest绑定；真实AlertmanagerClient没有新增能力。

解析器严格读取结构化manifest，拒绝未知／重复JSON字段，要求固定Compose→Sub2API→legacy、备份wrapper／metadata／import树、工具／运行环境条目及源码／镜像／prepared／演练材料的路径和摘要。不可变文件流式哈希，JSON／配置读取限制1MiB；符号链接（含父目录）、路径逃逸、额外helper、漏资源、错归属、共享备份模式或默认共享根均拒绝。资源逐项绑定objectId／tenantId／serverId，本profile只表达单对象专属范围；包含daemon、project、service、容器、PG／Redis实例、两份Compose、env、挂载／网络／端点、各根目录、锁／指标／日志、Nginx和精确告警匹配。Git revision与源码材料SHA-256分离，Docker ImageID与镜像归档摘要也分别绑定，不从文件摘要推断运行身份。执行环境只允许固定LANG／LC_ALL／TZ／PATH，不能继承宿主覆盖变量。

**明确的解析能力边界：** 本轮实现的是“受控解析结果的结构／归属／摘要及状态校验协议”，不是通用Compose、`.env`或OCI归档语义解析器。env原文件在此校验字节身份；真实资源解析、环境有效值、材料与运行镜像的对应关系、不可替换输入、封闭依赖和专属实例均须由可信后端观察并证明。B1没有该真实后端；合成observer返回的资源与边界事实不构成真实验收。哈希、布尔字段或平台名本身不能授信；现存共享备份仍不符合限定模式，未因manifest中的模式声明而获得真实运行权。

**状态／回执：** 固定preflight、backup、migration、apply、health、smoke、identity七阶段，仅允许同次update失败后受控rollback。两份Compose、ImageID／版本／Git revision与迁移集合摘要分别核对。apply成功必须是target；失败仅允许明确before、已证明单份／双份配置替换或完整target。部分组合只在同次apply失败及rollback校验中有效，不成为一般运行状态；未知schema、迁移进行中、第三值、跨执行或缺前置回执拒绝。rollback恢复旧配置／镜像，保持已证明的schema，不自动回滚数据。

回执绑定callId、preparation／plan／task／lease、scope／实现／输入摘要、前置回执、target／阶段、结果摘要和清理事实。宿主及后代结束、daemon工作结束、迁移／BGSAVE终结、无后继写者、临时资源清理分别记录，并关联本次调用与对应资源。事实只能由包内后端产生，不能从任意文件或HTTP提交；后端必须保留Proof引用对应的实际证据。只有事实完整、原子保存且复读字节一致才关闭Settled；保存失败、取消、未知结果、关联错配及清理失败保持阻断，迟到结果不恢复执行。输出固定非敏感投影，不传播后端原始错误／env／密钥／响应正文；owner未出现在序列化、操作目录、回执、事件或HTTP检查结果中。正常业务服务无需退出。

| 最终验证 | 结果与证据 |
| --- | --- |
| Sub2API定向 | [target-delivery.json](../output/s3.4b3b/b1/checks/target-delivery.json)：119通过事件，0失败；含七阶段、各失败点、部分双文件回滚、40位Git revision、独立镜像材料摘要、大文件、依赖／归属／环境负例、回执乱序／篡改／跨执行、异步取消后实际合成写入、保存／清理失败，以及B-P→独立批准→B-R→收口与Nginx检查链。 |
| 指定四包 | [four-delivery.json](../output/s3.4b3b/b1/checks/four-delivery.json)：`go test -ldflags=-linkmode=external ./internal/runner ./internal/store ./internal/model ./internal/web -count=1`，1253通过事件、0失败；7个既有可选浏览器测试按开关跳过，前端未改、不宣称重新做了网页验收。 |
| 相关race | [race-delivery.json](../output/s3.4b3b/b1/checks/race-delivery.json)：Sub2API／Registered／Preparation／HistoricalRelease相关Runner回归，181通过事件、0失败；1个既有浏览器开关跳过。 |
| 配置／平台／静态 | [config-validation.json](../output/s3.4b3b/b1/checks/config-validation.json)55通过；[Linux构建](../output/s3.4b3b/b1/checks/linux-delivery.json)linux/amd64、CGO=0通过，仅构建、不证明Linux运行；[static.json](../output/s3.4b3b/b1/checks/static.json)确认40ID、model／schema／配置／默认装配与差异空白检查。 |
| 独立只读复核 | 默认配置、fork_turns=none，`/root/b1_review`多轮核对实际增量和来源；修复实际客户端绑定、工具遗漏、Git revision／源码摘要混用、ImageID／镜像归档摘要混用四项，最终20文件及集合摘要一致，未发现B1范围剩余可确证阻断。[review.json](../output/s3.4b3b/b1/review.json)保留问题、取舍和最终主代理回验。 |

[coverage.json](../output/s3.4b3b/b1/coverage.json)关联本阶段15项要求；日志保留最初证据脚本工作目录错误（0测试，已修正）及修订前各轮结果，不借前轮通过代替最终检查。运行环境是macOS arm64、Go1.22.12；沿既有external链接和测试进程级Python3.12.12映射，无安装／全局修改。所有成功执行仅为明确标记synthetic的临时文件／SQLite／内存或回环替身，不触达真实Docker socket、数据库／Redis、生产端点、真实适配器或Alertmanager。检查进程已结束，测试自建临时根已清理，必要证据保留。

**增量与保全：** main／HEAD=`c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca`。起点1463份已有文件指纹；源码原字节及106项外部暂存补丁分别保存，未stash／reset／checkout／暂存／提交／重放旧patch。台账历史352560字节完整保留，1453份本次不涉及的原文件（含历史证据和scripts/local）不变。B-R旧patch及delta原字节不变，27文件集合在B1起点复算一致；B1修改后的重叠源码由本轮独立增量表示，不声称仍等于B-R旧工作区集合。

- [B1独立patch](../output/s3.4b3b/b1/b1-local.patch)SHA-256：`1ac6e33af28a06eaa0f6e0143985b9ddb327d93a0fe61d1ca831275d3efcdfca`；20文件集合（按排序路径＋NUL＋字节＋NUL）：`7caf2ee2222a20ecd98d4391c34aae093b18e7c25b67d3cd78b6d29516dca1fc`。排除台账／output／前阶段，隔离目录正向与反向重建均逐字节通过。
- 前后暂存106项摘要均为`1088fadfa396ffa326b7e87bb0f6e467b3bcd13a92473ae55034bd461906497e`；起点非暂存diff摘要`1c567b603fed3242955538a90eb45ef1957331906f898bbf28409e01feb82882`，最终工作区摘要另见[preservation.json](../output/s3.4b3b/b1/preservation.json)。
- [baseline.json](../output/s3.4b3b/b1/baseline.json)SHA-256：`c81b196cefcf5bb38f7504ca21dda0141cc41a150f15095c80f1faf30938f73d`；[源码起点归档](../output/s3.4b3b/b1/source-baseline.zip)SHA-256：`16d019b5963641096983d59c0ad84d9e3a668d7789eea563fa5e5fa62722f0dc`。
- B-R旧patch：`43fdf8a731fe93d9d5aead4f78ef64d431856d18137f5aa414895250c1d8a50c`；其起点27文件集合：`bcab158ef570e7d282e670cbfa66cfab67a06c24f513bf8565e22892163c1d30`。完整本轮产物指纹见[integrity.json](../output/s3.4b3b/b1/integrity.json)。源码回退仅按B1独立增量，保留前阶段成果；不涉及数据库降级或真实恢复。

**默认仍拒绝与后续：** 默认CommandExecutor没有检查或执行私有能力；非Linux明确拒绝实际执行，Linux也因隔离／daemon／迁移／BGSAVE未验收而拒绝。真实后端、真实允许集合与生产装配均未增加，配置或HTTP不能启用。本轮未创建真正可更新的prepared目标，未选择最新版本或伪造真实演练。B2须另批服务限定备份、敏感env／ACL归档边界、smoke认证覆盖和部分失败回滚；需要选定新prepared目标、离线源码／镜像／演练及真实资源映射。B3另需已批准Linux隔离环境和完整收敛证明。共享备份格式、smoke／临时key、up -d迁移、BGSAVE超时、未知schema和部分失败回滚问题均未修复或验收。

治理短检查只记录候选：编排器端点／本地根也是scope消费者；Git revision、ImageID、材料摘要须分别绑定；normalized manifest不得写成真实解析验收。未更新治理规则或清退历史引用。其他执行器、全局无工作证明、外部停用、schema48／49／50恢复兼容及其余全局待办全部保留。B1到此停止，不实施B2、不连接Linux、不发布部署、不向其他对话发送消息；生产状态未触及。

### S3.4b3b-B2a：Sub2API 服务限定备份与敏感归档——A 合同准备（2026-10-04，Asia/Shanghai）

**状态：partially_completed；A 方案准备／本地隔离检查就绪，以下 B2a 实现全部待一次具体批准。** 关联 OPS-07-01／E02，不改40个稳定ID及前阶段正文。当前仅追加本节和 `output/s3.4b3b/b2a/a/`；未修改产品源码、共享脚本或数据库，未读取真实env／ACL／凭据，未备份／恢复真实数据。真实profile允许集合及默认装配仍为空；B1仅有manifest／阶段／回执协议，不是资源观察、Compose语义解析或后台收敛后端。smoke认证、迁移完成证明、部分失败rollback、Linux后端及生产启用不在本单元。

#### 核对证据与推荐选择

- `scripts/backup/backup-postgres.sh:6-24` 经wrapper枚举Sub2API、Account Vault、AreaForge，运行 `pg_dumpall`；`backup-volumes.sh:156-170` 还处理JadeAI、AreaForge及Ops快照／operations。外层 `services/areasong-ops/adapters/sub2api.sh:47-66,200-210` 从stdout／日志挑最后匹配路径，不能证明执行范围；legacy `scripts/deploy/update-control/adapters/sub2api.sh:242-247` 调用三个无参数共享作业。
- `run-backup-job.sh:24-56` 有按job互斥、timeout及共享指标，但 `OPS_BACKUP_JOB_WRAPPED` 可跳过wrapper；timeout退出不证明Docker exec／BGSAVE结束。Redis `backup-redis.sh:54-98` 有时间／mtime／INFO检查，没有本次调用与实例代次的完整归因；ACL当前可选（104-119），生产恢复却强制需要（`restore-sub2api-production.sh:268-282`）。
- `restore-sub2api-isolated.sh:299-316`、`restore-sub2api-production.sh:132-149` 均先以管理角色postgres初始化新实例，再 `gzip -dc | psql -v ON_ERROR_STOP=1 -U postgres -d postgres`。依赖dump内角色、建库、扩展、所有权、GRANT／默认权限及顺序，不能丢弃globals。生产恢复仍用当前env／容器密码（293-305），与隔离恢复使用归档env的语义不同；新模式须显式校验兼容，不能借备份实现顺便替换线上秘密。
- **推荐首版：保留cluster逻辑SQL格式，前提是整个PG cluster已独立证明专属Sub2API且全部纳入scope。** 不是凭容器名选择。单库 `pg_dump -Fc --create` 可减少数据范围，但还需按依赖闭包导出角色／membership／权限／扩展／tablespace及独立pg_restore消费者；不能简单加 `--no-owner --no-acl` 或全量 `pg_dumpall --globals-only`。此方案不纳入B2a；若实际cluster不能满足下面专属条件，B2a必须拒绝，另定单库格式单元，不回退共享作业。
- 已读备份恢复标准、两个相关runbook、工程交付／可靠性／架构引用。Profile无可调用读取工具，记unknown；仅本地任务，未读无关inventory。默认系统Python3.9.6，隔离检查显式用已存在Python3.12.12，无安装。

#### B2a 完整入口、身份与资源合同（待实现）

唯一限定作业入口拟为：`run-backup-job.sh sub2api-set --request <operation>/backup-request.json --request-sha256 <64hex> --result <operation>/backup-result.json`。job固定 `sub2api-set`，内部固定顺序postgres→redis→volumes→config-capture→set-finalize；前四步产生四个收集子回执，set-finalize生成runtime及五产物集合回执。旧 `run-backup-job.sh postgres|redis|volumes|configs` 及三个无参数脚本保持共享职责；限定分支不进入旧枚举函数，不能逐个调用旧脚本再筛输出。三份旧脚本仅补内部锁／阻断门禁，不改默认备份对象、格式或保留策略。

请求严格JSON v1，mode=`service-exclusive-v1`，service=`sub2api`；必须带job、callId（本次backup阶段UUID）、taskId、planId、preparationId、leaseId、target、operationDir、scopeDigest、implementationDigest、parentReceiptDigest、resourceDigest、resourceManifest引用／摘要和明确deadline。子回执以同一callId＋job区分，不按时间戳猜关联。request/result路径必须为已绑定operationDir固定文件名；请求≤1MiB，拒绝未知／重复字段、重复参数、缺项、混合共享／限定参数、相对路径、已有结果、跨task／lease／target、符号链接及父目录逃逸。参数中的摘要只校验字节，不自行授信。

选择权在已获批的私有更新协调器；Compose→Sub2API→legacy只为 `update:backup` 传递同一显式请求和摘要，不能从catalog、HTTP、环境或文件存在性自动选择。普通手工备份／prepare／旧恢复演练继续旧合同，不因此获得profile能力。脚本运行者须为批准的本地执行身份；文件属主本身也不替代批准／资源来源证明。A不创建真正可执行请求。

新 `sub2api_backup.py` 作为set监督入口，使用固定Python函数执行三类备份及metadata，不提供任意shell／helper／容器命令字段；新 `sub2api_backup_contract.py` 负责严格请求／回执／锁状态校验，`sub2api_backup_archive.py` 负责受限文件归档。wrapper与helper均重核完整合同；不使用一个WRAPPED变量跳过限定检查。限定分支必须先于任何共享配置读取，拒绝宿主的 `OPS_BACKUP_JOB_WRAPPED`、`BACKUP_*`、`REDIS_*`、`SUB2API_*`、`OPS_RESTORE_*`、Docker context/TLS覆盖、PG*、PYTHONPATH、BASH_ENV、ENV及注入型loader变量；允许的LANG／LC_ALL／TZ／PATH和本次HOME／TMPDIR由受控bundle／请求导出。内部角色／目录不再由继承环境选择。无请求、无可信资源解析或错配均结束失败，不执行共享备用路径。

资源清单逐项含objectId／tenantId／serverId及读取／写入动作，来自将来获批的真实映射及可信后端观察；合成测试明确synthetic。精确绑定Docker unix endpoint＋daemon ID、project、三个container ID／启动代次、PG system_identifier／连接身份／库与角色全集、Redis run_id与完整实例、app挂载根、两份Compose、env、ACL、产物／临时／日志／指标根及共享锁。B2a仅实现校验／脚本合同，真实观察与不可替换挂载证明留B3；自报exclusive=true不得通过。

允许读取：所选三个容器的固定白名单字段、已批准的PG cluster全部内容、Redis全实例的RDB／ACL、唯一app数据根和下面三份配置。禁止Docker全局ps／无目标inspect、其他容器exec、Account Vault／AreaForge库和配置、JadeAI卷、Ops SQLite／snapshots／operations遍历以及额外include。允许在自身operationDir读请求、写回执，不将其归档为应用数据。不得访问其他应用来“证明排除”。若已批准cluster的只读目录核对发现额外库／角色，立即拒绝dump，不连接这些库或读取其内容。

#### PG／Redis／卷／配置与版本

PG首版必须先核实：daemon与容器ID／启动代次／PG system_identifier稳定；除默认维护／模板库外所有库明确属于Sub2API；默认库无其他业务对象；所有非内置角色、membership、所有权、授权及扩展依赖属于批准范围；无外部tablespace、foreign server／订阅等未映射资源；模板无未声明改造。源cluster与恢复器bootstrap角色／默认库不能冲突（例如dump含CREATE ROLE postgres），不能用忽略SQL错误绕过；缺精确版本的恢复证据就不声明可恢复。数据库目录元数据检查不读取密码哈希；dump本身可能含角色密码哈希，按敏感档案处理。需保证dump期间无并发DDL／角色／扩展变更；仅前后摘要相同不能证明中间无变化，这个独占／协调证明由后续Linux单元提供，缺失拒绝。

拟固定命令参数为：`docker --host <已绑定unix端点> exec <PG容器ID> env -i PATH=<批准容器工具路径> pg_dumpall -U <批准管理角色> -l <批准维护库> --no-password`，stdout直接接 `gzip -n -c` 写私有半成品；不拼接 `sh -c`，不传密码、`--no-owner`、`--no-acl`、`--clean` 或选库过滤。连接仅使用获批的容器本地认证，若不满足就拒绝，不新建凭据或读取未批密码。精确PG工具版本与参数支持需B3验证。

五类role保持不变，全部位于批准的独立Sub2API backupRoot下的 `<taskId>/<callId>/`；该根必须不在共享 `/var/backups/ops`、现有R2上传根、其他应用或operations树内，避免被旧扫描／同步意外纳入。实际根尚未选择，不创建真实目录。

| role | 文件／格式标识 | 内容与检查 |
| --- | --- | --- |
| postgres-sub2api | `postgres.sql.gz`；`pg-dumpall-sql-gzip-v1` | 保留cluster SQL＋globals、gzip；工具退出／gzip全读／大小摘要与调用绑定。SQL可恢复性不由gzip证明。 |
| redis | `redis.tar.gz`；`redis-rdb-acl-targz-v1` | 仅 `metadata.txt`、`redis_data/dump.rdb`、`redis_data/users.acl`；ACL变为限定模式必需，不带AOF。metadata保留旧 `aclfile_included=yes` 等恢复字段。 |
| volume-sub2api-data | `data.tar.gz`；`sub2api-data-targz-v1` | 内部固定 `data/`，仅批准app根；保持原排除 `data/logs`／子树，不新增include。 |
| configs | `configs.tar.gz`；`sub2api-configs-targz-v1` | 精确三成员：`opt/ops/services/sub2api/compose.yml`、`opt/services/sub2api/compose.yml`、`opt/services/sub2api/.env`，与旧消费者布局一致。 |
| runtime-snapshot | `runtime.json`；`sub2api-runtime-json-v2` | schemaVersion=2，service、白名单runtime及下述四产物集合／调用证据；不序列化Config.Env或完整inspect。 |

Redis只能在整个实例归属且所有BGSAVE发起者受控时执行；逻辑DB编号无隔离意义。固定操作为对绑定container ID执行 `redis-cli INFO server`／`INFO persistence`／`BGSAVE`，不传 `-a`，认证只允许已批准的现有机制。发起前记录run_id、容器代次、persistence基线及RDB inode／mtime／摘要；拒绝已有BGSAVE。状态区分not_started、request_sent、running、succeeded、failed、unknown；请求响应丢失后保持unknown，不重发。正常完成需要本次exec身份、请求接受、同run_id完成观察、成功状态及新RDB证据；秒级时间戳／mtime仅佐证，快完成而无法归因时也拒绝完成。定时save、其他客户端BGSAVE、实例重启及复制拓扑必须纳入独占证据，B2a不关闭定时save或修改Redis配置。超时／取消／断连不代表后台停止；copy前后确认文件代次及无写者，不能读取仍可能被替换的RDB后自报成功。

卷归档只以已打开的app根为锚，逐成员no-follow校验：普通文件／目录、合法相对名、批准属主和挂载身份；拒绝symlink、硬链接（nlink>1）、设备／FIFO／socket、嵌套挂载（包括同device的bind mount）、额外目录和路径替换。默认tar不解引用不足以证明安全，不能先遍历其他树再过滤。B2a用合成树和替身检查；挂载身份／TOCTOU封闭依赖Linux可信后端，缺失不启用。数据库是逻辑快照、Redis是一次RDB、文件是逐个读取、配置是前后稳定校验，**五文件齐全／时间接近不等于跨组件应用一致快照**。需要业务停写或协调时另批，不在B2a默认停写；未证实可接受一致性时不得作为更新就绪依据。

两份Compose分别保存受控基线与运行配置以便重建／对照；完整env保留数据库／Redis密码及应用加密、身份配置，不能仅挑几个变量；ACL保存用户权限和密码哈希，现有生产恢复需要；runtime保存固定镜像ID、版本／revision、容器ID及数据库／迁移基线来选取正确恢复材料。三份配置和ACL均不能在本单元省略；不额外备份R2、SSH、Docker登录配置或控制面凭据。

敏感来源限定上述已绑定常规文件，拒绝符号链接／父链逃逸／多硬链接及可被非批准身份写入的父目录。Compose允许批准属主的0600或0644；env必须0600；ACL必须0600且属主对应已验证Redis uid（或明确root管理身份），不强制把所有输入改成root属主。不得为通过检查chmod／chown源文件。生产归档执行者root、产物／回执／私有日志／半成品0600、目录及每级新建解包目录0700、umask077；本地合成执行用当前euid验证，并mock错误uid/gid、0600/0644差异、异常中断及文件替换，不能据此证明root执行通过。

runtime字段白名单：schemaVersion/service；containers.app/postgres/redis下的name/configured_image/image_id/container_id/version/revision；database.user/database/migrations；backup下的protocolVersion、task／plan／preparation／lease／call关联、scope／资源摘要、四项非runtime产物的相对路径／role／format／size／sha256与子回执摘要、必要证据引用及一致性级别。资源原文仅保存批准的非敏感映射。拒绝env值、原始SQL、ACL哈希、密钥、原始owner token和容器环境／原始stderr。底层错误只输出稳定分类；敏感半成品与原始必要诊断保存在同一私有隔离区，不进入普通stdout／回执／指标。

半成品写到本次目录的 `.incomplete/`，完成后fsync文件、同文件系统原子发布、fsync父目录并复读。任一job失败不发布集合完成；保存本次各已完成产物及失败分类，不覆盖、不重用旧文件。失败／unknown时保留敏感半成品及证据，不新增年龄删除或保留期限。只允许在已证实无写者后处理本次明确创建的空目录／非敏感缓冲／已提交文件的临时名称；不得自动清理已提交备份、请求、回执、阻断记录或不明归属文件。

#### 机器回执、恢复消费者与锁／指标

集合回执v1包含protocolVersion、service、job、callId及四关联ID、operationDir、requestDigest、scopeDigest、resourceDigest、实际非敏感资源映射／观察证据、start/end、state（completed/failed/uncertain）、四子回执路径／摘要、五产物的role/path/format/sizeBytes/sha256、cleanup事实／证据引用。路径来自固定目录计算，不能用stdout最后一行；写入、复读、摘要或解析失败均不能完成。回执是观察证据索引，不是daemon结束证明。B1接收者必须另验后端Proof／实例／调用代次，不能将回执自报布尔值转换为Settled。

为避免循环摘要：PG／Redis／卷／config-capture子回执只分别覆盖SQL／RDB归档／data归档／configs，config-capture不得生成或引用runtime。metadata新增窄函数capture_scoped_configs仅采集configs及内存runtime基线；set-finalize再将四产物manifest与四收集回执摘要写入runtime v2、计算其摘要，最后集合回执绑定五项。metadata现有capture一次生成configs+runtime的行为只保留给legacy调用，不套入新依赖顺序。runtime不引用自身摘要或最终回执摘要。metadata仅从本次目录／固定请求读取四子回执，精确校验callId、task／lease、job集合、scope、声明资源、role、路径、字节数、摘要、完成状态和禁止重复；mtime只作辅助，旧文件、同hash跨调用、旧stdout路径不得进入新恢复点。

恢复点对Ops现有封套仍使用schemaVersion=1及每artifact四字段（role/path/sizeBytes/sha256），不向 `restore_contract.py:123-141` 的严格字段集合硬塞format，不改model／schema。新增格式与调用绑定放在有摘要保护的runtime v2。旧runtime v1仍走明确legacy消费者；不能伪造成v2或被限定更新链接收。旧恢复器会在runtime schema检查阶段拒绝v2；新版 `restore_contract.py`／metadata明确分派v1／v2，v2须校验四产物manifest及格式／作用域后方可stage。PG／tar字节布局未换成pg_dump，故不存在用旧psql误读custom dump的问题。

`restore_point_metadata.py` 的capture／validated_point／stage／verify-staged增加v2窄分支，保持三配置成员精确匹配，安全解包并复读权限／摘要。`restore-sub2api-isolated.sh` 只增加v2校验和selected产物传递，不改变prepare默认共享流程、迁移／smoke／发布分支。`restore-sub2api-production.sh` 在任何quiesce／staging前验证v2、归档配置、ACL与当前批准运行身份兼容；当前密码／配置与归档不一致则拒绝并报告类别，不打印秘密、不自动轮换／覆盖配置、也不退回v1。实际恢复角色、扩展、所有权与授权顺序保持原 `psql ON_ERROR_STOP=1`，对bootstrap冲突先拒绝；不忽略错误或降级。

v2恢复必须分开备份来源与恢复目标：归档container_id／run_id只证明来源，不作为当前目标ID。在quiesce／staging前，须有本次恢复批准所绑定的当前daemon、project、container ID／启动代次、三挂载源和目标配置证据；现有仅比镜像的检查不足。若现有恢复合同不能提供可信目标绑定，v2恢复保持拒绝，不能从归档推造，也不借B2a扩展公共审批模型。合成测试可以提供明确synthetic目标证明；真实映射是后续输入。合法恢复会切换三个目录并重建PG／Redis（production脚本303-307），后续verify／resume只接受本次恢复状态记录且实际核实的新目录／新容器身份，保留旧→新转换证据，第三值拒绝；不能永久要求目标ID／配置字节等于归档。这里只实现v2校验合同，不执行真实恢复或新增部分失败rollback。

`internal/runner/recovery.go:418-462` 现有verified主要覆盖身份／时间窗／路径／大小／摘要和必需role，不证明数据库可导入。拟仅在私有Sub2API受控路径新增五类精确集合与runtime v2绑定校验，旧恢复封套规范化不变；新脚本回执completed表示合同／产物完成，实际恢复演练另留drill证据、镜像／源码版本、恢复内容与权限比对，不复用verified或B1 settled作为演练成功。

互斥必须共享已有 `ops-backup-postgres.lock`、`ops-backup-redis.lock`、`ops-backup-volumes.lock` 的同一物理文件；新增set按固定postgres→redis→volumes顺序非阻塞获取，全程持有，未全拿到不开始备份。metadata只读配置，其同源归档无需新增共享configs锁；配置变更需上述稳定性／协调证明。共享单job只拿自身锁。B1当前要求lock路径含 `/sub2api/`（`sub2api_manifest.go:236-240`），不能另造不同锁满足字符串：B2a须把私有set状态与三共享锁显式分开建模、摘要／真实inode对应验证，并明确这些共享锁只是协调资源，不授权其他应用数据。

共享内部执行改用wrapper传入的继承锁FD及已校验内部调用协议；inner重新核对FD实际文件／锁及持久阻断记录，WRAPPED环境值不再足以直入。对外无参数对象范围／输出／timeout／指标保持；已有绕过变量不属于公开授权入口，测试替身改用显式内部合同。新增固定包内backup-control.json v1，默认mode=shared-only、协调根为空，允许旧无参数共享作业并拒绝sub2api-set；所有新入口校验同一包模式，文件纳入B1依赖图与implementationDigest。模式不能由请求／环境切换，配置缺失／损坏直接拒绝，不能视作旧模式。未来coordinated模式须另批完整新wrapper／inner／helper同版本部署及持久状态初始化，并证明没有旧入口绕过；本B只实现shared-only默认及合成coordinated测试，不部署／启用。

coordinated的启用哨兵、schema版本、实例映射与追加式调用账本放在批准的持久coordinationRoot（独立协调对象，不在/run、/tmp或operations）；/run/lock仅承载现有三把物理flock。持锁后先核验哨兵与账本，已启用但缺失／损坏／非终态均拒绝；不自动初始化、不因重启回退shared-only、不重新认领旧call。后台副作用前写入并fsync非终态记录，按callId索引，一次仅一个set；正常有后端终结证明才追加终态，保留历史。异常退出释放flock后，持久记录仍阻断共享与限定后续。B1保持uncertain；无自动重试／解锁／年龄清理。B2a只验证协议，真实结束Proof及初始化／人工解除流程后续另批，脚本自报done不能清除unknown。共享锁／coordinationRoot须绑定实际跨服务协调对象，不能伪造Sub2API独占objectId；B1仅增加必要表达和校验，若现有目录模型不能表达即拒绝，不扩大业务数据权限。

限定指标采用独立 `sub2api-backup-set.prom`：`service_backup_job_last_attempt_timestamp_seconds`、`service_backup_job_last_duration_seconds`、`service_backup_job_last_result`（1完成／0其他）、`service_backup_job_uncertain`、`service_backup_set_last_success_timestamp_seconds`，标签仅service=sub2api与job=postgres|redis|volumes|config-capture|set；不把callId／路径／token作标签。只有五产物终态复读通过才更新限定set成功时间。共享原 `backup-job-<job>.prom`、`backup_job_*`、freshness扫描及十角色manifest保持原语义；限定根独立，不能刷新全量成功或被R2自动同步。无新定时器、计费或自动清理。

实际旧消费者：`scripts/backup/cron/ops-backup-{postgres,redis,volumes}:5`（UTC02:10／02:30／03:30，发行路径无参数）；AreaForge适配器共享PG／卷；Sub2API普通backup；legacy未限定backup；`restore-sub2api-isolated.sh` prepare分支；`backup_manifest.py:24-33`十角色集合、`observability/scripts/write-backup-metrics.sh`固定glob。B2a覆盖上述调用形态的本地回归，不部署cron／发行目录、不运行同步，不泛化改造其他应用。

兼容补充：`backup-configs.sh:6-8` 仍依赖原WRAPPED标记。wrapper对legacy调用继续传该标记，三份受影响PG／Redis／卷脚本额外核验锁FD及协调合同，标记不再足以绕过这三者；不删除标记导致未修改configs脚本递归，也不让限定入口接收标记获得授权。共享configs公开职责不变。

#### 一次待批准 B2a 单元：文件、验证、依赖与回退

拟修改的实际文件：`scripts/backup/{backup-postgres.sh,backup-redis.sh,backup-volumes.sh,run-backup-job.sh,restore_point_metadata.py,restore_contract.py,restore-sub2api-isolated.sh,restore-sub2api-production.sh}`；新增同目录 `sub2api_backup.py`、`sub2api_backup_contract.py`、`sub2api_backup_archive.py`、`backup-control.json`（v1、shared-only默认与空协调配置，不填真实资源）。`backup_manifest.py`、`restore_env.py`保持既有公开合同，窄检查放新模块，不缩减全量manifest角色。

调用方修改：`services/areasong-ops/adapters/{compose-service.sh,sub2api.sh}`及 `scripts/deploy/update-control/adapters/sub2api.sh` 仅限定backup分支／参数传递；Runner候选 `internal/runner/{sub2api_bundle.go,sub2api_manifest.go,sub2api_resources.go,sub2api_receipt.go,recovery.go}`，新增 `sub2api_backup_receipt.go`，只适配helper依赖／共享锁映射／新回执和runtime绑定，不实现Linux后端／启动允许集合，不改schema48／49／50。

测试范围：新增 `scripts/backup/tests/test_sub2api_backup.py`、`test_sub2api_backup_contract.py`、`test_sub2api_backup_archive.py`；定向调整 `test_backup_job_runner.py`、`test_backup_redis.py`、`test_backup_volumes.py`、`test_restore_point_metadata.py`、`test_restore_contract.py`、`test_restore_sub2api_isolated.py`、`test_restore_sub2api_production.py` 与 `recovery_fixture.py`。调用方用 `services/areasong-ops/adapters/tests/{test_compose_service_adapter.py,test_sub2api_adapter.py,test_backup_recovery_points.py}`；Runner用 `sub2api_profile_test.go`、`sub2api_execution_test.go`、`sub2api_fixture_test.go`、新增 `sub2api_backup_receipt_test.go`、已有恢复点定向测试。已有README仅按需同步本合同入口和兼容说明，唯一阶段台账仍为本文件。

B本地验证命令形态：清洁环境Python3.12 `-B -m unittest` 逐项运行上述隔离模块（先审查新fixture／绝对路径）；`bash -n` 九脚本；Go在service目录执行 `go test -ldflags=-linkmode=external ./internal/runner -run 'Test(Sub2API|Recovery|Restore)' -count=1`，需要时仅扩大实际受影响测试，不要求四包／全站浏览器。新增测试必须记录命令与文件访问：正常、缺／多资源、额外库／角色、未知service、重复／冲突参数、环境覆盖、WRAPPED伪造、跨调用／旧产物、损坏／截断回执、替换路径／父链／硬链／挂载、权限与属主错误、PG失败／角色冲突、Redis已运行／快完成无法归因／超时断连／重启、失败保留与共享锁阻断、共享产物／指标回归。禁止资源设置成“被访问即失败”的Docker替身及文件访问哨兵（目录list/stat/open均审计），确认Account Vault／AreaForge／JadeAI／Ops无访问，不能只查归档文件名。root-only与真实挂载／daemon证明另留Linux验收。

A实际证据：[checks.json](../output/s3.4b3b/b2a/a/checks.json)、[tests.log](../output/s3.4b3b/b2a/a/tests.log)、[run_a_checks.py](../output/s3.4b3b/b2a/a/run_a_checks.py)。42项通过：Redis4、wrapper4、env5、restore_contract6、manifest19、metadata4；9脚本语法通过。真实脚本＋替身仅Redis和wrapper分支，env／合同是合成路径CLI，manifest／metadata是合成文件与mock runtime；没有真实PG／Redis／数据恢复。卷测试仍含未隔离绝对应用路径及 `/usr/bin/python3`，不运行；生产恢复及其整套替身测试本轮未运行；metadata的shell恢复分支未选入。首次证据脚本根目录索引错误导致测试加载失败、语法路径不存在，0个业务测试执行；已修正，仅最终运行计通过，失败原文保留。临时HOME／TMPDIR已清理，PATH中的端点命令均替身／拒绝stub，无真实socket／网络／数据库。

B授权窗口拟限定为“批准后的当前一次本地实施与合成验证，到交接停止”。开始先重新核对Git及本轮证据；只备份本单元将改的源码原字节到独立B证据目录，保留106项暂存及已有非暂存成果；不stash／reset／checkout／重放历史patch。失败只撤销可确认属于B的增量，不恢复整个索引，不删除失败档案。代码回退不能撤销BGSAVE／数据库副作用、已复制的秘密、数据恢复或迁移；如未来有真实非终态记录，禁止降级到绕过阻断的旧脚本，需另批恢复处置。B不执行上述真实行为，不暂存／提交／推送／安装／发布／部署。

缺失输入不妨碍本合同准备：真实prepared目标、离线镜像／对应源码与恢复演练、PG cluster专属／角色／扩展／bootstrap兼容证据、Redis全实例归属及发起者独占、真实资源／属主／挂载／端点映射、获批Linux隔离设施。不得自动选版本／下载／造真实记录。B3再验证GNU工具、flock／持久阻断、root权限、daemon exec／BGSAVE终结、配置／文件竞态、真实SQL导入后角色／权限／扩展／数据、Redis ACL认证和应用一致性；仅格式可读不算通过。schema48／49／50控制面恢复仍独立待办。

未来发行依赖：新helper与backup-control必须进入同版本发行包；当前 `ansible/observability-host-jobs.yml:67-85` 的安装清单尚未包含它们。B2a不修改或执行发行清单；后续发行准备必须单列，不能只拷贝新wrapper，否则默认共享调用会因依赖缺失而拒绝。真实coordinated启用另需批准持久根／初始化和全部旧入口治理；不能靠有限代码测试宣称旧生产作业已迁移。

治理短检查仅报告候选：B1单个service路径锁与共享互斥不相容；Redis备份ACL可选而恢复必需；新产物根如果位于全量同步树会产生意外外部归档；metadata的“private”当前只拒绝可写权限而非0600。未自动更新规则／清退历史引用。独立只读复核与最终保全结果续记下方。

**独立复核与A退出：** 默认配置只读子代理 `/root/b2a_contract_review`、fork_turns=none，未修改／运行测试／连接生产／派生。最初指出持久阻断初始化、metadata摘要循环、恢复来源与目标身份三项，主代理沿wrapper8／26-29、metadata134-153、production restore65-75／237-242／303-307复核并纳入上文；子代理复读确认三项合同已澄清，未发现原问题残余矛盾。[review.json](../output/s3.4b3b/b2a/a/review.json)记录取舍和未覆盖项。主代理另核对configs包装标记兼容，尚未进行实现验证。

起点main／HEAD=`c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca`；[baseline.json](../output/s3.4b3b/b2a/a/baseline.json)覆盖820份原有改动文件及scripts/local／服务output证据。最终核对819份非本次台账文件无变化，台账原362864字节前缀、40稳定ID保持，暂存106项binary diff SHA-256仍为 `1088fadfa396ffa326b7e87bb0f6e467b3bcd13a92473ae55034bd461906497e`。B1 patch仍为 `1ac6e33af28a06eaa0f6e0143985b9ddb327d93a0fe61d1ca831275d3efcdfca`，20文件集合仍为 `7caf2ee2222a20ecd98d4391c34aae093b18e7c25b67d3cd78b6d29516dca1fc`；完整检查见[preservation.json](../output/s3.4b3b/b2a/a/preservation.json)及[integrity.json](../output/s3.4b3b/b2a/a/integrity.json)。仅新增本节与A证据，不重放／暂存／提交／发布；A到此停止并申请上列一次B2a本地实施单元。B未批准，限定备份、敏感归档、恢复兼容均不能宣称已实现或已验收。

### S3.4b3b-B2a-B：服务限定备份合同与合成验证交付（2026-10-05，Asia/Shanghai）

**状态：completed——仅本次获批 B2a 本地合同实现、合成验证、独立只读复核及增量证据完成。真实接入／恢复／发行仍未完成，不据此启用或部署。** 承接用户对上节具体文件、参数、完整 env／必需 ACL／SQL 角色密码哈希及兼容影响的一次明确批准；未扩大到 smoke、真实迁移、部分失败 rollback、Linux 收敛后端、真实允许集合或安装清单。A 与全部历史正文保留；其“B 待批准”是历史状态，本节记录批准后结果。关联 OPS-07-01／E02，40 个稳定 ID 不变。

**实现与边界：** 34 个源码／测试／README 文件的实际增量见 [delta.json](../output/s3.4b3b/b2a/b/delta.json)。限定入口严格绑定 `sub2api-set`、固定 request/result 文件名、UUID／task／plan／preparation／lease／target、scope／implementation／父回执／资源摘要及 deadline；拒绝重复／未知字段、额外参数、旧结果、别名路径与环境覆盖。Compose→Sub2API→legacy 显式转发；wrapper 与监督器各自重核合同，限定分支先于共享配置读取。解释器固定为包内 `tools/python3` 并用隔离参数，shell 路径计算使用内建；PATH/HOME/TMPDIR 绑定到包与本次临时根。请求、WRAPPED 或 synthetic 标记均不能装配后端；真实后端仍不存在，命令行不能执行合成成功路径。

- PG 保留固定 `docker --host <unix endpoint> exec <container ID> env -i PATH=... pg_dumpall -U <role> -l <maintenance DB> --no-password` 参数和 cluster SQL＋gzip 格式。监督器用 Python gzip 的空文件名／mtime=0 流式压缩，不筛选单库、不丢 globals。后端必须给本次 exec／实例／退出码证据；额外库／角色、bootstrap postgres 冲突、专属／DDL 排他／恢复版本兼容缺证均拒绝。gzip 可读不等于 SQL 可导入。
- Redis 按全实例处理，来源 UID/GID 与 RDB inode／mtime／ctime／大小／摘要分别核验；本次接受、运行、实例代次、终结及安全复制缺证时保留不确定，不重发 BGSAVE。归档精确包含 metadata、RDB、必需 ACL。应用树以 fd 锚定、逐成员 no-follow；排除 logs 子树，不归档 operation／其他来源／输出／协调树。configs 精确两份 Compose 和完整 env；源权限不修改。
- 四产物→四子回执→runtime v2→五产物集合回执，避免循环摘要。私有 Runner 精确核对五角色、文件、子回执和 runtime，并把资源摘要／来源基线绑定私有后端观察；旧三角色消费者策略在私有 profile 拒绝，公共 model／schema 与旧共享策略不变。外层恢复点仍 v1，runtime 单独 v2。
- 新目录0700，产物、半成品、回执和私有诊断0600；发布 fsync 并复读，不覆盖旧调用。错误对外为固定类别，原始必要诊断留 `.incomplete/`。失败／晚到 proof／结果发布异常保留材料和持久阻断，无自动清理、接管、释放或降级。三把共享锁使用同一物理文件，协调资源使用独立 objectId；追加账本先登记再创建 start，记录缺失／损坏／非终态拒绝，重启不解除。
- 包内控制文件仍 `shared-only`／空协调根。**coordinated 下旧共享 shell 尚无可信终结后端，当前保持拒绝**；真实初始化、共存证明及启用仍属后续批准范围。独立 `service_backup_*` 指标不刷新旧全量指标；[旧合同核对](../output/s3.4b3b/b2a/b/legacy-contract.json)证明三共享备份主体、原指标发布段、configs、十角色 manifest、freshness 与 cron 原字节保持。
- v2 恢复校验四产物格式与绑定，合成目标 verifier 必须独立绑定本次恢复 task／plan、目标 daemon／project／容器代次／挂载及配置和 bootstrap 兼容。真实 CLI 无此能力时在 staging／quiesce 前拒绝 v2，不从来源归档推造目标、不回退 v1、不覆盖当前秘密、不忽略 SQL 错误。v2 合成暂存逐级0700、创建即0600，复验全树权限与三份配置字节。真实旧→新目标转换核验和恢复执行仍待后续后端；本轮未执行真实恢复。

| 实际验证 | 证据与结论 |
| --- | --- |
| Python 合成／旧调用方回归 | [最终结果](../output/s3.4b3b/b2a/b/checks/008/results.json)：15 模块、114 项测试通过，无跳过。使用既有 Python3.12、独立 HOME/TMPDIR、外部端点拒绝 stub 和显式替身；没有安装工具。旧绝对路径卷测试只在临时脚本副本精确替换默认根／前缀与解释器，未放宽产品规则。 |
| 路径／资源／失败验证 | 新限定测试覆盖禁止树绝对路径、相对 openat/dir_fd 与整数 fd list/stat/open 哨兵及反向自测；[访问／固定命令记录](../output/s3.4b3b/b2a/b/checks/008/synthetic-materials/access-sentinel.json)。涵盖额外库／角色、WRAPPED／环境／路径绕过、跨调用与旧子回执、敏感异常不泄漏、权限／UID/GID、失败材料、共享锁、缺失账本记录、Redis 快完成／失败／重启／断连、晚到 proof。合成成功和失败材料的 zip 留在同目录。 |
| Shell 与三层路由 | 9 份脚本 `bash -n` 通过；实际三层脚本→wrapper 重核→监督器链路在受控模拟解释器中执行，最终因无可信后端拒绝，不进入共享枚举。macOS 注入的 Cocoa 编码变量仅在测试工具中模拟 Linux 清洁启动消除，没有扩大产品环境白名单。 |
| Runner | [Go 日志](../output/s3.4b3b/b2a/b/checks/008/go-tests.log)：`go test -json -ldflags=-linkmode=external ./internal/runner -run 'Test(Sub2API\|Recovery\|Restore)' -count=1`，24 项顶层、含子测试134项通过，无跳过；GOPROXY/GOSUMDB关闭，GOTOOLCHAIN=local。临时 SQLite 的合成初始化／既有回归不代表 schema48／49／50 恢复兼容。 |
| 外部资源与独立复核 | [端点拒绝日志](../output/s3.4b3b/b2a/b/checks/008/denied.jsonl)为空；没有真实 daemon、数据库或网络连接。两路默认配置、fork_turns=none 子代理只读复核，问题与取舍见 [review.json](../output/s3.4b3b/b2a/b/review.json)；最后相关编辑后重跑。原失败日志不删除，[运行历史](../output/s3.4b3b/b2a/b/run-history.json)区分环境／夹具失败、修正与最终通过。 |

**保全与回退证据：** B 起点在任何产品修改前保存，[baseline.json](../output/s3.4b3b/b2a/b/baseline.json)覆盖1526份原文件指纹，[源码原字节归档](../output/s3.4b3b/b2a/b/source-baseline.zip)与 [106项暂存补丁](../output/s3.4b3b/b2a/b/index-baseline.patch)独立保存。HEAD 保持 `c0b4150ed5caf6a17c3e26faa9a69ee4fc1fc5ca`、main；暂存 binary diff SHA-256 仍为 `1088fadfa396ffa326b7e87bb0f6e467b3bcd13a92473ae55034bd461906497e`。1500份本轮未涉及的原文件及权限、B1／A／全部历史 output 与 scripts/local 保持；台账原392466字节前缀不改。

- [B2a独立增量 patch](../output/s3.4b3b/b2a/b/b2a-local.patch)：SHA-256 `2d510398089745d057a35397e6b86e3c68b5c8d64d70e1d74c25dec386bbaac1`。仅含34文件本次增量，排除旧阶段、台账和 output；文件集合（排序路径＋NUL＋原字节＋NUL）SHA-256：`c561c52e52220cf3f4a7f7b5bc230df6a2d3c762066874081999c58015d678e4`。
- [隔离重建](../output/s3.4b3b/b2a/b/reconstruction.json)：在独立临时目录从 B 原字节重建，`git apply --check`、正向逐字节、反向检查／还原及新增文件消失均通过；未把 patch 应用到现有工作区，未操作索引。台账另有 [ledger.patch](../output/s3.4b3b/b2a/b/ledger.patch)。
- [保全核验](../output/s3.4b3b/b2a/b/preservation.json)及 [完整指纹](../output/s3.4b3b/b2a/b/integrity.json)覆盖本轮证据；B1 patch仍为 `1ac6e33af28a06eaa0f6e0143985b9ddb327d93a0fe61d1ca831275d3efcdfca`。重叠源码的当前变化由B2a增量表示，不把B1旧文件集合冒充当前集合。源码回退只针对本次独立增量，不能撤销未来真实后台写入或解除持久阻断。

**保留项与停止：** 未连接生产、读取真实秘密、执行真实库迁移／恢复、安装、改全局配置、暂存、提交、推送、发布或部署。真实材料与专属／初始化兼容证明、root／Linux／挂载／daemon／SQL导入／ACL认证／应用一致性全部留B3；Ops schema48／49／50恢复兼容独立未完成。同版本helper／control／工具打包及真实协调根初始化、启用仍是发行前依赖；安装清单未修改。治理短检查仅报告候选：新wrapper缺同版本依赖会阻断共享作业；WRAPPED内部协议改变应进入未来发行说明；备份来源与恢复目标必须分开绑定。未新增治理规则、改inventory或清退历史引用。B2a本地单元到此停止。
