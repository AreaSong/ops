# AreaSong Ops 1.1.5 生产发布记录

日期：2026-09-05
变更窗口：2026-09-05 22:10–23:00（Asia/Shanghai）
范围：仅 AreaSong Ops Web 与 Runner

## 发布身份

| 项目 | 值 |
| --- | --- |
| 版本 | `1.1.5` |
| Git revision | `a232253805286d8a72ba8fda4453afda0c676916` |
| GitHub Release tag | `areasong-ops-a232253805286d8a72ba8fda4453afda0c676916` |
| GitHub Actions run | `33971156234`（success） |
| Web image digest | `sha256:59d9d6839dda3f474a154e8a52bd5d1edbce0c56971ebbaf896f8912ad13c014` |
| Runner bundle SHA-256 | `f87047befa20049440f5f58d79b494850cba2c12805d14bc25c9545bd1cada19` |
| Deployment ID | `ops-20260905T143013Z-2597b20be3e240c8ae502cf1c423dfc1` |
| 最终状态 | `succeeded` |

GitHub Release 和 GHCR Web 镜像均绑定完整 revision 与不可变摘要；Runner archive、checksum、
Sigstore bundle 和 Web 镜像签名在发布入口复验通过。

## 执行与回滚边界

- 使用 `la-share` 连接 LosAngeles，通过仓库内统一发布编排器执行；未使用旧的分散部署命令。
- 编排顺序为参数与签名校验、源码/生产预检、原子备份、Runner 更新、Web 重建、运行态预检。
- 回滚点位于
  `/var/lib/areasong-ops/release-orchestrator/deployments/ops-20260905T143013Z-2597b20be3e240c8ae502cf1c423dfc1/backup`；
  包含旧 Runner、Updater、两个 systemd unit、Web 环境、Compose、运行环境、脱敏 Web inspect
  和 SQLite 一致性快照。
- 1.1.4 首次执行因 Web `RepoDigests` 比对缺陷未通过运行态摘要门禁，编排器自动回滚；没有重放
  该 deployment。修复提交 `a2322538…` 经测试和签名发布为 1.1.5 后，使用新的 deployment ID
  完成部署。

## 验证结果

- Runner systemd 为 `active/running`，Unix socket 权限为 `0660 root:areasong-ops`；健康端点返回
  `1.1.5/a2322538…`。
- Web 健康检查通过；运行镜像 digest、OCI revision 与批准制品一致，容器为非 root、只读 rootfs，
  网络和挂载边界符合生产合同。
- `preflight installed` 与 `preflight runtime` 均通过；生产源码和 Web/Runner revision 一致，
  `source/runtime drift: none`。
- Nginx `nginx -t` 通过。`forge.areasong.top` 仍有既有重复 `server_name` 警告；该问题不在本次
  变更范围内，未修改或 reload Nginx。
- AreaForge、Sub2API、相关数据库及 exporter 保持健康；未创建或执行任何业务服务 Release Plan，
  未切换业务流量。
- 生产 `/opt/ops` 对齐 `a2322538…`，并字节级保留既有
  `services/sub2api/compose.yml` 修改和
  `services/areasong-ops/deploy/preflight.sh.bak.20260826-1247` 未跟踪文件。
- 发布后已执行 `sudo -k`，清理共享终端的 sudo 时间戳。

## 明确未执行

- 未操作 AreaForge、Sub2API、业务数据库、Nginx 流量或其他服务。
- 未处理既有 Nginx 重复 `server_name` 告警。
- 未清理或覆盖生产 `/opt/ops` 的两项既有未提交内容。

## 结论

AreaSong Ops 1.1.5 的 Web 与 Runner 已按同 revision 发布，统一编排器状态为 `succeeded`；
运行身份、健康、隔离、摘要和源码漂移门禁全部通过。本次控制面发布完成，无需重复执行。
