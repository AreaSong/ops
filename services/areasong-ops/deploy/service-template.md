# AreaSong Ops 通用服务模板

`compose-service-v1` 用一份声明复用控制面的网页、确认短语、任务阶段、服务锁、审计、备份、重启、发布发现、准备门禁、更新和回滚。模板适合“一个应用容器加若干依赖容器”的 Compose 服务。

## 接入边界

通用适配器负责：

- 比对 Git 管理的 Compose 与运行副本。
- 调用声明的分类备份作业。
- 只重建 `applicationService`，并验证依赖容器身份未变化。
- 只访问 `127.0.0.1` 或 `localhost` 的 HTTP health 地址。
- 查询 GitHub 最新发布，并读取静态或动态 prepared 记录。
- 把服务特有的恢复演练、发布准备、更新和回滚委托给固定 hook。

模板不提供任意 Shell、任意路径文件写入、生产数据库恢复或自动更新。Compose 内容如需调整，只能走控制面的 `propose`/`validate` 流程：候选内容写入受控提案目录，校验 expected digest、服务 allowlist、Compose 语法和安全边界；摘要进入发布计划并由具备权限的操作者批准后，固定适配器才可在声明的受控路径执行 `apply`。任何 digest 漂移、路径变化、审批过期或验证失败都必须拒绝 apply。涉及迁移、认证 smoke、数据一致性和旧版本兼容时，必须提供服务特有 hook。

生产恢复不属于通用 Compose apply 或 rollback：只能从已验证恢复点创建独立恢复计划，`isolated` 演练和 `production` 恢复分开；生产恢复必须有两名独立确认、备份证据和目标身份复核。批量任务也不由模板隐式开启，必须经过 fleet 目标、DAG、并发、失败策略和变更窗口门禁。

## 最小声明

```json
{
  "name": "demo",
  "objectId": "service:demo",
  "metadata": {
    "type": "service",
    "environment": "production",
    "owner": "operations",
    "criticality": "important",
    "lifecycle": "proposed",
    "maturity": "inspect_only"
  },
  "displayName": "Demo",
  "description": "Demo Web 与 PostgreSQL",
  "template": "compose-service-v1",
  "adapterRef": "compose-service-v1",
  "tenantId": "production",
  "serverId": "losangeles",
  "capabilities": ["docker.compose", "backup", "restore-drill", "lifecycle"],
  "statePolicy": {
    "defaultDesired": "running",
    "maintenanceTtlSeconds": 14400,
    "drainTimeoutSeconds": 300,
    "autoReconcile": false
  },
  "recoveryPointPolicy": {
    "requiredArtifactRoles": ["postgres-demo", "volume-demo-data"],
    "recoverableSeconds": 604800
  },
  "alertPolicy": {
    "matchers": {"service": "demo"},
    "blockingAlerts": ["AppHttpProbeFailed", "AppBlackboxTargetDown"],
    "maintenanceAlerts": ["AppHttpProbeFailed"]
  },
  "runtime": {
    "controlledCompose": "/opt/ops/services/demo/compose.yml",
    "runtimeCompose": "/opt/services/demo/compose.yml",
    "envFile": "/opt/services/demo/.env",
    "applicationService": "demo",
    "applicationContainer": "demo",
    "dependencyContainers": ["demo-postgres"],
    "healthUrl": "http://127.0.0.1:8090/health",
    "releaseRepository": "owner/demo",
    "releaseCatalog": "/opt/ops/services/demo/releases.json",
    "preparedReleaseDir": "/var/lib/areasong-ops/prepared-releases/demo",
    "inspectExecutable": "/usr/local/libexec/areasong-ops/hooks/demo-inspect.sh",
    "backupEvidenceExecutable": "/usr/local/libexec/areasong-ops/hooks/demo-backup.sh",
    "prepareExecutable": "/usr/local/libexec/areasong-ops/hooks/demo-prepare.sh",
    "updateExecutable": "/usr/local/libexec/areasong-ops/hooks/demo-update.sh"
  }
}
```

完整受管对象目录使用 `schemaVersion: 4`，并在顶层 `adapters` 注册由 root 管理的适配器路径及允许对象类型。对象只能用 `adapterRef` 引用注册项，不能直接声明任意可执行路径。Runner 兼容读取既有 schema 3 服务目录，但所有新增或迁移声明必须采用 schema 4。

对象可以声明 `tenantId`、`serverId` 和 `capabilities`；服务可用 `statePolicy` 指定默认目标状态、维护 TTL、排空超时和是否允许自动协调。`tenantId`/`serverId` 不会把请求变成跨租户或跨服务器授权，实际访问仍由 `access` 的角色绑定和 fleet 状态共同决定。

`objectId` 是稳定治理身份；`metadata` 记录对象类型、环境、责任域、重要级别、生命周期和成熟度。新对象默认使用 `lifecycle: proposed` 与 `maturity: inspect_only`，只开放 `inspect`、`check` 等只读动作；完成接入验证后才能显式提升生命周期和成熟度。`retiring`、`retired`、`disabled` 对象不能开放动作。

`alertPolicy.matchers.service`
必须精确等于服务名。`blockingAlerts` 只保存 Git 管理的处置映射，不复制 Prometheus 规则；
`maintenanceAlerts` 必须是其子集。维护静默不得覆盖 Blackbox 抓取链路、备份、审计、通知链路、
控制面或恢复失败告警。matcher、告警名和最长 4 小时时长均由声明与观察窗口生成，网页不允许输入。

动作仍在同一服务对象的 `actions` 中声明。更新动作使用 `targetMode: signed_release_tag` 和 `readinessGate: prepared_release`；准备动作使用相同目标格式并在成功后发布动态 prepared 记录。包含 `runtime_mutation` 或 `data_mutation` 的生产变更动作必须声明 60 到 86400 秒的 `observationSeconds`，该值会进入不可变批准摘要。

`metadata.lifecycle: active` 的服务还会得到 Runner 动态生成的 `enter-maintenance`、`drain`、`resume-traffic`、`start`、`stop` 动作。它们不需要复制到 JSON；调用路径仍必须经过预览、RBAC、确认短语、服务锁和审计。维护/排空动作只改变目标状态，启动/停止动作才委托适配器。

## Hook 契约

Runner 以以下参数调用所有适配器和 hook：

```text
<action> <phase> <operation-dir> <target> <source-dir>
```

schema 4 对象成功时 stdout 必须只输出一个 v2 JSON 对象：

```json
{"schemaVersion":2,"action":"update","phase":"health","ok":true,"summary":"阶段完成","data":{}}
```

Runner 会拒绝缺少 v2 身份、动作/阶段身份不匹配、尾随第二个 JSON 或其他多余输出。schema 3 旧目录暂时兼容 legacy 输出，但不能作为新接入方式。错误说明写到 stderr，并以非零状态退出。hook 必须是 root 拥有的普通文件、不可由组或其他用户写入，并设置 owner execute。服务声明中的所有路径必须为绝对路径。

变更动作应为每个阶段声明 `phaseSemantics`，明确 `effect`、`failurePolicy`、恢复点产消关系和回滚阶段。Runner 按阶段策略决定失败、人工关注或执行声明的恢复阶段，不再按动作名称推断。schema 3 未声明阶段语义时只保留兼容默认值。

产生恢复点的备份阶段还需在顶层返回 `recoveryPoint`：它必须绑定当前 service/task，列出受控备份目录中的产物角色、绝对路径、大小和 SHA-256。`recoveryPointPolicy.requiredArtifactRoles` 声明服务的完整备份集合，`recoverableSeconds` 必须为 3600 至 604800 秒。Runner 会：

1. 校验全部必需角色、文件路径、普通文件属性、证据时间、大小和 SHA-256。
2. 将证据摘要与任务的 `expected-before` 规范摘要一起持久化。
3. 在每个 `requiresRecoveryPoint` 阶段前重新读取并复验恢复点与文件。
4. 在有效期内保护恢复点所属操作目录；仍可回滚的任务目录同样不被清理。

`compose-service-v1` 配置恢复点策略后必须设置 `backupEvidenceExecutable`。通用适配器会把 `backup:preflight`、`backup:backup` 和 `backup:verify` 原样委托给该服务专属 hook；此时 `backupExecutables` 不参与这三个阶段。没有数据库或恢复点要求的简单服务才使用通用 `backupExecutables`。

中高风险生产变更执行前，Runner 从本机 Alertmanager 查询活动告警，包括已被人工静默覆盖的告警。
命中 `blockingAlerts` 或 Alertmanager 不可用时拒绝执行；通过后才创建精确维护静默并与任务原子关联。
生产变更任务成功只会让计划进入观察状态。观察窗口结束后，Runner 先解除维护静默，再复核活动阻断告警、
执行固定 inspect 并核对目标版本或运行身份；验证和收口审计在同一事务完成后，计划才进入完成状态。
任务失败时提前解除静默并进入人工关注。观察期间不得把任务成功当作计划已经收口。

### Compose 受控变更契约

1. `GET`/inspect 只读取当前受控 revision 和运行身份。
2. `mode: validate` 只解析和检查候选内容，不落盘运行 Compose；`mode: propose` 只在受控提案目录创建带 expected digest 的 revision。
3. 计划摘要固定候选 digest、目标服务、影响、回滚和观察窗口；批准人必须确认摘要，批准后任何内容变化都会使计划失效。
4. 受信适配器在 apply 前重新核对 digest、受控路径、容器 allowlist、备份和告警门禁；只允许重建声明的应用服务，不能修改依赖或写任意宿主文件。
5. apply 后执行 health、smoke、identity 和观察期收口；失败按阶段策略回滚到上一受控 revision，数据库不自动恢复。

发布准备成功后，以原子方式写入：

```text
/var/lib/areasong-ops/prepared-releases/<service>/<tag>.json
```

记录至少包含 `tag`、`status: prepared`、当前身份、目标镜像摘要、迁移数量和隔离演练证据。更新 hook 必须再次验证该记录和运行时 expected-before，不能只依赖网页按钮状态。

## 接入顺序

1. 固定受控 Compose、运行 Compose、env、容器名、health 和发布仓库。
2. 实现只读 inspect hook；有数据服务时实现分类备份和隔离恢复 hook。
3. 在隔离网络中验证 fresh backup、目标迁移、目标 health、认证 smoke 和旧镜像兼容；不得发布宿主端口。
4. 在 schema 4 顶层受信注册表中复用或新增适配器，再将受管对象声明加入 `services.json`；复核稳定 `objectId`、metadata、精确告警 matcher 和最小静默白名单，然后执行测试、构建和 `preflight.sh installed`。
5. 部署后先运行 inspect/check/prepare，核对 prepared 记录和证据摘要。
6. 只有 prepared 门禁通过后才开放目标更新；生产升级仍需单独确认。
