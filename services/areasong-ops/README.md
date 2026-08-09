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
- Runner 独占 `/var/lib/areasong-ops/ops.db`，通过 `root:areasong-ops 0660` Socket 提供 API。
- Runner 对每个服务加锁，备份/更新/恢复演练再加全局备份锁。
- 详细事件保留 30 天，任务和审计摘要保留 365 天，SQLite 快照及操作产物保留 30 天。
- AreaForge 使用发布自带签名 manifest 与严格 V2 request guard；Sub2API 只接受已固定摘要并完成迁移演练的 allowlist 目标。

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
- `/etc/areasong-ops/web.env`：Access issuer、audience、允许邮箱和 public origin。
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

- Runner：恢复上一 commit 的二进制、adapter 和服务声明，只重启 `areasong-ops-runner.service`。
- Web：恢复 Compose env 中上一 commit tag，只重建 `web`。
- Nginx：恢复上一站点文件，`nginx -t` 后 reload。
- Access：删除或禁用本 Application 前先确认不会留下公开源站；源站仍由 Cloudflare CIDR allowlist 保护。
- 保留 SQLite、任务产物与审计；不自动恢复 SQLite 或任何业务数据库。

详细生产检查见 [deploy/deploy-checklist.md](deploy/deploy-checklist.md)，Access 见 [deploy/cloudflare-access.md](deploy/cloudflare-access.md)。
