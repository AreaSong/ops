# AreaSong Ops 部署检查清单

> 这是分阶段变更与验收门禁，不是生产已部署声明。每个带写入或流量影响的阶段都要单独说明影响、批准人、观察窗口和回滚点。

独立功能的范围、前置证据和回滚步骤见 [production-change-packages.md](production-change-packages.md)。

## 阶段 0：变更冻结与批准

- [ ] 记录目标 commit、变更编号、操作人、批准人、维护窗口和预计影响。
- [ ] 记录当前 Runner/Web/Nginx/Compose/image digest、SQLite 快照和最近完整备份 manifest；确认回滚文件可读。
- [ ] 明确本次是否涉及 Compose apply、生命周期状态、Kubernetes dry-run/apply、生产恢复或多服务器批量；未列出的能力默认不执行。
- [ ] 生产恢复至少有创建人和独立批准人两方确认；普通版本回滚确认不能替代生产恢复审批。

## 部署前

- [ ] 当前 `/opt/ops` commit 与批准 commit 一致且工作树无非预期变更。
- [ ] `127.0.0.1:3200` 未占用。
- [ ] `areasong-ops` 组已创建，并记录 GID 到 Compose env。
- [ ] Cloudflare Access Application AUD 已写入 `/etc/areasong-ops/web.env`。
- [ ] Grafana HTTPS origin 已写入 `OPS_GRAFANA_URL`，且不包含路径、查询或认证信息。
- [ ] 本机 `http://127.0.0.1:9093/-/ready` 返回成功，Runner 只连接 loopback Alertmanager v2 API。
- [ ] schema 4 目录包含受信适配器注册表，服务与自动任务的 `objectId`、metadata 和 `adapterRef` 稳定；告警 matcher 精确，维护静默白名单为阻断映射子集。
- [ ] 每个服务/自动任务的 `tenantId`、`serverId` 与 inventory 对齐；`statePolicy` 的默认目标、维护 TTL、排空超时和 `autoReconcile` 经审核，默认不自动协调生产。
- [ ] `access` 的 default tenant、principal SHA-256、角色 permission、对象范围和过期时间已核对；无绑定主体、越租户和越对象请求预期为拒绝。
- [ ] `fleet` 只登记已批准的 server/Runner、能力标签和心跳超时；`allowRemoteRunners`、mTLS 和在线租约状态符合窗口要求，示例单机不等于允许跨机执行。
- [ ] `extensions.enabled` 默认关闭；若启用，trusted publisher、`areasong-ops.extension` 用途签名、`wasm` 沙箱和最小权限逐项留证。
- [ ] Kubernetes 目标的 cluster/context/namespace、resourceKinds 和对象 allowlist 已核对；没有 kubeconfig、token 或任意 manifest 路径进入 Git/浏览器。
- [ ] schema 4 适配器及全部服务 hook 只输出带匹配 `schemaVersion: 2`、action 和 phase 的单个 JSON 对象。
- [ ] 使用恢复点的服务声明完整 `requiredArtifactRoles` 和 1 小时至 7 天有效期；通用 Compose 服务配置专属 `backupEvidenceExecutable`。
- [ ] 自动任务调度仍由现有 cron/systemd 管理；补跑只包含已审查的固定采集器，不接受 unit、脚本、命令、参数、target 或 source 输入。
- [ ] 当前 Runner、Compose、Nginx 和 Web image 身份已保存为回滚点。
- [ ] 先执行 `migrate_github_credential.py --validate-source`，再执行 `--apply`；工具必须按旧锁、新锁顺序互斥，将旧 4 键规范化为固定 8 键并原子创建目标，目标已存在且不一致时拒绝覆盖。
- [ ] 执行 `migrate_github_credential.py --validate-destination` 后再切换 cron；两条新 cron 和 Runner smoke 均按旧锁、新锁顺序取锁。旧配置只作为部署回滚点保留，不能进入普通备份。
- [ ] 最近完整备份 manifest 与 R2 校验均有效。

## 控制面统一发布入口（C0）

- [ ] 发布参数通过 `deploy/release-orchestrator.sh` 进入；禁止临时拼接 Runner/Web 部署命令。
- [ ] manifest、Sigstore bundle、Runner checksum、Web digest 和完整 revision 已互相绑定并验证。
- [ ] 生产 `/opt/ops` 已在批准 revision，工作树干净；入口不会隐式 checkout/pull。
- [ ] deployment ID 唯一且可重放；同 ID 制品摘要漂移、已回滚或 `needs_attention` 均拒绝继续。
- [ ] 备份 Runner/updater/unit、Web env、Compose、image inspect 和 SQLite snapshot；备份目录已有残留时拒绝覆盖。
- [ ] Runner 先于 Web 安装和健康验证；Web 只按 immutable digest 拉取并 `--force-recreate --no-deps`。
- [ ] 每个阶段状态与脱敏审计原子落盘；不记录环境文件、Token、密码或命令输出。
- [ ] 任一步失败立即停止后续阶段，仅按实际已改变组件逆序回滚；回滚验证失败进入 `needs_attention` 并保留证据。
- [ ] 生产入口固定 root-only 路径；测试必须设置 `OPS_RELEASE_TEST_MODE=1` 并使用临时隔离目录。

## 阶段 1：离线构建与静态门禁

- [ ] 仅在工作树/隔离目录运行 JSON schema、`jq empty`、Go、适配器、Shell、前端和 Docker Compose 静态检查。
- [ ] 验证没有任意 Shell/任意文件写入口；适配器路径均为 root-owned 普通文件、owner-execute，配置 `0600`。
- [ ] 验证 Compose 候选只能生成 propose/validate revision；未经摘要审批不能触碰运行 Compose、依赖容器、宿主机文件或公网流量。
- [ ] 验证 Kubernetes 仅能生成声明目标的 dry-run 记录；apply 能力未获单独批准前保持关闭。

## 阶段 2：安装与只读启动

- [ ] 先安装 Runner、适配器、schema 4 配置和 Web，不执行 update、restart、rollback、backup、restore-drill、Compose apply 或数据库操作作为 smoke。
- [ ] `systemd-analyze verify`、Socket owner/mode、非 root Web、只读 rootfs、无 Docker Socket/业务卷/备份目录挂载均通过。
- [ ] `/healthz`、`/metrics`、Cloudflare Access JWT/CSRF、Alertmanager loopback readiness 和 Nginx `-t` 通过。
- [ ] 只执行 `inspect`、`check`、fleet inventory/heartbeat 读取和 Kubernetes dry-run；保存返回的 revision、身份、租户和服务器证据。

## 阶段 3：隔离能力验收

- [ ] 生命周期动作 `enter-maintenance`、`drain`、`resume-traffic`、`start`、`stop` 在隔离服务上验证预览、确认、RBAC、锁、目标状态、健康检查和审计。
- [ ] `stop`/`start` 验证网站保护顺序；`drain` 保存旧 worker 与活动连接归零或明确超时的证据，失败保持维护页并进入 `needs_attention`。
- [ ] Compose 依次验证 `validate`、`propose`、摘要 digest、审批失效条件、受控 apply、health/smoke/identity、观察窗口和失败回滚；验证任意路径、依赖容器和过期 digest 被拒绝。
- [ ] 恢复演练只使用 `isolated` 模式和临时资源，验证备份角色、SHA-256、expected-before、清理和生产数据不变；不得以演练替代生产恢复批准。
- [ ] fleet 批量只在非生产或明确隔离目标上验证 selector、DAG、canary、并发上限、暂停/失败策略、变更窗口、心跳租约和审计；禁止通配符扩大目标；审批使用创建人/独立批准人两方模型。
- [ ] Runner Fleet 自更新验证创建人与独立批准人分离、显式目标、v2 签名心跳、mTLS 指纹、制品/策略摘要、assignment fencing、Canary 观察、临时错误退避、重启回执恢复、失败停止和逐节点回滚；生产开关仍保持关闭。
- [ ] WASM 扩展验证用途隔离签名、独立批准、创建人执行、输入/输出/内存/超时限制以及无宿主文件、网络、环境变量和 Docker Socket 权限；生产开关仍保持关闭。
- [ ] Kubernetes 只验证声明 allowlist 内的 dry-run、manifest digest、命名空间和资源 kind；任何 apply 另建计划并单独批准。

## 阶段 4：生产变更与观察收口

- [ ] 每个生产动作单独创建预览/计划，摘要固定 service、tenant、server、target digest、影响、回滚、告警门禁和观察秒数。
- [ ] 公开计划创建携带 UUID 幂等键；需要延迟执行的计划验证 `scheduleAt`、`scheduled` 状态和到时前拒绝执行。
- [ ] 中高风险动作先检查 Alertmanager 活动阻断告警；通过后才创建声明生成的精确静默，不能由操作者输入 matcher/告警名/时长。
- [ ] Compose apply 或版本更新必须先有 fresh recovery point；生产恢复必须另有双确认、恢复点复验和目标身份复核。
- [ ] 观察期内保持任务/静默/恢复点证据；失败提前解除静默并进入人工关注，成功收口前重新 inspect、解除静默、复核告警后再标记完成。
- [ ] 多服务器批量必须逐批观察；任一失败按声明策略 stop/pause/rollback/needs_attention，不自动跳过红线或扩大下一批。

## 阶段 5：记录与交接

- [ ] 更新 `inventory/services.yaml`、端口、备份覆盖、Grafana/告警和 runbook；记录实际 revision、审批和验证输出。
- [ ] 变更记录明确“已验证能力”和“未执行能力”；没有完成生产窗口和 runtime preflight，不得宣称生产已部署。

## 离线门禁

- [ ] `CGO_ENABLED=0 go test ./...`
- [ ] adapter Python tests、`bash -n`、`shellcheck` 通过。
- [ ] `npm run lint && npm run typecheck && npm run build` 通过。
- [ ] Runner export 与 Web Docker image 构建通过。
- [ ] 发布 manifest 使用 schema 2 和规范 `sha256:<64>` Runner 摘要；下载后的 checksum 仅引用归档 basename，并通过 `verify-release-assets.sh` 独立校验 Runner blob 与 Web 镜像的固定 GitHub Actions/Cosign 身份。
- [ ] `docker compose config --quiet` 通过。
- [ ] Nginx 配置在隔离前缀或生产 `nginx -t` 通过。
- [ ] `deploy/preflight.sh source` 通过；安装文件后 `installed` 模式通过。

## 上线验收（仅在批准窗口执行）

- [ ] `areasong-ops-runner.service` active，Socket 为 `0660 root:areasong-ops`。
- [ ] Web 容器为非 root、rootfs 只读、未挂载 Docker Socket。
- [ ] `http://127.0.0.1:3200/healthz` 返回 `200`。
- [ ] Web、Runner `/metrics` 可由本机 Prometheus 抓取。
- [ ] Cloudflare Access 未登录、错误邮箱、正确邮箱三条路径符合策略。
- [ ] AreaForge/Sub2API inspect 与 check 为只读，返回真实生产身份。
- [ ] `/v1/objects` 汇总全部受管对象，`/v1/services` 保持旧客户端兼容，`/v1/automatic-tasks` 返回调度来源、新鲜度和最近成功证据。
- [ ] `/v1/states` 和服务 state 页面显示 desired/actual/health/drift、tenant/server；目标状态变化有 generation、原因和审计事件。
- [ ] fleet 页面只显示登记的 server/Runner、能力、心跳和 lease；离线、draining、disabled 状态不能被调度器当作可用目标。
- [ ] access 页面能显示租户、角色、binding 和当前 subject，但不显示凭据；修改 RBAC 必须具备 `access.manage` 并产生审计。
- [ ] Compose 页面区分当前 revision、候选 propose/validate revision 和已批准摘要；没有审批或 digest 漂移时 apply 按预期拒绝。
- [ ] Kubernetes 页面只显示登记目标、allowlist 和操作状态；默认 dry-run，manifest digest 与命名空间/资源 kind 一致。
- [ ] 自动任务页面可查看既有调度状态；只对运行资产快照和 Docker 运行指标显示固定补跑入口，运行期间正确锁定动作。
- [ ] `/v1/alerts` 只投影声明映射的活动阻断告警；Alertmanager 不可用时明确返回 `503`，其他只读页面仍可用。
- [ ] 凭据页只显示类型、目标、短指纹、到期日和轮换摘要；浏览器 localStorage/sessionStorage、Runner 日志、SQLite、审计和 API 响应均不包含 Token。
- [ ] `switched_pending_revocation` 超过 24 小时产生 warning，`needs_attention` 持续 5 分钟产生 critical；两者都由 Prometheus 规则统一告警。
- [ ] 隔离验收证明：身份/固定仓库访问/Issues 权限/到期日任一验证失败都不切换；真实同步或到期指标失败会恢复旧配置；旧 Token 未撤销时拒绝收口。
- [ ] 在隔离验收中证明：活动阻断告警拒绝生产执行，映射外告警不阻断，静默 matcher 与最长到期时间符合声明。
- [ ] 在隔离验收中证明：任务失败提前解除静默；任务成功进入观察，收口前解除静默并复核被其他静默覆盖的活动告警。
- [ ] 在隔离验收中证明：缺失必需备份角色、expected-before 漂移、备份文件篡改或恢复点过期都会在生产变更前失败关闭。
- [ ] SQLite 增量迁移后恢复点包含 expected-before 摘要与必需角色；有效恢复点和可回滚任务的操作目录不会被 30 天清理误删。
- [ ] 删除已不存在的静默按幂等成功处理；遗留静默可在 Alertmanager 以计划注释和精确 matcher 定位。
- [ ] 不执行 update、rollback、restart、backup 或 restore-drill 作为首次部署 smoke。
- [ ] 不以首次部署 smoke 执行自动任务补跑；首次补跑必须另行批准，并核对原指标保留、flock 互斥和原子发布证据。
- [ ] 生产恢复双确认、隔离演练与普通 rollback 三条路径分别验收；任何一条不能以另一条的成功记录代替。
- [ ] 多服务器批量红线验收：空 selector、跨租户目标、离线 Runner、无窗口、超过并发/队列或 wildcard 目标均拒绝。
- [ ] Prometheus 规则、中文告警、Grafana 自监控面板通过。
- [ ] inventory、端口、备份覆盖与 runbook 已更新并提交。
- [ ] `deploy/preflight.sh runtime` 证明 Web/Runner revision、Socket 权限和容器隔离一致。

## 回滚

- [ ] 回滚触发条件、当前生产是否已改变、观察窗口、影响告警和批准人已记录；不确定时按已改变处理并停止自动重试。
- [ ] 恢复上一 Runner 二进制并重启单个 Runner unit。
- [ ] 将 Compose env 恢复为上一 Web commit tag并只重建 Web。
- [ ] 如 Nginx 新站点异常，恢复配置后 `nginx -t` 再 reload。
- [ ] Compose apply 失败时只恢复上一受控 revision；不得直接从用户输入覆盖运行文件，依赖容器和数据库 schema 保持人工核对。
- [ ] Kubernetes 变更失败时保留 manifest digest、dry-run/apply 证据，按同一受控 manifest 回滚；不删除 namespace/PV，不执行未批准的广泛清理。
- [ ] fleet 批量失败时停止后续批次，锁定已变更节点，按 failure policy 逐节点回滚或转人工；不能用全局重启止血。
- [ ] 生产恢复失败时保留恢复点和隔离/生产日志，暂停后续恢复；再次执行必须重新双确认，不能自动重试或覆盖业务数据库。
- [ ] 保留 `/var/lib/areasong-ops` 和审计证据，不恢复任何业务数据库。
- [ ] 如凭据路径迁移异常，暂停两条 GitHub Issue cron，恢复旧 cron/config 路径和旧 Runner；旧锁继续阻止迁移与旧任务并发，确认同步指标恢复后再解除暂停。
