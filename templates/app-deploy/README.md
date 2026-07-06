# 标准应用部署模板

用途：把一个新项目接入 LosAngeles 单机生产服务器。

复制本目录后，按占位符填写：

| 占位符 | 含义 | 示例 |
| --- | --- | --- |
| `__APP_NAME__` | 应用名，只用小写字母、数字、短横线 | `portal` |
| `__DOMAIN__` | 对外域名 | `www.areasong.top` |
| `__HOST_PORT__` | 宿主机本地监听端口 | `3100` |
| `__APP_PORT__` | 容器内应用端口 | `3000` |
| `__HEALTH_PATH__` | 健康检查路径 | `/health` |
| `__APP_DESCRIPTION__` | 应用说明 | `AreaSong portal website` |

## 推荐目录

```text
/opt/services/__APP_NAME__/
  app/                   # 项目源码或构建上下文
  docker-compose.yml
  app-runbook.md
  deploy-checklist.md

/etc/__APP_NAME__/
  __APP_NAME__.env       # 真实环境变量，root-only，不进 Git
```

## 最小流程

1. 填写 `app-intake.md`。
2. 复制项目源码到 `/opt/services/__APP_NAME__/app`。
3. 复制 `env.example` 到 `/etc/__APP_NAME__/__APP_NAME__.env`，填真实值并设为 `0600 root:root`。
4. 修改 `docker-compose.yml` 和 `nginx-site.conf` 中的占位符。
5. 启动容器并验证本机 health。
6. 安装 Nginx 配置，执行 `nginx -t` 后 reload。
7. 验证公网 HTTPS。
8. 更新 `/opt/ops` inventory / runbook 并提交。

## 约束

- 不把 `.env`、token、数据库密码、私钥提交到 Git。
- 新应用默认只监听 `127.0.0.1:__HOST_PORT__`，由 Nginx 反代到公网。
- 数据库、Redis、管理端口默认不暴露公网。
- 每次部署后必须验证 health、日志和回滚路径。
