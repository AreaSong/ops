# LosAngeles Sub2API v0.1.168 受控升级

日期：2026-07-29  
状态：完成  
范围：Sub2API 应用容器、受控在线更新控制面；不重建 PostgreSQL、Redis，不恢复数据库，不启用自动更新。

## 变更

- 来源版本：`0.1.163`，Git commit `d0bdd7e771636a8d315f542cafd39484f39bd60c`。
- 目标版本：`0.1.168`，Git commit `99c8e4bf7564823bafbab369acab6539e734c1bb`。
- 目标镜像：`weishaw/sub2api@sha256:8c94357c48d6cad360159b14a4bee913a6375520845593ec942cdd59506855e0`。
- 控制面提交：`916f728d87fe7d33ebd5c92b8e699993e2312313`。
- 请求：`update_1785329456218_e02d594a-d98f-4def-b386-f8d45191f80c`。
- 请求摘要：`sha256:21ba2c604d88c404fc7f4827f80d953ed0d9d1540f11c344f67de68a51f84b27`。

## 备份与演练

- 升级前证据集：`/var/backups/ops/change/20260729T115314Z-sub2api-v0168`，目录 `root:root 0700`。
- 证据 manifest：`sha256:c5a0aab13dc4c81a5698b12656a9285f1017984a07c7f76d2d647f4380118ba1`。
- fresh PostgreSQL：`/var/backups/ops/postgres/sub2api-postgres-20260729-125113.sql.gz`，`68519558` bytes。
- fresh Redis：`/var/backups/ops/redis/redis-20260729-125133.tar.gz`，`53008` bytes。
- fresh `/app/data`：`/var/backups/ops/volumes/sub2api-data-20260729-125134.tar.gz`，`9378` bytes。
- 隔离恢复使用 internal Docker network、无宿主端口；目标版 migration 229 -> 237。
- 8 个新增 migration 与预期清单一致；旧 `v0.1.163` 在 237 schema 上 health 和认证 smoke 均通过。

## 生产执行

控制面依次完成：`preflight`、`backup`、`migration`、`apply`、`health`、`smoke`、`identity`，终态为 `succeeded`、返回码为 0。

唯一重建命令的服务参数为：

```bash
docker compose --env-file /opt/services/sub2api/.env \
  -f /opt/services/sub2api/compose.yml \
  up -d --no-deps --force-recreate sub2api
```

未执行 Compose `down`，未重建或恢复 PostgreSQL、Redis。

## 身份与数据验收

- 应用容器：`a19e3b85c5b4fe6501d8dc83b074dcba7331a3ebb4e0c325faf239b4a1215152`，`StartedAt=2026-07-29T12:51:37.501547919Z`，healthy。
- PostgreSQL：`53b48693bcb6a8558a748c8b01db3c9431147e2940d167082b80b2263fb61766`，`StartedAt=2026-07-17T08:37:41.266118499Z`，升级前后完全一致。
- Redis：`0ef6a1f74bacdd37aa8671bf6f9312f3f6feac5ac86b0e90b09b78c8d9e2dcfc`，`StartedAt=2026-07-22T10:19:10.114854382Z`，升级前后完全一致。
- migration 数量为 237。
- 临时 admin smoke key 已清理；`settings.admin_api_key` 行数恢复为 0。
- 两份 Compose SHA-256 均为 `a9ab3e9a763c70f49d78bac9042f37ade93ffe02e504658cc55946a6fd79c04c`。

认证 smoke 全部 HTTP 200：public settings、admin settings、admin accounts、admin API keys、system version、`/v1/models`。运行时 system version 为 `0.1.168`。

## 可观测验收

- `https://cpa.areasong.top/health` 返回 `{"status":"ok"}`。
- Blackbox `health-json` 与 `login-page` 两条 Sub2API 业务旅程均 `up=1`。
- Prometheus firing alert 为空。
- Docker 近 15 分钟 `error|panic|fatal` 计数为 0。
- Loki 同期有 3 条 warning：CORS 默认拒绝跨域、trusted proxies 使用直连 peer IP、pricing fallback 文件缺失；均为启动配置提示，不是升级错误。

## 收口

- 控制面 operation 目录为 `root:root 0700`，请求文件为 `root:root 0600`。
- 主机不存在 Sub2API 自动更新 timer。
- 已从服务 allowlist 移除本次一次性目标，避免重放已过期 expected-before；适配器、发布目录和审计证据保留。
