# LosAngeles standards/09 C0：容器与数据服务只读审计

更新时间：2026-07-05  
服务器：LosAngeles  
范围：Docker 容器基线、Redis、Postgres、Compose 文件位置  
风险级别：只读；未重启服务，未修改业务配置，未打印密钥或密码

## 1. 审计结论摘要

- 运行中容器数：16
- 未显式带 Docker 日志 max-size/max-file 的运行中容器数：16
- 未显式带日志限制的运行中容器：alertmanager, prometheus, blackbox-exporter, postgres-exporter-account-vault, redis-exporter-sub2api, postgres-exporter-sub2api, account-vault-web-1, sub2api, sub2api-postgres, sub2api-redis, grafana, promtail, node-exporter, loki, resume-jadeai-app-1, account-vault-postgres-1
- 未设置内存限制的运行中容器数：16
- 未设置内存限制的运行中容器：alertmanager, prometheus, blackbox-exporter, postgres-exporter-account-vault, redis-exporter-sub2api, postgres-exporter-sub2api, account-vault-web-1, sub2api, sub2api-postgres, sub2api-redis, grafana, promtail, node-exporter, loki, resume-jadeai-app-1, account-vault-postgres-1
- 未设置 CPU 限制的运行中容器数：16
- 特权运行中容器数：0
- 特权运行中容器：无
- 未启用 no-new-privileges 的运行中容器数：16
- 根文件系统非只读运行中容器数：16
- 镜像未使用 digest pin 的运行中容器数：16
- 镜像未使用 digest pin 的运行中容器：alertmanager, prometheus, blackbox-exporter, postgres-exporter-account-vault, redis-exporter-sub2api, postgres-exporter-sub2api, account-vault-web-1, sub2api, sub2api-postgres, sub2api-redis, grafana, promtail, node-exporter, loki, resume-jadeai-app-1, account-vault-postgres-1
- Redis 本体容器检查数：1
- Redis CONFIG 是否出现 NOAUTH：False
- Redis 是否存在 maxmemory=0：True
- Redis 是否存在 appendonly=no：True
- Postgres 本体容器检查数：2
- Postgres 查询是否有失败信号：False
- compose 文件数：3

## 2. 已检查内容

### 2.1 Docker 容器基线

已检查所有容器，并在结论中只统计运行中容器：

- restart policy
- Docker 日志配置
- memory limit
- CPU limit
- privileged
- no-new-privileges
- read-only root filesystem
- 镜像引用是否使用 digest pin

原始检查结果留存在服务器临时目录：

- \
- \
- \
- \

### 2.2 Redis

已对容器名包含 Redis 且容器内存在 \ 的 Redis 本体容器做只读检查：

- PING 可用性
- requirepass 是否存在信号
- maxmemory
- maxmemory-policy
- appendonly
- protected-mode

说明：

- 本审计不打印 Redis 密码值。
- Redis exporter 这类没有 \ 的容器会被跳过。
- 如果 Redis 已启用认证，部分 CONFIG 查询可能返回 NOAUTH，这是预期信号。

### 2.3 Postgres

已对容器名包含 Postgres 且容器内存在 \ 的 Postgres 本体容器做只读检查：

- 角色权限位：superuser、createdb、createrole、replication、bypassrls、login、connection limit
- 数据库大小

说明：

- 本审计不打印数据库密码、连接串或环境变量值。
- Postgres exporter 这类没有 \ 的容器会被跳过。

### 2.4 Compose 文件

已扫描 compose 文件位置，结果留存在：

- \

## 3. 初步风险判断

### P1：现有容器日志限制未完全继承

批次 B2 已设置 Docker daemon 默认日志轮转，但现有容器大多是在该配置之前创建。Docker daemon 默认日志配置主要影响后续新建容器；旧容器需要在维护窗口逐个重建，才能完全继承新默认值。

建议：

- 后续按服务逐个检查 compose。
- 在 compose 中显式增加 logging 策略，或通过重建容器继承 daemon 默认值。

### P1：资源限制尚未企业化

如果容器未设置 memory / CPU limit，单个服务异常时可能挤占整机资源。

建议：

- 对数据库、缓存、业务 Web、监控组件分别设置保守资源上限。
- 先观察 3 到 7 天 Prometheus 资源曲线，再按实际峰值加余量设置。

### P1：Redis 持久化与内存边界需要收敛

审计显示 Redis 存在 \ 或 \ 信号时，说明还没有明确内存上限或 AOF 持久化策略。是否开启 AOF、设置多大内存上限，需要结合业务是否允许丢缓存和当前数据量决定。

### P2：镜像 digest pin 尚未完成

如果镜像只使用 tag，没有 pin 到 digest，未来拉取同一 tag 可能得到不同镜像内容。

建议：

- 对关键业务镜像和监控组件逐步 pin digest。
- 保留升级记录和回滚版本。

### P2：容器运行时安全选项可继续增强

可继续评估：

- no-new-privileges
- read-only root filesystem
- cap_drop
- user 非 root 运行

这些需要按服务验证，不能一刀切直接开启。

## 4. 下一步建议

推荐进入 C1：数据服务安全与恢复能力收敛。

建议顺序：

1. Redis：确认是否已有密码、maxmemory、AOF/快照策略。
2. Postgres：审计超权角色，确认业务用户是否最小权限。
3. 容器资源：先为最容易失控的服务设置内存上限。
4. 旧容器日志：按 compose 逐个重建，使日志轮转真正落到现有容器。

状态：审计完成，待 C1 分批收敛。
