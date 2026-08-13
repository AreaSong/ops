# AreaSong Ops

AreaSong Ops 是 `ops.areasong.top` 的受控交互式运维控制面。它只开放 root-only 服务声明中的类型化能力，不提供任意 Shell、文件管理、Compose 编辑、批量更新或数据库自动恢复。

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
- Runner 对每个服务加锁，备份/更新/恢复演练再加全局备份锁。
- 变更先形成持久化发布计划；批准绑定不可变 SHA-256 摘要，执行前重新核对运行身份、目标和动作声明，任何变化都会使批准失效。
- 生产变更任务成功后进入声明的观察窗口；到期后重新核对运行身份并原子写入收口审计，才将计划标记为完成。
- Runner 从本机 Alertmanager 只读投影 Git 声明映射的活动阻断告警；Prometheus 仍是唯一告警规则源，Alertmanager 仍是告警和静默的唯一权威。
- 中高风险生产变更执行前必须通过告警门禁。Runner 只按声明创建最长 4 小时的精确维护静默，操作者不能输入 matcher、告警名或时长。
- 任务失败时提前解除维护静默；任务成功时保留到观察期结束。收口先解除静默，再复核包括被其他静默覆盖的活动阻断告警和运行身份。
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
```

## 构建

Web 镜像和 Runner 必须来自同一个 40 字符 Git commit：

```bash
docker build --target runner-export --output type=local,dest=build/runner \
  --build-arg BUILD_VERSION=1.0.0 --build-arg BUILD_REVISION=<commit> .

docker build --target web -t areasong-ops-web:<commit> \
  --build-arg BUILD_VERSION=1.0.0 --build-arg BUILD_REVISION=<commit> .
```

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

1. 备份当前配置、二进制、镜像身份并确认完整备份集。
2. 创建 `areasong-ops` 系统组和 root-only 目录。
3. 将既有 GitHub 告警同步凭据安全迁移到 Runner 专用凭据目录，切换两条 cron 的固定配置与锁路径。
4. 构建并安装 Runner，安装 adapter 和 `services.json`。
5. `systemd-analyze verify` 后启动 Runner，核对 Socket owner/mode 与 `/healthz`。
6. `docker compose config --quiet` 后构建、启动 Web，只验证 loopback health。
7. 安装 Nginx 站点，`nginx -t` 通过后 reload。
8. 创建 Cloudflare DNS/Access 并完成邮箱、JWT、CSRF 与源站限制验收。
9. 接入 Prometheus、告警、Grafana、备份和 inventory 后再宣布完成。

每一步都是独立生产变更，必须说明影响与回滚并单独批准。首次部署只运行 inspect/check，不用 restart/update/rollback/backup/restore-drill 做 smoke。

## 回滚

- Runner：恢复上一 commit 的二进制、adapter 和服务声明，只重启 `areasong-ops-runner.service`。SQLite 迁移只增列和建表，旧版本不会读取新字段；回退前保留当前 SQLite 快照，不能用旧二进制写入新任务。
- Web：恢复 Compose env 中上一 commit tag，只重建 `web`。
- Nginx：恢复上一站点文件，`nginx -t` 后 reload。
- Access：删除或禁用本 Application 前先确认不会留下公开源站；源站仍由 Cloudflare CIDR allowlist 保护。
- 保留 SQLite、任务产物与审计；不自动恢复 SQLite 或任何业务数据库。

详细生产检查见 [deploy/deploy-checklist.md](deploy/deploy-checklist.md)，Access 见 [deploy/cloudflare-access.md](deploy/cloudflare-access.md)。
