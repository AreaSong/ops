# AreaSong Ops 生产功能变更包

> 本文件把已实现能力拆成可独立批准的生产变更包。它不是执行授权，也不是生产已启用证明。每个包都必须在有效变更窗口内通过 `la-share` 执行，完成备份、验证、审计和回滚记录后才能关闭。

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
- **当前状态**：代码和本地/生产基线已验收；不重复执行 AreaForge stop/start。

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
