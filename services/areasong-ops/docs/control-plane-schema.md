# AreaSong Ops 控制面 Schema 与边界

> 本文是 schema 4 的设计和验收说明，不是生产部署证明。示例配置可以被开发 Runner 读取，但只有完成 [部署检查清单](../deploy/deploy-checklist.md) 的阶段门禁、批准和 runtime preflight，才可以在生产窗口启用对应能力。

## 1. 根目录模型

`config/services.example.json` 的根对象使用 `schemaVersion: 4`。schema 4 的可执行路径只能从顶层 `adapters` 受信注册表解析；服务或自动任务通过 `adapterRef` 引用注册项，不能在请求中提交脚本路径、命令、参数或任意 source 目录。

```text
catalog
├── adapters       root-owned 适配器注册表
├── access         租户、principal、角色和对象范围绑定
├── fleet          server/Runner 清单与心跳策略
├── extensions     签名和沙箱策略（默认关闭）
├── kubernetes     受控 cluster/context/namespace 目标
├── services       服务对象
└── automaticTasks cron/systemd 的只读治理投影
```

schema 3 只为迁移兼容保留；新对象和新文件必须使用 schema 4。生产副本 `/etc/areasong-ops/services.json` 应由 root 拥有并设为 `0600`，适配器和 hook 应是 root-owned 普通文件、不可由组/其他用户写入。

## 2. 受管对象字段

服务和自动任务共享 `name`、`objectId`、`metadata`、`adapterRef`、`actions` 等字段，并可补充：

| 字段 | 作用 | 门禁 |
| --- | --- | --- |
| `tenantId` | 租户归属 | 必须与 `access.tenants` 和调用主体租户一致；不会单独授予权限 |
| `serverId` | 目标 server 归属 | 必须能在 `fleet.inventory.servers` 中定位；Runner 不在线时不得执行 |
| `capabilities` | 能力标签 | 只用于选择和展示，真正能力由受信 adapter/hook 固定 |
| `statePolicy` | 服务目标状态默认值 | `defaultDesired` 为 `running/stopped/maintenance/drained`；维护 TTL 60 秒至 7 天；`autoReconcile` 默认 `false` |
| `metadata.lifecycle` | 对象生命周期 | `proposed/onboarding/active/maintenance/retiring/retired` |
| `metadata.maturity` | 能力成熟度 | `disabled/inspect_only/shadow/manual_approval/automated`；提案或 inspect-only 对象只能开放只读动作 |

`objectId` 是审计和 RBAC 的稳定身份，不能因为显示名或服务器迁移而复用。`retiring`、`retired` 或 `maturity: disabled` 对象不能继续开放动作。

## 3. Access 与 RBAC

Cloudflare Access 负责验证入口身份，Runner 负责授权。Web 将规范化（去空格、转小写）邮箱计算 SHA-256，并只把 hash 传给 Runner；因此示例中的 `access.principals` key 和 `emailHash` 是 64 位十六进制值，不能把邮箱明文、浏览器角色或请求体里的 `tenantId` 当作授权依据。

`access` 的关键字段：

- `defaultTenant`：缺省租户，仅用于补齐缺省值，不绕过 binding。
- `principals`：subject hash 到租户和基础角色的映射。
- `tenants`：租户状态和审计元数据。
- `roles`：权限集合，例如 `ops.read`、`ops.inspect`、`ops.lifecycle`、`ops.deploy`、`ops.recover`、`ops.batch`、`fleet.manage`、`access.manage`、`config.manage`。
- `bindings`：subject + tenant + role + `objectIds` 的窄范围授权，可设置 `expiresAt`。空 `objectIds` 只在经过审查的全租户角色中使用。

建议的最小角色边界：

| 角色 | 允许 | 不允许 |
| --- | --- | --- |
| `viewer` | 读对象、状态、告警和审计 | 所有写入、生命周期、恢复和批量 |
| `platform-reader` | 读取访问、Fleet、策略、凭据元数据和能力状态 | 所有平台配置、凭据轮换、节点登记、终端和其他写入 |
| `operator` | 单服务生命周期、恢复演练和只读检查 | 发布、Compose apply、生产恢复、fleet/RBAC 管理 |
| `release-manager` | 已绑定对象的发布、生命周期、备份/演练 | 修改租户/角色、扩展、任意 server |
| `platform-admin` | 仅在单独 break-glass 审计下管理平台策略 | 不得把 wildcard 权限当作日常账号 |

权限检查必须同时满足主体、租户、对象范围、角色、过期时间和动作映射。Access 登录成功但没有匹配 binding 时应返回 `403`。RBAC 修改本身是高风险配置变更，需备份、审批、审计和可回滚版本。

## 4. 生命周期与状态

当服务 `metadata.lifecycle` 为 `active` 时，Runner 可动态生成以下动作；不需要在每个 JSON `actions` map 中重复写一份：

| 动作 | 目标状态 | 主要副作用 | 失败处理 |
| --- | --- | --- | --- |
| `enter-maintenance` | `maintenance` | 写入目标状态和 TTL，阻止新的自动变更 | 状态写入失败进入人工关注 |
| `drain` | `drained` | 排空新请求，等待现有请求结束 | 超时保持人工核对，不强制恢复流量 |
| `resume-traffic` | `running` | 恢复目标状态和流量 | 需重新 inspect/health |
| `start` | `running` | 通过 adapter 启动应用并健康检查 | 失败按运行时变更策略处理 |
| `stop` | `stopped` | 停止应用，可能造成公网不可用 | 高风险确认，失败按已改变处理 |

动作仍需预览、确认短语、RBAC、服务锁、心跳和审计。网站 `stop` 固定执行 `preflight -> drain -> enter-maintenance -> stop -> health`，`start` 固定执行 `preflight -> enter-maintenance -> start -> health -> resume-traffic -> verify`；健康失败或恢复流量失败都保持维护状态并进入 `needs_attention`。`drain` 必须证明旧 worker 与活动连接都归零，或达到声明超时。`desired`、`actual`、`health` 和 `drift` 是不同维度：目标状态由控制面写入，实际状态由 inspect/reconcile 观察；发现漂移不能自动假定恢复成功。

全局高风险计划默认使用两方策略 `two_party_v1`：创建人创建计划，独立批准人批准，创建人执行；创建人与批准人必须是不同主体，批准请求和执行请求均可安全幂等重试。历史空策略/`legacy_four_party` 记录继续按四方身份链解释。经批准的 C2 例外仅适用于生产 `service:areaforge` 的 `start`/`stop` 生命周期计划：允许同一操作者审批自己的计划。例外以 `approvalException: c2_lifecycle_single_actor` 写入签名摘要和审计。

### ReleasePlan 合同

所有公开生产写请求必须携带 UUID `idempotencyKey`。计划摘要和计划外壳同时固定 `tenantId`、`serverId`、目标、影响、回滚、版本/配置快照和 `trafficPolicyDigest`；重复提交同一幂等键只返回原计划，并追加重放审计，不会创建第二个任务。

`state` 的批准/执行顺序为 `pending_approval -> scheduled -> approved -> executing -> observing -> completed`（没有未来 `scheduleAt` 时批准直接进入 `approved`）。进入 `scheduled` 后，只有受控 cron/systemd 或人工补跑在 `scheduleAt` 到达后才能原子释放为 `approved`；执行接口不会提前绕过时间门禁。公开 API 只允许创建、批准和查看计划，不能直接写 desired state。

## 5. Compose 受控提案流

控制面没有默认任意 Shell/文件写能力。Compose 变更必须遵循以下顺序：

1. **inspect**：读取当前受控 revision、运行 Compose 身份、容器 allowlist 和 expected digest。
2. **validate**：校验候选文本的 Compose 语法、固定服务/依赖边界、资源和日志约束；允许非循环 YAML anchor/alias 复用只读配置块，但拒绝 merge key、循环引用和危险字段；失败只返回诊断，不写运行文件。
3. **propose**：把候选内容写入 root-owned 提案目录，生成不可变 SHA-256 digest、操作者和过期时间；候选不等于已生效。
4. **approve**：计划摘要固定 service、tenant、server、digest、影响、备份、观察窗口和回滚；具备 `config.manage`/`ops.deploy` 的主体逐字确认，任何字段变化都使批准失效。
5. **apply**：受信 adapter 再次核对 digest、声明路径、当前身份、告警门禁和 fresh recovery point，只能更新声明的受控 Compose 副本并重建允许的应用服务。
6. **observe/close**：health、smoke、identity 和观察期全部通过后才收口；失败时恢复上一受控 revision，不自动恢复业务数据库。

以下输入永远不从网页或 API 透传到 shell：任意可执行文件、重定向、宿主路径、Docker Socket、Compose project name、依赖容器列表、环境文件路径、网络/端口和 `kubectl` 命令。提案/验证记录也不能替代生产批准。

## 6. 恢复边界

`restore-drill` 只在隔离环境使用最近完整备份，验证恢复点角色、文件普通属性、时间、大小、SHA-256、expected-before 和健康结果。它不会切换生产数据。

生产恢复是单独的高风险操作：

- 选择明确的 recovery point 和 `mode: production`，不能由 update/rollback/Compose apply 隐式触发。
- 执行前需要操作者和第二确认人两次独立确认（恢复点/目标与影响/窗口），并重新核对备份证据、租户、server、数据库目标和告警状态。
- 生产恢复失败或目标身份不确定时停止自动重试，保留日志和恢复点，进入人工关注；不执行“顺手再试一次”。
- 普通版本 rollback 只恢复受控应用身份/Compose，不自动回放数据库；它不能代替生产恢复双确认。

## 7. Fleet 与多服务器批量

`fleet.inventory.servers` 描述 server，`runners` 描述其执行器；节点状态为 `unknown/online/offline/draining/disabled`，Runner 以心跳租约证明在线。`allowRemoteRunners: false` 和 `requireMTLS: true` 是示例的保守默认；真实启用前要完成证书、租约和网络边界验收。Runner 自更新还必须显式设置 `runnerUpdate.fleetEnabled: true`，否则 Fleet API 和协调器保持关闭。

批量任务必须携带明确的 `targetIds` 或带标签/能力的 `targetSelector`，并通过 DAG、批次策略、并发策略、失败策略和变更窗口验证：

- 批次策略只能是 `serial`、`fixed`、`percentage` 或 `canary`；生产默认 serial/canary，先观察再继续。
- 并发必须声明 `global`、`per_runner` 或 `per_server` 上限和队列限制；不能用 0 或隐式无限并行。
- 失败策略必须显式选择 stop/continue/rollback/pause/needs_attention；生产不允许静默跳过失败节点。
- **多服务器批量是红线**：禁止 wildcard/空 selector、跨租户混跑、离线 Runner、无变更窗口、未经审批的全量 rollout，以及把一次批准当作后续批量授权。任一节点失败都要停止后续批次或按批准的回滚策略逐节点处理。

Runner Fleet 自更新计划必须进一步绑定签名制品摘要、策略摘要、每个目标的 mTLS 指纹和 v2 心跳身份，并要求创建人与独立批准人分离、由创建人执行；首批只能是 Canary，观察窗口结束且身份复验通过后才释放后续批次。失败会停止后续批次，已成功节点逐节点回滚，无法确认旧身份时进入 `needs_attention`。

远程 Runner 在本地持久化 assignment 回执和 fencing token，重启后从 `prepared/launching/launched` 状态继续，不能重新猜测或重复激活。控制面 408/425/429、5xx、网络中断和响应超时采用最长 30 秒的指数退避；401/403、mTLS 身份、签名、策略摘要、制品摘要和 assignment fence 不一致立即失败。传输中断可重新下载，但长度、摘要或响应身份不一致不得降级放行。

示例只登记 LosAngeles 一台 server/Runner；这表示 schema 的 inventory 形状，不表示已经获准执行多服务器生产任务。

## 8. 扩展与 Kubernetes

### Extensions

`extensions.enabled` 默认是 `false`。当前执行器只接受显式 `sandbox: wasm`，开启前必须登记 `trustedPublishers`、Ed25519 公钥并强制 `requireSignature: true`。扩展 manifest 固定 `purpose: areasong-ops.extension` 和 `schemaVersion: 1`，用途、版本、digest、publisher、只读权限和 `allowedObjects` 均纳入签名及审计；执行计划还必须绑定租户、目标对象、输入摘要、签名 manifest 摘要、执行策略摘要、超时、包/输入/输出上限和内存上限，审批后任何策略或发布者公钥变化都会阻止执行。上传、计划、独立批准、创建人执行和终态均写入审计；WASM 不启用宿主文件预开放、网络、环境变量或 Docker Socket。脚本类型可以登记为历史制品，但不能进入执行计划。没有独立批准时，保持关闭比添加空权限更安全。

扩展执行计划状态为 `pending_approval -> approved -> running -> succeeded/failed/needs_attention`（历史四方记录可出现 `pending_second_approval`），计划过期后不可批准或执行。创建人、独立批准人和执行人身份按两方策略绑定，重试只能由同一执行主体复用同一执行幂等键；Runner 重启发现 `running` 计划时必须进入 `needs_attention`，禁止猜测执行结果。WASI 仅开放 `fd_read`（受控 stdin）、`fd_write` 和 `proc_exit`，不开放文件、网络、环境变量或宿主句柄。

### Kubernetes

顶层 `kubernetes` 只保存受控目标元数据：`cluster`、`context`、`namespace`、`allowlist` 和 `resourceKinds`。操作必须固定目标并记录 manifest digest；默认先 dry-run。Kubeconfig、token、证书和任意 manifest 文件不得进入 Git、浏览器 localStorage 或普通日志。真正 apply 需要独立计划、RBAC、告警门禁、namespace/kind/object allowlist 和回滚方案；禁止从请求拼接 `kubectl` 命令，也不执行 namespace/PV 等破坏性删除。

## 9. 分阶段上线与回滚

上线顺序固定为：

```text
冻结/备份 -> 离线门禁 -> 安装只读组件 -> inspect/check/dry-run
       -> 隔离能力验收 -> 单项生产计划 -> 观察收口 -> 台账交接
```

每一步都应在变更记录中标出“执行了什么”和“明确未执行什么”。回滚顺序通常是 Runner/adapter 与 schema、Web image、Nginx；Compose 只恢复上一受控 revision，fleet 批量停止后续批次，Kubernetes 保留 manifest 证据并按批准路径回滚。SQLite、审计、任务产物和业务数据库均不得被旧版本自动覆盖。

完成判定必须同时有：离线测试结果、`preflight.sh` source/installed/runtime 结果、组件健康、RBAC 负向用例、隔离恢复演练、Compose 提案流、生命周期状态、fleet/Kubernetes 只读验收和批准记录。缺少任一项时，只能报告“代码/配置已准备或部分验收”，不能报告生产已部署。
