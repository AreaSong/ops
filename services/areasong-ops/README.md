# AreaSong Ops

AreaSong Ops 是 `ops.areasong.top` 的受控交互式运维控制面。它只开放 root-only 服务声明中的类型化能力，不提供任意 Shell、文件管理、Compose 编辑、批量更新或数据库自动恢复。

## 运行边界

```text
Cloudflare Access -> Nginx -> 非 root Web 容器 -> Unix Socket -> root Runner
                                                                  |
                                      固定适配器 -> Docker/备份/更新器
```

- Web 只接收并校验 Cloudflare Access JWT，向 Runner 传递邮箱 SHA-256。
- Web 不接触 Docker Socket、SQLite、备份目录或业务卷。
- Runner 独占 `/var/lib/areasong-ops/ops.db`，通过持久目录中的 `root:areasong-ops 0660` Socket 提供 API；Runner 重启不会使 Web 的 bind mount 失效。
- Runner 对每个服务加锁，备份/更新/恢复演练再加全局备份锁。
- 变更先形成持久化发布计划；批准绑定不可变 SHA-256 摘要，执行前重新核对运行身份、目标和动作声明，任何变化都会使批准失效。
- 任务持久化阶段、心跳、生产变更事实与恢复能力；Runner 重启后，未触碰生产的任务可重新计划，生产可能已改变的任务只允许人工核对。
- AreaForge 与 Sub2API 的备份阶段必须返回服务专属恢复点，Runner 复核产物路径、大小和 SHA-256 后才允许进入更新阶段。
- 服务页从 SQLite 恢复最近一次成功的发布发现结果；准备发布完成后同步恢复 prepared 门禁状态。
- 任务、审计和任务事件支持分页读取，前端不会把首批 100/200 条误当成完整保留记录。
- 详细事件保留 30 天，任务和审计摘要保留 365 天，SQLite 快照及操作产物保留 30 天。
- AreaForge 使用发布自带签名 manifest 与严格 V2 request guard；Sub2API 只接受已固定摘要并完成隔离迁移、恢复和旧镜像兼容演练的动态 prepared 目标。

## 通用服务模板

`compose-service-v1` 把 Compose 应用的检查、备份、单服务重建、健康检查、发布发现和 prepared 门禁统一到一个适配器。新增服务通常只需要一份 `schemaVersion: 2` 服务声明；数据库恢复、迁移验证或认证 smoke 等服务特有逻辑通过少量 root-owned hook 接入。

模板字段、hook 契约、接入步骤和安全边界见 [deploy/service-template.md](deploy/service-template.md)。模板不会自动恢复生产数据库、修改公网流量或启用自动更新。

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

- `/etc/areasong-ops/services.json`：root-only 服务能力声明。
- `/etc/areasong-ops/web.env`：Access issuer、audience、允许邮箱、public origin 和 Grafana origin。
- `/opt/services/areasong-ops/.env`：构建版本、commit 和 Runner 组 GID。

`/opt/ops/services/areasong-ops` 是 Git 管理的受控源码；`/opt/services/areasong-ops`
只保存运行 Compose 和非敏感构建参数。部署前后分别执行只读预检：

```bash
sudo /opt/ops/services/areasong-ops/deploy/preflight.sh installed
sudo /opt/ops/services/areasong-ops/deploy/preflight.sh runtime
```

## 部署顺序

1. 备份当前配置、二进制、镜像身份并确认完整备份集。
2. 创建 `areasong-ops` 系统组和 root-only 目录。
3. 构建并安装 Runner，安装 adapter 和 `services.json`。
4. `systemd-analyze verify` 后启动 Runner，核对 Socket owner/mode 与 `/healthz`。
5. `docker compose config --quiet` 后构建、启动 Web，只验证 loopback health。
6. 安装 Nginx 站点，`nginx -t` 通过后 reload。
7. 创建 Cloudflare DNS/Access 并完成邮箱、JWT、CSRF 与源站限制验收。
8. 接入 Prometheus、告警、Grafana、备份和 inventory 后再宣布完成。

每一步都是独立生产变更，必须说明影响与回滚并单独批准。首次部署只运行 inspect/check，不用 restart/update/rollback/backup/restore-drill 做 smoke。

## 回滚

- Runner：恢复上一 commit 的二进制、adapter 和服务声明，只重启 `areasong-ops-runner.service`。SQLite 迁移只增列和建表，旧版本不会读取新字段；回退前保留当前 SQLite 快照，不能用旧二进制写入新任务。
- Web：恢复 Compose env 中上一 commit tag，只重建 `web`。
- Nginx：恢复上一站点文件，`nginx -t` 后 reload。
- Access：删除或禁用本 Application 前先确认不会留下公开源站；源站仍由 Cloudflare CIDR allowlist 保护。
- 保留 SQLite、任务产物与审计；不自动恢复 SQLite 或任何业务数据库。

详细生产检查见 [deploy/deploy-checklist.md](deploy/deploy-checklist.md)，Access 见 [deploy/cloudflare-access.md](deploy/cloudflare-access.md)。
