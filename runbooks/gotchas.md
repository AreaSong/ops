# 已知坑点索引（Gotchas）

> 从真实变更与故障记录中提炼的高代价坑点。每条 = 一句话坑点 + 根因 + 详细记录锚点。
> 任务涉及对应组件时**必须先读相关条目**，踩到坑深入排查前先查这里。

## 录入标准（新增条目前自检）

至少满足 2/3 才录入，否则留在 records/ 即可：

1. **可重复**：同类场景下次还会遇到，不是一次性变通。
2. **代价高**：不提前知道会浪费大量排查时间或引发生产风险。
3. **配置/代码不可见**：从现有配置或代码表面看不出来。

写法遵循泛化规则：脱离本项目上下文也能看懂（具体发现 → 通用 pattern → 不遵守的后果）。**只从真实记录提炼，禁止凭空想象。**

## Redis

- **收紧命令权限禁止 `-@dangerous` 类别一刀切**——Redis 8 的 `@dangerous` 不只含破坏性命令，还包含 `INFO`、`CONFIG GET`、`SLOWLOG GET`、`CLIENT LIST` 等监控依赖命令；redis_exporter 默认执行 `CONFIG GET *`。动手前先盘点 exporter / 备份脚本 / 业务代码对命令的真实依赖（本机备份依赖 `BGSAVE`，sub2api 依赖 `EVAL`/`SCAN`/PUB-SUB），逐条精确禁用。
  → 详见 [records/losangeles-standards-09-c1b-redis-acl-compatibility-analysis-20260706.md](records/losangeles-standards-09-c1b-redis-acl-compatibility-analysis-20260706.md)
- **运行态 `ACL SETUSER` 重启即失效**——不配 `--aclfile` + `ACL SAVE`，容器重建后收紧策略静默消失。aclfile 含密码 hash，属敏感文件：不入 Git，且必须纳入备份集（与 `dump.rdb` 同备同恢）。
  → 详见 [records/losangeles-standards-09-c1d-redis-acl-persistence-20260706.md](records/losangeles-standards-09-c1d-redis-acl-persistence-20260706.md)、[records/losangeles-standards-09-c1e-redis-acl-backup-coverage-20260706.md](records/losangeles-standards-09-c1e-redis-acl-backup-coverage-20260706.md)

## PostgreSQL

- **应用启动内嵌 migration 时，不能直接切纯 CRUD 低权限用户**——migration 需要 DDL 权限，切换后容器持续 unhealthy（`pq: permission denied`）。正确路径：先确认应用能否关闭启动自动 migration 或把迁移拆成独立维护步骤，否则保持管理用户运行。
  → 详见 [records/losangeles-standards-09-c2b-sub2api-low-privilege-switch-attempt-20260705.md](records/losangeles-standards-09-c2b-sub2api-low-privilege-switch-attempt-20260705.md)
- **PostgreSQL 大版本升级会破坏 exporter collector 兼容性**——PG 18 移除了 `pg_stat_bgwriter` 的 checkpoint 字段，postgres-exporter v0.15 持续 `collector failed`；需升级 exporter 并按 PG 版本分别配置 collector 开关（`--no-collector.stat_bgwriter` + `--collector.stat_checkpointer`）。同宿主多 PG 版本时 exporter 配置不能复用一套。
  → 详见 [records/losangeles-standards-09-c7-postgres-exporter-pg18-compatibility-20260706.md](records/losangeles-standards-09-c7-postgres-exporter-pg18-compatibility-20260706.md)

## Docker / Compose

- **改 `.env` 后 `docker compose up -d` 不一定重建容器**——运行时环境可能保持旧值，让人误以为变更已生效；需 `--force-recreate --no-deps <svc>` 强制重建，改完用 `docker inspect` 复核运行时值。另注意 compose 环境映射可能复用初始化变量（如 `POSTGRES_USER`）而非独立应用变量，改 `.env` 前先核对映射关系。
  → 详见 [records/losangeles-standards-09-c2b-sub2api-low-privilege-switch-attempt-20260705.md](records/losangeles-standards-09-c2b-sub2api-low-privilege-switch-attempt-20260705.md)
- **容器日志上限必须写进 compose，不能依赖 daemon 默认值**——依赖隐式默认时，容器迁移/重建到其他环境会丢失限制，json-file 日志无限增长打满磁盘。新增容器统一显式声明 `json-file max-size=50m max-file=5`。
  → 详见 [records/losangeles-standards-09-c4-container-logging-limits-20260705.md](records/losangeles-standards-09-c4-container-logging-limits-20260705.md)

## Nginx

- **location 里 `include` 已含 `proxy_read_timeout` / `proxy_send_timeout` 的 snippet 后，不能再写同名指令想“覆盖”**——Nginx 同上下文重复指令会直接 `nginx -t` 失败（`directive is duplicate`），不会以后者为准。长连接（WebSocket / 反代隧道）需要更长超时时应去掉该 include，改为在 location 内联 headers + 目标超时，或单独维护不含超时的 snippet。
  → 详见 [records/losangeles-xui-tcp-nginx-tuning-20260721.md](records/losangeles-xui-tcp-nginx-tuning-20260721.md)

## Prometheus

- **relabel 的 `replacement: $1` 不会自动引用整个匹配值**——配套 `regex` 必须显式包含第一个捕获组（如 `(.+)`）；若写成 `.+`，`$1` 会展开为空，目标标签随即消失。后续再 `labeldrop` 原标签时，原本靠该标签区分的多条序列会发生标签碰撞并静默丢失维度。发布此类变更必须在真实抓取后同时核对新标签数量、旧标签为零和代表性序列基数。

## 系统 / 启动链路

- **`/etc/fstab` 改动必须走验证链，且不主动重启**——`findmnt --verify --verbose` → `mount -a` → `systemctl daemon-reload`，启动级验证留到维护窗口。swap 文件的 `non-bind mount source is a regular file` warning 是正常形态，不是错误。
  → 详见 [records/losangeles-standards-09-b3-fstab-uuid-20260706.md](records/losangeles-standards-09-b3-fstab-uuid-20260706.md)

## Git / CI

- **容器内扫描 Git worktree 时不能只挂载工作目录**——worktree 的 `.git` 是指向公共 Git 目录的绝对路径；容器看不到目标时，部分扫描器可能打印 fatal 却仍以 0 退出，并显示 `0 commits scanned`。必须核对实际扫描量；工作树可用 no-git 模式补扫，历史扫描需把公共 Git 目录挂入容器，并显式设置 `GIT_DIR` 与 `GIT_WORK_TREE`。

## 应用行为

- **浏览器指纹作匿名身份的应用，"数据丢失"多半是身份错位**——指纹（如 `x-fingerprint`）变化会被当成新用户，旧数据看似消失实际都在。先只读核对数据库行数与完整性（SQLite `PRAGMA integrity_check`），确认是归属问题再做归属修正；不要急着从备份恢复，误恢复反而可能覆盖数据。
  → 详见 [records/losangeles-jadeai-fingerprint-incident-20260703.md](records/losangeles-jadeai-fingerprint-incident-20260703.md)

## 清退规则

- 相关技术/组件已下线 → 直接删除条目。
- 迁移进行中 → 加作用域标注（如"仅适用于 legacy 模块"）。
- 不确定是否仍适用 → 加 `<!-- DEPRECATED: 原因, 日期 -->` 注释，保留一个迭代周期后删除。
