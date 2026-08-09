# AreaSong Ops 接入表

| 项目 | 内容 |
| --- | --- |
| 应用名 | `areasong-ops` |
| 应用说明 | AreaForge 与 Sub2API 的受控交互式运维控制面 |
| 项目类型 | Go Runner + Go/React Web + Docker Compose |
| 负责人 | `as` |
| 域名 | `ops.areasong.top` |
| 宿主机端口 | `127.0.0.1:3200` |
| 容器端口 | `8080` |
| Health | `/healthz`，同时验证 Web 到 Runner Socket |
| 持久化目录 | `/var/lib/areasong-ops`，仅 Runner 可读写 |
| Secret | `/etc/areasong-ops/web.env`，`root:root 0600` |
| 发布方式 | 固定 Git commit 构建 Web 镜像；Runner 使用同 commit 的 Linux 静态二进制 |
| 回滚方式 | 恢复上一 Runner 二进制、Web 镜像 tag、Compose 和 Nginx 配置 |
| 数据库恢复 | 禁止自动恢复；SQLite 只恢复控制面审计，不触碰业务数据库 |
| 自动更新 | 不启用 |

Cloudflare Access 仅允许 `song80184@gmail.com`。Web 不挂载 Docker Socket、业务卷、备份目录或 SQLite。
