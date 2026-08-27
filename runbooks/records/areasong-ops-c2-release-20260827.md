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

## 2026-08-27 Nginx 受控配置修复

- 在独立批准范围内安装 AreaForge 站点配置及两个受管 snippet；站点文件与本地候选 SHA-256 一致，权限为 `0644 root:root`，snippet 目录为 `0755 root:root`，snippet 文件为 `0644 root:root`。
- `forge.areasong.top` 的受管 traffic include 恰好一次；traffic snippet 保持 `running`，未改变公网流量状态。
- `nginx -t` 通过后执行 `systemctl reload nginx`；reload 后 Nginx 为 `active`，`https://forge.areasong.top/` 返回 HTTP 200，`areaforge-web` 与 `areaforge-postgres` 仍为 healthy。
- `nginx -t` 和 reload 仍报告既有重复 `server_name forge.areasong.top` 警告，来源是 `sites-enabled` 中两个旧备份符号链接；本轮按批准保留，未删除或改名。
- 当时运行态 preflight 未通过：生产 `/opt/ops` HEAD 为 `e9b8b3e2…`，而 `/opt/services/areasong-ops/.env` 的 `OPS_BUILD_REVISION` 为 `c74fe0c2…`，两者不是祖先关系。未因此执行任何控制面重建、Runner 重启或其他服务变更。该门禁已于 2026-08-28 通过下述仓库同步收口。

## 仓库与安全边界

- 生产 `/opt/ops` 的旧 HEAD 为 `e9b8b3e2…`，本轮未 checkout、merge 或覆盖其原有未提交修改；新 revision 通过 `git fetch --no-tags origin main` 获取，并在临时 detached worktree 中完成 preflight。
- 已执行远端 `sudo -k` 并关闭 `la-share` 会话；未读取、输出或持久化 cookie、token、密码。

## 2026-08-28 生产仓库同步与 runtime preflight 收口

- 经用户即时明确批准，通过 `la-share` 备份生产 `/opt/ops` 的 3 个未提交文件及完整 worktree diff，备份目录为 `/var/backups/ops/ops-sync-20260827-161153`（服务器 UTC 时间戳）。目录权限为 `0700 root:root`，文件权限为 `0600 root:root`；3 个文件的备份 SHA-256 与同步前原文件一致，完整 diff 为 2608 字节。
- 将生产 `/opt/ops` HEAD 和 `origin/main` 对齐到已推送提交 `57db5852f7770503514a397f06fa9a34b812bb7a`。同步前单独保存并同步后恢复 `services/sub2api/compose.yml` 的镜像 digest 修改，内容仍为 `sha256:cff6bc3ed1a6eba7ea240bad8637cf12856161a4efb98be0882c2fa7aff371e3`；未跟踪的 `preflight.sh.bak.20260826-1247` 也原样保留。
- 同步后工作树只剩上述 Sub2API 未暂存修改和原 `.bak` 文件，`git diff --check` 通过；`services/areasong-ops/deploy/preflight.sh` 与目标提交一致。
- `preflight.sh runtime` 输出源码 revision `57db5852…`、运行制品 revision `c74fe0c2…` 和 `source/runtime drift: source HEAD is ahead of the deployed revision`，最终结果为 `runtime preflight: PASS`。运行制品是源码 HEAD 的祖先，符合 revision 门禁。
- 本次未重建 Web 容器、未重启 Runner、未写入 TrafficPolicy、未执行 AreaForge stop/start，也未修改 Sub2API 运行状态。`la-share` 会话已退出，sudo 时间戳已清理。

## AreaForge stop 计划门禁结果

2026-08-27 在已确认的 C2 窗口内执行只读 preflight，未创建或执行 stop 计划。生产证据如下：

- `areaforge-web` 与 `areaforge-postgres` 均为 `running/healthy`；Runner `healthz` 返回 `ok`，revision 为 `c74fe0c2…`。
- `/etc/areasong-ops/services.json` 中 `service:areaforge` 的 `trafficPolicy` 为 `null`。
- 按控制面合同，缺失 TrafficPolicy 时禁止 `maintenance`、`drain`、`stop` 和 `resume-traffic`；因此本次变更在门禁处安全停止，未改变生产状态。
- 按仓库候选定义预检引用文件时，发现 `/etc/nginx/snippets/areasong-ops/areaforge-traffic.conf` 与 `/etc/nginx/snippets/areasong-ops/areaforge-maintenance.conf` 均不存在，且 `/etc/nginx/sites-enabled/forge.areasong.top.conf` 权限为 `777`，未达到配置安全基线。
- 因此本次连 TrafficPolicy 写入也未执行；修复前不得通过 Nginx、Docker 或任意宿主命令绕过控制面。应先单独提交 Nginx 文件与权限修复计划，完成 `nginx -t`、摘要绑定、Runner 复验和独立验收，再写入 TrafficPolicy 并重新创建生命周期计划。

后续 Nginx 修复尝试仍未安装正式配置：已按批准创建站点备份 `/etc/nginx/sites-enabled/forge.areasong.top.conf.bak.20260827-0601`，生成临时 running/site 文件时因 Bash 历史展开错误（`!doctype`）停止，临时目录已清理，原站点与生产流量保持不变。下一次执行应使用不触发 shell 展开的受控文件传输方式，并重新走预检与批准。

2026-08-27 再次按批准尝试修复时，`la-share` 共享会话的 sudo 授权持续失败并循环提示密码；未安装任何 Nginx 文件、未 reload、未写入 TrafficPolicy，生产状态保持不变。该项需在 sudo 授权稳定后重新进入独立变更窗口。随后 sudo 授权确认已生效，已完成受控文件安装、`nginx -t`、reload 与健康检查；整体验收仍受 runtime revision ancestor 门禁阻断。

## 收口状态

状态：Nginx 受控配置修复已部署并通过语法、reload 与公网健康检查；控制面已完成认证读取链路 smoke。生产 `/opt/ops` 已对齐 `57db5852…`，runtime revision ancestor 门禁已通过。AreaForge stop 仍因 TrafficPolicy 缺失被安全拒绝；下一步是单独审批 TrafficPolicy 写入和摘要复验，然后重新创建 AreaForge 生命周期计划。
