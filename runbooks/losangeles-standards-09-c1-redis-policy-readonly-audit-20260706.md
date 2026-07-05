# LosAngeles standards/09 C1：Redis 策略只读复核

更新时间：2026-07-06  
服务器：LosAngeles  
范围：`sub2api-redis` Redis 运行策略、认证、内存上限、持久化、网络暴露面  
风险级别：只读复核；未修改 Redis 配置，未重启容器，未打印 Redis 密码

## 1. 结论

本次只读复核确认：Redis 已具备密码认证、内存上限、AOF 持久化和容器内网隔离。

已完成：

- Redis 容器不发布公网端口，`docker port sub2api-redis` 无映射。
- 宿主机未监听公网 `6379`。
- Redis `requirepass` 已设置。
- `sub2api` 已配置 Redis 密码环境变量。
- `redis-exporter-sub2api` 使用带认证的 Redis 地址，内容未打印。
- Redis `maxmemory=512MiB`。
- Redis `maxmemory-policy=noeviction`。
- Redis `appendonly=yes`。
- Redis 容器资源限制已由 C3a/C3c 复核：容器内存上限 `640MiB`，为 Redis 512MiB 数据上限保留运行余量。

仍需决策：

- Redis 默认 ACL 当前仍允许 `+@all` 命令集合。
- 是否限制 `FLUSHALL`、`FLUSHDB`、`CONFIG`、`SHUTDOWN` 等高危命令，需要维护窗口和应用兼容性确认。

## 2. 关键只读证据

运行状态：

- 容器：`sub2api-redis`
- 镜像：`redis:8-alpine`
- 状态：`Up`，healthcheck healthy
- 端口：容器内 `6379/tcp`，未发布到宿主机公网

Redis 配置摘要：

| 项 | 当前值 |
| --- | --- |
| `requirepass` | 已设置 |
| `maxmemory` | `536870912`，即 512MiB |
| `maxmemory-policy` | `noeviction` |
| `appendonly` | `yes` |
| `save` | `60 1` |
| `protected-mode` | `no` |
| `bind` | `* -::*` |
| `evicted_keys` | `0` |
| `rejected_connections` | `0` |

说明：

- `protected-mode=no` 与 `bind=*` 在容器内不等于公网暴露；当前没有 Docker 端口映射，外部无法直接访问 Redis。
- Redis 密码和 exporter Redis URL 均未写入本报告。

## 3. 风险判断

当前主要风险不是公网暴露，也不是无密码或无内存上限。

当前剩余风险是：默认 ACL 仍为宽权限命令集合。如果应用凭据泄露、容器网络内其他进程被攻破，攻击面仍包括 Redis 高危管理命令。

直接禁用高危命令的风险：

- 需要修改 Redis 启动参数或 ACL。
- 需要确认 `sub2api`、healthcheck、redis exporter 不依赖被禁命令。
- 需要重启 Redis 容器或做 ACL 在线变更；Redis 容器重启会短暂影响 sub2api。

## 4. 推荐后续

建议后续单独开维护窗口处理 Redis ACL / 高危命令策略。

推荐路径：

1. 先在隔离临时 Redis 或低风险窗口验证 `sub2api` 是否只需要常规读写命令。
2. 建议优先限制明显危险命令：`FLUSHALL`、`FLUSHDB`、`SHUTDOWN`。
3. 谨慎处理 `CONFIG`、`KEYS`、`EVAL` 等命令；这些可能影响排障、迁移、脚本或第三方库。
4. 变更后验证：
   - `sub2api /health`
   - Redis healthcheck
   - `redis-exporter-sub2api`
   - Prometheus Redis target
   - Grafana Datastores 面板

## 5. 验证留痕

只读采集输出保存在服务器临时文件：

- `/tmp/codex-redis-policy-audit.out`

该文件已将 Redis 密码 hash 和连接密码字段替换为 `<redacted>`，不进入 Git。

状态：密码、maxmemory、持久化、内网隔离完成；高危命令 / ACL 策略待决策。
