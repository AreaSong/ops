# AreaSong Ops

> 本 README、`config/services.example.json` 和 `deploy/` 文档描述控制面代码与配置的目标状态及验收门禁；它们不是“已部署到生产”的证明。生产上线必须按阶段取得批准并完成只读验收。

AreaSong Ops 是 `ops.areasong.top` 的受控交互式运维控制面。它只开放 root-only 服务声明中的类型化能力：不提供默认任意 Shell 或文件写入；Compose 只能走 propose/validate、摘要审批和受控 apply；生产恢复与多服务器批量均是独立高风险流程。

## 运行边界

```text
Cloudflare Access -> Nginx -> 非 root Web 容器 -> Unix Socket -> root Runner
                                                                  |
                                      固定适配器 -> Docker/备份/更新器
                                                                  |
                                            loopback Alertmanager v2 API
```

- Web 只接收并校验 Cloudflare Access JWT，向 Runner 传递邮箱 SHA-256。
- Web 不接触 Docker Socket、SQLite、备份目录或业务卷。
- Runner 独占 `/var/lib/areasong-ops/ops.db`，通过持久目录中的 `root:areasong-ops 0660` Socket 提供 API；Runner 重启不会使 Web 的 bind mount 失效。
- 没有默认的任意 Shell、任意路径文件写入、动态可执行路径或任意 Compose/Kubernetes 目标；所有可执行能力必须来自 root-owned 适配器、声明的路径和固定 allowlist。
- Compose 编辑只允许提交候选内容或执行离线 validate；只有 expected digest 未漂移、校验通过、计划摘要已批准且满足备份/观察门禁时，受控适配器才可 apply 到声明的 Compose 副本。
- Kubernetes 目标只接受 `config/services.example.json` 中登记的 cluster/context/namespace、资源 kind 和对象 allowlist；网页输入不能扩大目标范围，默认只做 dry-run/检查。
- 扩展默认关闭；启用时必须使用受信发布者、用途隔离签名和 `wasm` 沙箱，扩展权限不能越过对象和租户边界。
- Runner 对每个服务加锁，备份/更新/恢复演练再加全局备份锁。
- 变更先形成持久化发布计划；批准绑定不可变 SHA-256 摘要，执行前重新核对运行身份、目标和动作声明，任何变化都会使批准失效。
- 公开计划创建必须携带幂等键；计划固定租户、服务器和可选 `scheduleAt`。未来调度在时间到达前保持 `scheduled`，重复请求只追加重放审计，不重复创建任务。
- 生产变更任务成功后进入声明的观察窗口；到期后重新核对运行身份并原子写入收口审计，才将计划标记为完成。
- Runner 从本机 Alertmanager 只读投影 Git 声明映射的活动阻断告警；Prometheus 仍是唯一告警规则源，Alertmanager 仍是告警和静默的唯一权威。
- 中高风险生产变更执行前必须通过告警门禁。Runner 只按声明创建最长 4 小时的精确维护静默，操作者不能输入 matcher、告警名或时长。
- 任务失败时提前解除维护静默；任务成功时保留到观察期结束。收口先解除静默，再复核包括被其他静默覆盖的活动阻断告警和运行身份。
- `ops.areasong.top` 是控制面自身域名，永远不能出现在任何 `trafficPolicy.hostname`，避免控制面被自己的流量开关切断。
- 自动更新维护窗口使用显式 IANA `maintenanceTimezone`（缺省为 `UTC`），启用时强制 `requireApproval`、`requireBackup` 和 `rollbackOnAlert` 同时为真；扩展、终端、文件、Runner 更新等能力继续默认关闭。
- 任务持久化阶段、心跳、生产变更事实与恢复能力；Runner 重启后，未触碰生产的任务可重新计划，生产可能已改变的任务只允许人工核对。
- AreaForge 与 Sub2API 的备份阶段必须返回服务专属恢复点。Runner 校验声明的全部必需产物角色、路径、大小、时间和 SHA-256，并把恢复点绑定到批准时的变更前身份；每个变更阶段执行前都会重新核验。
- 有效恢复点和仍可回滚任务的操作目录不会被定期清理；恢复点按服务声明的 1 小时至 7 天窗口过期，过期后才重新进入普通产物留存清理范围。
- 服务页从 SQLite 恢复最近一次成功的发布发现结果；准备发布完成后同步恢复 prepared 门禁状态。
- 任务、审计和任务事件支持分页读取，前端不会把首批 100/200 条误当成完整保留记录。
- 详细事件保留 30 天，任务和审计摘要保留 365 天，SQLite 快照及操作产物保留 30 天。
- AreaForge 使用发布自带签名 manifest 与严格 V2 request guard；Sub2API 只接受已固定摘要并完成隔离迁移、恢复和旧镜像兼容演练的动态 prepared 目标。
- `schemaVersion: 4` 将服务和自动任务统一为受管对象；对象通过 `adapterRef` 引用顶层受信适配器注册表，不能自行声明可执行路径，适配器输出必须包含匹配的 v2 动作和阶段身份。
- 自动任务页只汇总既有 cron/systemd 任务的状态和新鲜度。调度配置仍以 cron/systemd 为权威，网页不能修改调度、unit、脚本、命令或参数。
- 首批补跑白名单仅包含运行资产快照和 Docker 运行指标两个低风险采集器；备份、清理、发布、凭据、网络、权限和数据库任务不开放补跑。
- 凭据页仅开放固定的 GitHub 告警 Issue 同步 Token。新值只经 HTTPS、Unix Socket 与 Runner 内存传递，不进入浏览器持久化、SQLite、事件、日志、Git 或普通备份；验证身份、GitHub 签发方到期日、固定仓库访问与 Issues 读写能力后原子切换并执行真实同步，失败自动恢复旧配置。
- 成功切换后轮换状态保持为“等待撤销旧凭据”；只有 GitHub API 确认旧 Token 已失效，Runner 才删除隔离回滚副本并将轮换标记为完成。
- Runner 暴露活动轮换状态及持续时间；等待撤销超过 24 小时或进入人工关注状态时，由 Prometheus 唯一规则源告警。

## Schema 4 与控制面能力

完整字段说明、示例和迁移注意事项见 [docs/control-plane-schema.md](docs/control-plane-schema.md)。示例目录现在包含：

- 每个服务/自动任务的 `tenantId`、`serverId`、能力声明；服务还声明 `statePolicy`，但 `autoReconcile` 默认关闭。
- `access` 租户、principal、角色和对象范围绑定。principal key 是 Cloudflare Access 邮箱规范化后的 SHA-256，不把邮箱明文当作授权标识。
- `platform-reader` 仅授予控制面资源的读取范围，不能修改 RBAC、Fleet、凭据、扩展或其他平台配置；平台写入仍需对应管理权限和审批。
- `fleet` 的 server/Runner 清单、心跳租约和能力标签；当前示例只登记一台服务器，不代表已启用跨机生产执行。
- `extensions` 签名与沙箱策略（默认 `enabled: false`）以及受控 Kubernetes 目标清单。

### 生命周期动作

当服务 `metadata.lifecycle` 为 `active` 时，Runner 会动态生成 `enter-maintenance`、`drain`、`resume-traffic`、`start` 和 `stop`。这些动作不需要在每个服务的 `actions` 中重复声明；它们仍需预览、确认、RBAC、服务锁和审计。声明 `trafficPolicy` 后，`stop`/`start` 会组合流量保护、应用变更和健康检查；`drain` 必须等待活动连接归零或明确超时。生产 C2 仅对 `service:areaforge` 的 `start`/`stop` 允许同一操作者审批自己的计划，且必须在签名摘要和审计中标记 `c2_lifecycle_single_actor`；其他高风险操作仍保持独立双人审批。详细状态转换见 [docs/control-plane-schema.md](docs/control-plane-schema.md)。

### 高风险边界

- 生产恢复不能由普通更新或回滚动作隐式触发；必须选择明确的恢复点和 `production` 模式，执行双人/双确认，完成备份证据、目标身份和影响范围复核。`isolated` 恢复演练与生产恢复严格分开。
- 多服务器批量是红线能力：必须显式目标 selector/目标列表、DAG、并发上限、失败策略、变更窗口和 canary/观察；禁止通配符扩大范围、默认并行或跨租户混跑。首次接入和生产批量都要单独批准。
- 任何失败只按声明的阶段策略处理。生产是否已改变不确定时按“已改变”处理，停止自动重试并进入人工核对；不自动恢复业务数据库。

## 通用服务模板

`compose-service-v1` 把 Compose 应用的检查、备份、单服务重建、健康检查、发布发现和 prepared 门禁统一到一个适配器。新增服务通常只需要一份 `schemaVersion: 4` 受管对象声明；数据库恢复、迁移验证或认证 smoke 等服务特有逻辑通过少量 root-owned hook 接入。Runner 仍兼容读取既有 schema 3 服务目录，schema 4 新声明必须使用受信适配器引用。

模板字段、hook 契约、接入步骤和安全边界见 [deploy/service-template.md](deploy/service-template.md)。模板不会自动恢复生产数据库、修改公网流量或启用自动更新。

自动任务的对象模型、固定补跑白名单和接入审查见 [deploy/automatic-task-template.md](deploy/automatic-task-template.md)。新任务默认只开放 `inspect`，经过风险审查后才能加入补跑白名单。

## 本地验证

```bash
cd /opt/ops/services/areasong-ops
CGO_ENABLED=0 go test ./...
python3 -m unittest discover -s adapters/tests -v
bash -n adapters/*.sh
shellcheck adapters/*.sh

cd web
npm ci
npm run lint
npm run typecheck
npm run build
# 需要 Web、Runner 和开发身份同时在线
OPS_PLAYWRIGHT_URL=http://127.0.0.1:4173 npm run smoke:playwright
```

本地要逐页验收默认关闭的终端、受管文件、扩展、Runner 单机/Fleet 更新，可在开发 Runner
启动时显式设置 `OPS_DEV_ENABLE_FEATURES=all`。该开关只存在于 `cmd/dev-runner`
的开发入口，会把文件根目录和 Runner 制品目录重映射到临时目录，并保留只读终端
和人工批准门禁；扩展上传仍强制签名，并额外信任 RFC 8032 的公开测试向量发布者
`AreaSong Development`；Fleet 页面使用开发态 v2 Runner 身份，不会建立生产 mTLS 通道或执行
真实 Runner 更新。生产 Runner 不识别此开关，生产 `services.json` 的默认关闭策略不变。
如需在本地演练 Break-glass Shell，再额外设置 `OPS_DEV_ENABLE_BREAK_GLASS=1`；该
开关不会被 `OPS_DEV_ENABLE_FEATURES=all` 隐式打开。
需要验收平台级写能力时，必须再显式设置 `OPS_DEV_ADMIN_EMAIL=<开发邮箱>`；该变量
只由 `cmd/dev-runner` 读取，并只给对应开发身份临时加入 `platform-admin`，生产 Runner
和生产访问策略均不识别此开关。需要验收三方或四方独立批准链路时，使用
`OPS_DEV_ADMIN_EMAILS=<邮箱1>,<邮箱2>,<邮箱3>,<邮箱4>` 显式登记多个开发身份；各身份仍需
通过独立 Web 会话发起请求，不能由页面切换或请求参数伪造。

## 构建

Web 镜像和 Runner 必须来自同一个 40 字符 Git commit：

```bash
docker build --platform=linux/amd64 --target runner-export --output type=local,dest=build/runner \
  --build-arg BUILD_VERSION=1.0.0 --build-arg BUILD_REVISION=<commit> .

docker build --platform=linux/amd64 --target web -t areasong-ops-web:<commit> \
  --build-arg BUILD_VERSION=1.0.0 --build-arg BUILD_REVISION=<commit> .
```

发布合同固定为 `linux/amd64`；在 Apple Silicon 本机上省略 `--platform` 会生成
`aarch64` 制品，不能上传到生产发布流程。

生产 Compose 位于 `/opt/services/areasong-ops/compose.yml`，来源为 [deploy/compose.yml](deploy/compose.yml)。真实配置位于：

- `/etc/areasong-ops/services.json`：root-only 受管对象、受信适配器和能力声明。
- `/etc/areasong-ops/web.env`：Access issuer、audience、允许邮箱、public origin 和 Grafana origin。
- `/opt/services/areasong-ops/.env`：构建版本、commit 和 Runner 组 GID。
- `/var/lib/areasong-ops/credentials/alertmanager-github.env`：root-only 类型化凭据配置，由 Runner 原子维护。

`/opt/ops/services/areasong-ops` 是 Git 管理的受控源码；`/opt/services/areasong-ops`
只保存运行 Compose 和非敏感构建参数。部署前后分别执行只读预检：

```bash
sudo /opt/ops/services/areasong-ops/deploy/preflight.sh installed
sudo /opt/ops/services/areasong-ops/deploy/preflight.sh runtime
```

## 部署顺序

1. **准备与冻结**：确认批准 commit、备份 manifest、当前 Runner/Web/Nginx 身份和回滚窗口；复制配置备份，不改生产流量。
2. **离线门禁**：校验 JSON/schema、root-owned 路径、适配器契约、Go/适配器/Web 测试、Compose config 和 Nginx test。
3. **安装但不开放写能力**：创建 root-only 目录，安装 Runner、适配器和 `services.json`；先以 inspect-only/禁用扩展状态启动。
4. **组件验收**：逐项验证 Socket、非 root 只读 Web、Cloudflare Access、Alertmanager、Prometheus、fleet 心跳和 Kubernetes dry-run 投影。
5. **受控能力验收**：只在隔离环境验证 Compose propose/validate/摘要审批、生命周期状态转换、备份/恢复演练和批量计划；不执行生产恢复或跨机批量。
6. **单独变更窗口**：每个生产 update/restart/Compose apply/生产恢复/多服务器批量都单独说明影响、回滚和批准人；观察窗口收口后才记录为完成。
7. **记录与交接**：更新 inventory、端口、备份和审计记录；没有完成上述门禁时，不得写“生产已部署”。

每一步都是独立生产变更，必须说明影响与回滚并单独批准。首次部署只运行 inspect/check 和必要的 dry-run，不用 restart/update/rollback/backup/restore-drill 或生产恢复做 smoke。

## 回滚

- Runner：恢复上一 commit 的二进制、adapter 和服务声明，只重启 `areasong-ops-runner.service`。SQLite 迁移只增列和建表，旧版本不会读取新字段；回退前保留当前 SQLite 快照，不能用旧二进制写入新任务。
- Web：恢复 Compose env 中上一 commit tag，只重建 `web`。
- Nginx：恢复上一站点文件，`nginx -t` 后 reload。
- Access：删除或禁用本 Application 前先确认不会留下公开源站；源站仍由 Cloudflare CIDR allowlist 保护。
- 保留 SQLite、任务产物与审计；不自动恢复 SQLite 或任何业务数据库。
- Compose 候选内容或 Kubernetes manifest 只保留带摘要的提案/验证记录；apply 失败时恢复上一受控 revision，不接受直接覆盖运行文件。
- 生产恢复回滚不是普通版本回滚：停止变更、保留证据并重新走双确认和恢复点核对，不能用旧二进制或批量任务代替。

详细分阶段检查见 [deploy/deploy-checklist.md](deploy/deploy-checklist.md)，schema/生命周期/fleet/Compose/Kubernetes 见 [docs/control-plane-schema.md](docs/control-plane-schema.md)，Access 见 [deploy/cloudflare-access.md](deploy/cloudflare-access.md)。

## 控制面统一发布入口

Web 与 Runner 的生产发布统一使用 [`deploy/release-orchestrator.sh`](deploy/release-orchestrator.sh)。
入口只接受 GHCR 固定 digest 和签名 Runner 归档，要求生产 `/opt/ops` 已处于批准 revision；它不会
隐式执行 Git checkout/pull。执行链固定为：只读 preflight、备份（Runner/updater/unit、Web env、
Compose、image inspect、SQLite snapshot）、Runner 安装与健康验证、Web digest 拉取和单容器重建、
runtime preflight、审计收口。状态和审计保存在 root-only 的 deployment 目录，失败自动按已改变组件
回滚并保留证据。使用 `plan` 可只创建可审计计划，`status` 查看状态；生产 `deploy`/`rollback` 必须
root 执行并拒绝任意路径覆盖。

签名发布的 manifest 使用 schema 2：Web 必须绑定 revision 与不可变镜像 digest，Runner 归档名必须绑定同一 revision，`sha256` 必须是规范的 `sha256:<64>`；配套 checksum 只能引用归档 basename。发布端和下载端都必须运行 `deploy/verify-release-assets.sh`，拒绝 CI 绝对路径、身份漂移、文件名漂移、摘要不一致以及不属于本仓库发布工作流的 Runner/Web 签名。
