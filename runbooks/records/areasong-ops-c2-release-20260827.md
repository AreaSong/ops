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

## 收口状态

状态：已部署并完成认证读取链路 smoke；台账与本记录待提交收口 commit。
