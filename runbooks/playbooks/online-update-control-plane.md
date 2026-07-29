# 在线更新控制面

## 目标与边界

宿主机控制面替代业务容器内的二进制自更新。业务容器继续使用只读根文件系统，不挂载 Docker Socket，不获得宿主机命令执行能力。

当前状态：

- AreaForge：控制面已部署，`v0.1.9` 生产身份和只读阶段验收通过。
- Sub2API：`v0.1.168` 已完成生产升级并退休一次性目标；适配器保留，等待下一固定目标再次审批启用。
- 自动更新：不启用。每次 `apply` 都需要一个 10 分钟内有效的 root-only 请求文件。

## 安全契约

请求只能包含固定 schema 的 `service`、`action`、`targetId` 和 expected-before 身份。服务目录决定适配器、动作和发布目标；请求不能提供命令、脚本、镜像、Compose 路径或环境文件路径。

控制器按服务获取非阻塞锁，记录请求摘要和幂等键。相同幂等键只能重放完全相同的请求；中途崩溃的 `in_progress` 状态必须人工核对，不能自动重试。

执行阶段固定为：

1. `preflight`
2. `backup`
3. `migration`
4. `apply`
5. `health`
6. `smoke`
7. `identity`
8. 失败时 `rollback`

AreaForge 适配器将通用请求转换为现有 updater 的严格 V2 request guard。updater 继续负责签名、manifest、迁移和内部失败回滚；控制面额外执行 fresh backup、认证只读 smoke 和三方身份核验。回滚点来自请求中已核验的实际更新前 Docker 身份，不读取旧 updater status 中的历史回滚目标。

Sub2API 适配器只允许 `v0.1.163` 升级至 linux/amd64 固定 digest 的 `v0.1.168`。它验证 fresh backup 和隔离演练证据，只执行 `up -d --no-deps --force-recreate sub2api`；PostgreSQL、Redis 的容器 ID 与 `StartedAt` 必须保持不变。生产没有预置 admin API key 时，smoke 会创建随机短生命周期 key，按值精确删除且不写入日志；不会覆盖已有 key。失败回滚只恢复旧 Compose 和旧应用镜像，不恢复数据库，因为旧镜像在 237 条 migration 的新 schema 上已演练通过。

## 请求门禁

- 文件：regular file、非 symlink、`root:root 0600`
- TTL：`expiresAt - requestedAt <= 600s`，允许 30 秒时钟偏差
- `id`：`update_<epoch-ms>_<uuid>`
- `idempotencyKey`：UUID
- `actorEmailHash`：操作者邮箱的小写 SHA-256，不记录明文邮箱
- `expectedBefore`：版本、镜像引用、Docker Image ID、运行身份哈希、自动更新和签名策略、回滚元数据

状态与审计默认写入 `/var/lib/ops/update-control/`，目录 `0700`、文件 `0600`。`audit.jsonl` 采用 append + fsync；它提供主机侧操作证据，但不替代远端不可变审计归档。

## 本地验证

```bash
python3 -m unittest scripts.deploy.tests.test_update_control -v
python3 -m py_compile scripts/deploy/update-control.py
bash -n scripts/deploy/update-control/adapters/*.sh
jq empty scripts/deploy/update-control/services.json scripts/deploy/update-control/releases/*.json
```

## Sub2API v0.1.168 完成记录

- 固定目标 `v0.1.168` 的 linux/amd64 image digest 与镜像内版本、commit 三方一致。
- fresh PostgreSQL、Redis、`/app/data`、两份 Compose 与 root-only `.env` 备份完成并写入 manifest。
- 229 条现有 migration 基线已记录；新增 8 个 SQL migration 在隔离恢复副本上演练。
- 认证只读 smoke 覆盖登录、系统设置、账号列表、API key 列表和最小网关请求。
- 失败回滚验证旧镜像可在新 schema 上安全启动；若不兼容，升级请求必须标记不可自动回滚并安排数据库恢复窗口。
- 只重建 `sub2api`，PostgreSQL 与 Redis 容器身份保持不变。
- 健康、外部 Blackbox、Prometheus 告警、Loki 错误和版本身份全部通过。
- 生产接入已单独批准；不恢复数据库、不启用 timer 或自动更新。
- 请求 `update_1785329456218_e02d594a-d98f-4def-b386-f8d45191f80c` 七阶段成功；生产版本、Git commit、镜像 digest、237 条 migration 和依赖容器身份一致。
- 一次性目标执行后已从服务 allowlist 移除，防止重放已过期的 expected-before；发布目录与演练证据继续保留。

## 回滚

控制面部署本身的回滚是移除入口 unit/受控请求目录并保留 state 证据，不修改业务容器。服务更新回滚由适配器恢复实际更新前 Compose 和 immutable image digest，随后重复 health、认证 smoke 和身份核验。任何 `recovery_uncertain` 都必须停止自动处理并人工对账。
