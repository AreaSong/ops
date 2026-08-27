# AreaSong Ops C2 生产发布记录

日期：2026-08-27
变更窗口：2026-08-27 13:00–15:00（Asia/Shanghai）
变更负责人/操作人：`song80184@gmail.com`
批准级别：C2 单人审批（本次仅适用于已确认的 AreaForge 生命周期控制面发布；全局高风险操作仍保留双人审批）
制品来源：GHCR，按 `AreaSong/ops` commit `c74fe0c2a19d71db71fd8a8fb8ab84eb9e0a96cf` 发布并签名

## 变更范围

- 更新 AreaSong Ops Runner 与 Web 控制面制品。
- 未修改 AreaForge、Sub2API、Nginx、数据库、业务流量或 `/opt/ops` 既有未提交修改。
- 生产 `/etc/areasong-ops/web.env` 使用 `OPS_BUILD_VERSION=1.0.0` 与本次 revision。
- systemd 实际 `ExecStart` 指向 `/usr/local/libexec/areasong-ops/areasong-ops-runner`；按 unit 定义替换并重启 Runner，Web 使用 `--force-recreate --no-build` 重建。

## 制品与回滚

| 项目 | 值 |
| --- | --- |
| Git revision | `c74fe0c2a19d71db71fd8a8fb8ab84eb9e0a96cf` |
| Web image digest | `sha256:ba98c087a8748cbe8f1b9bfb2aa475a95f2c126d3bb8c17ddb53a895c583a832` |
| Runner bundle SHA-256 | `c301e6f1bf061aa9017da172f4f346fd688eb8e7b6dc377740bf9ff49b60ab47` |
| 回滚备份 | 以本次 `la-share` 部署输出记录的 `/var/backups/ops/deployments/` 对应时间戳目录为准 |

## 验证结果

- Runner systemd：`active`；metrics revision 为 `c74fe0c2…`。
- Web metrics revision 为 `c74fe0c2…`，镜像标签为 `areasong-ops-web:c74fe0c2…`，健康检查通过。
- Web 运行用户 `65532:65532`；`ReadonlyRootfs=true`。
- runtime preflight：`PASS`。
- Web image Cosign 与 Runner bundle Cosign verify：通过。
- 缺失 Access 身份或内部 actor 的 API 请求返回 `401`（`内部身份无效`）；该结果证明匿名写入保护生效。
- 已认证 API smoke：通过 Cloudflare Access 身份 `song80184@gmail.com` 登录控制面，依次加载服务、状态、受管对象、Fleet 与 Runner 更新视图；服务状态与受管对象正常渲染，Fleet/Runner 自更新按策略返回“尚未启用”，实时连接正常且无 API 错误提示。该 UI-backed smoke 覆盖 `/v1/services`、`/v1/states`、`/v1/objects`、`/v1/fleet`、`/v1/runner/update` 对应的控制面读取链路。

## 仓库与安全边界

- 生产 `/opt/ops` 的旧 HEAD 为 `e9b8b3e2…`，本轮未 checkout、merge 或覆盖其原有未提交修改；新 revision 通过 `git fetch --no-tags origin main` 获取，并在临时 detached worktree 中完成 preflight。
- 已执行远端 `sudo -k` 并关闭 `la-share` 会话；未读取、输出或持久化 cookie、token、密码。

## AreaForge stop 计划门禁结果

2026-08-27 在已确认的 C2 窗口内执行只读 preflight，未创建或执行 stop 计划。生产证据如下：

- `areaforge-web` 与 `areaforge-postgres` 均为 `running/healthy`；Runner `healthz` 返回 `ok`，revision 为 `c74fe0c2…`。
- `/etc/areasong-ops/services.json` 中 `service:areaforge` 的 `trafficPolicy` 为 `null`。
- 按控制面合同，缺失 TrafficPolicy 时禁止 `maintenance`、`drain`、`stop` 和 `resume-traffic`；因此本次变更在门禁处安全停止，未改变生产状态。
- 按仓库候选定义预检引用文件时，发现 `/etc/nginx/snippets/areasong-ops/areaforge-traffic.conf` 与 `/etc/nginx/snippets/areasong-ops/areaforge-maintenance.conf` 均不存在，且 `/etc/nginx/sites-enabled/forge.areasong.top.conf` 权限为 `777`，未达到配置安全基线。
- 因此本次连 TrafficPolicy 写入也未执行；修复前不得通过 Nginx、Docker 或任意宿主命令绕过控制面。应先单独提交 Nginx 文件与权限修复计划，完成 `nginx -t`、摘要绑定、Runner 复验和独立验收，再写入 TrafficPolicy 并重新创建生命周期计划。

## 收口状态

状态：控制面已部署并完成认证读取链路 smoke；AreaForge stop 因 TrafficPolicy 缺失被安全拒绝，待单独补齐 TrafficPolicy 后重新审批。
