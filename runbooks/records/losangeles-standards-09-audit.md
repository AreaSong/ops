# LosAngeles standards/09 验收矩阵

> 本文件是 2026-07-06 的历史基线。阶段 9 当前事实、证据边界和逐项分类以 [losangeles-standards-09-phase9-20260819.md](losangeles-standards-09-phase9-20260819.md) 为准。

更新时间：2026-07-06 04:05 UTC
服务器：LosAngeles  
公网 IP：23.185.200.12  
依据：`standards/09-server-ops-handbook.md` 附录 A P0 汇总  
审计方式：只读实机检查 + `/opt/ops` 台账/Runbook 核对  
审计证据：`/tmp/losangeles-09-audit.out`

## 1. 结论

LosAngeles 当前已经具备可生产运行的单机基础盘：SSH/UFW/Fail2ban、公网端口收敛、Nginx 统一入口、本机与 R2 备份、恢复演练、R2 Postgres 隔离恢复、Prometheus/Grafana/Alertmanager/Loki、数据库与 Redis exporter、安全日志、Cloudflare/证书台账均已落地。单机生产收尾清单见 `runbooks/records/losangeles-single-host-production-closure-20260706.md`。

但如果严格按 `standards/09` 的企业级全生命周期标准验收，当前不是“所有 P0 字面项 100% 满分”，而是：

- 已达标：单机安全、备份、恢复、监控、告警、台账、变更留痕主线。
- 风险接受：SSH 来源 IP 未限制、单机无 HA、无独立数据盘、主机名暂不规范化。
- 待优化：Redis 分用户 ACL、sub2api migration/runtime 拆分实施、SSH 来源 IP 限制等待固定出口 IP。Redis 高危命令阶段 1 已在 C1c 运行态实施，并在 C1d 完成 `aclfile` 持久化；sub2api 直接切低权限失败原因已在 C2f 定位，C2g 已确认当前上游未发现独立 migration-only 命令或关闭启动自动 migration 的开关。
- 已完成补齐项：journald/logrotate/sysctl 与 Docker daemon 日志基线已在 B1/B2 完成，`fstab` UUID 已在 B3 完成，Postgres exporter 对 PostgreSQL 18 的 collector 兼容性已在 C7 修复。
- 云侧已核对 / 能力限制：云账号 MFA 已开启，当前无 API Key；账单/到期按用户要求暂缓；厂商无安全组/云防火墙、快照、云审计/安全通知能力，已记录为风险接受和补偿控制。

## 2. 附录 A P0 验收矩阵

| # | P0 项 | 当前结论 | 证据 / 说明 | 后续动作 |
|---|---|---|---|---|
| 1 | 主备跨可用区；到期计费入台账有告警；抢占式无状态服务 | 单机模型风险接受 / 账单暂缓 | 当前是单机部署；`servers.yaml` 已补 provider/region/owner/control_plane；账单/到期治理按用户要求暂缓；未发现抢占式证据；单机收尾文档已明确边界 | 后续有第二台机器或 HA 需求时做多 AZ；账单/到期告警后续单独处理 |
| 2 | 时区 UTC + 时间同步；非 EOL 系统；数据/日志/应用三分离 | 基本达标 / 数据盘风险接受 | Ubuntu 24.04.4 LTS，NTP active/synchronized；当前时区已为 UTC；应用在 `/opt/services`，日志在 `/var/log`，但无独立数据盘 | 数据盘作为后续增强 |
| 3 | 系统盘数据盘分离；fstab UUID+nofail；磁盘监控含 inode | 部分达标 / 数据盘风险接受 | 当前只有 `/dev/sda1` 系统盘；`/`、`/boot`、`/boot/efi` 已在 B3 切换为 UUID；`findmnt --verify` 与 `mount -a` 已通过；磁盘容量和 inode 已在 node_exporter/Grafana 覆盖 | 数据量增长后规划独立 `/data`；下次维护窗口或自然重启后补一次启动链路复核 |
| 4 | 无 `curl|bash`；第三方源有 GPG 验证 | 基本达标 | 在 `/opt/services`、`/opt/ops` 未扫到明显 `curl|bash`；Docker apt keyring 存在 | 后续补一次 apt sources 详细审计 |
| 5 | 命名三处一致；台账字段完整；到期/欠费双告警 | 部分达标 / 账单暂缓 | 主机名 `LosAngeles` 与台账一致，但不符合规范化命名；台账字段已补 owner/provider/region/control_plane；账单/到期治理按用户要求暂缓 | 主机名规范化等维护窗口；账单/到期告警后续单独处理 |
| 6 | 环境 VPC 隔离；公网入口收敛；IPv6 明确管控 | 部分达标 / 厂商能力限制 | 公网监听实际为 22/80/443；IPv6 仅 link-local；无主机级私网 IP；用户确认厂商无安全组/云防火墙/网络规则能力 | 以 UFW、Fail2ban、端口收敛和监控告警补偿；有固定出口 IP 后收敛 SSH 来源 |
| 7 | SSH 禁密码禁 root 直登；一人一账号；云主账号 MFA；最小权限 AK | 基本达标 | `sshd -T`: `permitrootlogin no`、`passwordauthentication no`；`as` key 登录；云主账号 MFA 已开启；账号邮箱/手机号可用；主账号未共用；当前无 API Key | 后续如新增 API Key，必须记录用途、最小权限和轮换周期 |
| 8 | 双层防火墙默认拒绝；无 0.0.0.0/0 全放行；数据组件不暴露公网；Docker 端口陷阱已处理 | 基本达标 | UFW active，默认 deny incoming，仅 22/80/443；数据组件和 exporter 仅本机或 Docker 网络；SSH 22 仍 Anywhere | 有固定出口 IP 后限制 SSH；`services.yaml` 修正 SSH 限源描述 |
| 9 | 域名/证书自动续期+独立过期监控；ICP 备案一致；Nginx 先 `-t` 再 reload | 基本达标 / 不适用 | `nginx -t` 通过；证书监控已接入；Cloudflare 与证书台账已记录；ICP 对当前非大陆源站按不适用处理 | 保持证书监控；门户上线时补备案判断 |
| 10 | SELinux/AppArmor 不被关闭 | 达标 | AppArmor module loaded；Docker 进程在 `docker-default` enforce；auditd 未启用但不等于 AppArmor 关闭 | auditd 可作为 P1 增强 |
| 11 | 无明文凭证入 Git/命令行/crontab；secrets 权限 600 | 基本达标 | `/etc/ops/r2-backup.env` 600；多数 `/etc/observability/*.env` 600；SMTP 密码文件已为 600 root:root；未打印内容 | 持续保持 secret 不入 Git |
| 12 | 入侵处置流程：隔离保现场、默认重装、凭证全轮换 | 文档达标 | `standards/09`、runbook 模板已覆盖；未做桌面演练 | 年度或季度做一次桌面演练 |
| 13 | 服务位置可预测；开机自启；健康检查接入监控 | 基本达标 | 运行服务主要在 `/opt/services` 和 `/opt/ops/observability`；systemd/Docker restart 均配置；Prometheus targets 全 up；旧 account-vault compose 已改名为 legacy | 持续保持受控副本和运行配置一致 |
| 14 | 编排路线明确；容器日志上限；固定 tag；端口显式绑定 | 基本达标 | Docker Compose 路线明确；端口显式绑定；容器日志轮转已显式配置并验证；第三方镜像已固定 digest | 后续镜像升级继续记录版本/digest |
| 15 | 数据组件内网监听+专属账号；MySQL binlog+慢日志；Redis maxmemory+密码；应用账号无 DDL | 部分达标 | Postgres/Redis 未暴露公网；Postgres/Redis exporter 已接入；C7 已修复 PostgreSQL 18 exporter collector 兼容性；Redis `requirepass`、`maxmemory=512MiB`、AOF 已复核，C1c 已完成 Redis 高危命令阶段 1 运行态收紧；`account-vault` 已使用低权限数据库用户；`sub2api` 当前仍使用 superuser；C2f 确认低权限切换失败点为启动时 `CREATE TABLE IF NOT EXISTS schema_migrations` 需要 `public` schema `CREATE`；C2g 已确认当前上游未发现独立 migration-only 命令或关闭启动自动 migration 的开关 | Redis ACL 阶段 1 已在 C1c 实施，并在 C1d 完成 `aclfile` 持久化；后续需补分用户 ACL；`sub2api` 需应用侧新增或确认独立 migration 与关闭启动自动 migration 能力后，再切 runtime 低权限 |
| 16 | 配置入 Git 有变更记录；TF 远程 state + plan 审阅 | 达标 / 不适用 | `/opt/ops` Git 化；当前未使用 Terraform | 继续保持变更后提交 |
| 17 | 制品可追溯；CI 凭证专用最小权限 | 部分达标 | Compose 和镜像 digest 已记录；未见 CI 部署链路 | 后续业务 CI/CD 时补制品策略 |
| 18 | 变更有单五要素；回滚就绪；一次一变更 | 基本达标 | runbook 和当前流程已记录；高风险变更仍需单独确认 | 保持执行纪律 |
| 19 | 备份任务失败有告警；脚本入 Git | 达标 | 备份脚本在 `/opt/ops/scripts/backup`；本机/R2 指标和告警已接入 | 持续观察告警噪声 |
| 20 | 三处日志上限；审计日志留存 >=180 天 | 基本达标 | journald 已持久化并限制 `SystemMaxUse=1G`；rsyslog/fail2ban/ufw 已调到 26 周保留；Docker daemon 与 compose 均有日志轮转上限；Loki/Promtail 已接入基础日志 | 若后续需要不可篡改审计或跨机长期归档，可补 R2/对象锁类归档 |
| 21 | 全机 node_exporter+基础告警集；探活+证书监控；告警可达值班人 | 达标 | Prometheus targets 全 up；Alertmanager QQ 邮箱已接入；无 firing alerts | 后续按噪声调优 |
| 22 | 每次 OOM 有归因 | 未触发 / 待流程化 | 当前未见本次 OOM 事件核查；标准属于事件发生后的纪律 | OOM 发生时按 runbook 复盘 |
| 23 | 核心服务无单点或有明确决策记录；主从有监控 | 单机模型风险接受 | 当前是单机生产；恢复和异地备份已补齐，HA 不作为当前一台主机模型的完成门槛；收尾文档已记录边界 | 后续有第二台机器、预算或业务 SLA 要求时做 HA |
| 24 | 关键数据有备份方案+异云副本；恢复演练做过；备份告警在线 | 达标 | 本机备份、R2 备份、本机/R2/应用级恢复演练均已完成；C1h 已从 R2 拉回两个 Postgres dump 并导入无网络临时 Postgres 容器验证 | 跨机器实机恢复作为未来有临时机器后的增强项 |
| 25 | 自动安全更新开启；紧急 CVE 流程明确 | 基本达标 | unattended-upgrades active/enabled；`20auto-upgrades` 开启；CVE 流程在标准中 | 月度补丁日持续执行 |
| 26 | 止血优先共识；P0/P1 故障 48h 复盘 | 文档达标 | `standards/09` 和 postmortem 模板已存在 | 真实故障后执行复盘 |
| 27 | 生产测试隔离；批量操作灰度；割接方案含回切点 | 部分达标 | 当前单机/少量服务；跨机器恢复预案已含回滚；生产/测试环境云侧隔离未核查 | 后续多环境时补 VPC/账号隔离 |
| 28 | 离职当日回收+轮换；退役九步；AI 变更需批准 | 文档达标 | `standards/09` 已定义；`/opt/ops` root-only 和共享终端授权流程已记录 | 后续按流程执行 |

## 3. 优化候选

### 可直接做的低风险项

1. 修正 `inventory/services.yaml` 中 SSH 全局端口描述：从 `restricted/限制来源 IP` 改为“当前 Anywhere，密钥登录 + Fail2ban；固定出口 IP 后再限制”。
2. 新增本验收矩阵到 `runbooks/records/losangeles-standards-09-audit.md`，并在 `runbooks/README.md` 建索引。
3. 补充 `losangeles-current-status.md`：说明 `standards/09` 严格验收下仍有“风险接受/云侧能力限制/账单暂缓/维护窗口优化项”。
4. SMTP 密码文件权限已收紧到 `600 root:root`。
5. 旧 account-vault compose 已改名为 `docker-compose.legacy.yml`。
6. Nginx 安全响应头已补齐，记录见 `runbooks/records/losangeles-standards-09-a1-nginx-security-headers-20260705.md`。

### 需要维护窗口或明确确认的项

1. Redis 分用户策略：C1c 已完成阶段 1 运行态收紧，C1d 已完成 `aclfile` 持久化，不能直接 `-@dangerous` 的结论仍成立。后续需在 `sub2api` 支持 Redis username 后拆分业务、exporter、admin 用户。
2. sub2api migration/runtime 拆分实施：C2f 已定位失败点；C2g 已确认当前上游未发现独立 migration-only 命令或关闭启动自动 migration 的开关。后续需应用侧新增或确认该能力后，再尝试 runtime 低权限用户切换。

### 云侧已核对与限制

1. 云主账号 MFA 已开启；账号邮箱/手机号可用；主账号未共用。
2. 当前无 API Key。
3. 云厂商无安全组/云防火墙/网络规则能力，已记录为厂商限制。
4. 云厂商无快照、审计与安全通知能力，已记录为厂商限制。
5. 账单、余额、到期、实例维护事件通知按用户要求暂缓。

## 4. 后续执行口径

1. 当前单机生产模型已收尾，后续按 `runbooks/records/losangeles-single-host-production-closure-20260706.md` 做日常维护。
2. 批次 A、B1、B2、B3、C1、C3a、C3b、C3c 已完成；Postgres 角色权限只读复核、C2f sub2api migration/runtime 失败点定位和 C2g migration 能力分析已完成。
3. Redis 分用户治理、sub2api 应用侧 migration/runtime 拆分属于维护窗口或应用侧能力配合项。
4. 跨机器实机恢复、高可用、多机部署属于未来有第二台主机或明确 SLA 需求后的升级项。
5. 云侧 D2 已由用户在控制台核对并补台账；账单/到期治理按用户要求后续单独处理。

## 5. 2026-07-06 C2g sub2api migration 能力分析

状态：完成；运行态不变。

结论：当前上游源码和生产镜像受控副本未发现独立 migration-only 命令，也未发现关闭启动自动 migration 的配置项、环境变量或命令行参数。因此 `sub2api` runtime 低权限切换仍需应用侧能力配合；确认前不要再次直接强切 `DATABASE_USER=sub2api_app`。

留痕：

- `runbooks/records/losangeles-standards-09-c2g-sub2api-migration-capability-analysis-20260706.md`

## 6. 2026-07-06 C1b Redis ACL / 高危命令兼容性分析

状态：分析完成；运行态不变。

结论：不能直接用 `-@dangerous` 一刀切收紧 Redis ACL，因为 Redis 8 的该类别包含 `INFO`、`CONFIG GET`、`SLOWLOG`、`LATENCY` 等监控/诊断命令。`sub2api` 未发现直接使用 `FLUSHALL`、`FLUSHDB`、`CONFIG`、`SHUTDOWN`、`KEYS`，但依赖 Lua、`SCAN`、`PUB/SUB`、hash/set/zset、pipeline 等能力。第一阶段建议维护窗口内精确禁用破坏性命令；分用户 ACL 需要应用侧 Redis username 支持。

留痕：

- `runbooks/records/losangeles-standards-09-c1b-redis-acl-compatibility-analysis-20260706.md`


## 7. 2026-07-06 C1c Redis ACL 阶段 1 实施

状态：完成；C1d 已完成持久化 `aclfile`。

结论：已精确禁用 Redis `default` 用户上的破坏性 / 高风险管理命令，包括 `FLUSHALL`、`FLUSHDB`、`SHUTDOWN`、`DEBUG`、`MONITOR`、`KEYS`、`CLIENT KILL/PAUSE`、`CONFIG SET/REWRITE`、`REPLICAOF/SLAVEOF`、`MODULE LOAD/LOADEX/UNLOAD`。保留 `INFO`、`CONFIG GET`、`CLIENT LIST`、`BGSAVE`、`SAVE`、`BGREWRITEAOF`、`SCAN`、`EVAL`、`SCRIPT LOAD` 和 `ACL SETUSER`。

同时修复 Redis 本机备份脚本，改为等待 `BGSAVE` 完成后打包稳定 `dump.rdb` 快照，避免在线打包 AOF 时失败。验证 ACL dry-run、Redis 备份、容器健康、HTTP health 和近期日志均通过。

留痕：

- `runbooks/records/losangeles-standards-09-c1c-redis-acl-stage1-implementation-20260706.md`


## 8. 2026-07-06 C1d Redis ACL 持久化实施

状态：完成。

结论：Redis ACL 阶段 1 已通过 `/data/users.acl` 持久化，Redis 容器重启后会加载同一套 default 用户 ACL 规则。运行副本和 Git 受控副本 compose 已同步，`sub2api-redis` 重建后健康，Redis 备份、公开 health 和近期日志检查均通过。

留痕：

- `runbooks/records/losangeles-standards-09-c1d-redis-acl-persistence-20260706.md`


## 9. 2026-07-06 C1e Redis ACL 备份覆盖

状态：完成。

结论：Redis 备份脚本已在 `users.acl` 存在时纳入该文件，备份 metadata 会记录 `aclfile_included=yes/no`，新生成的 Redis 备份包固定为 `0600`。验证确认新备份包包含 `metadata.txt`、`redis_data/dump.rdb`、`redis_data/users.acl`，未打印 ACL 内容，备份指标刷新成功，Redis / sub2api 运行正常。

留痕：

- `runbooks/records/losangeles-standards-09-c1e-redis-acl-backup-coverage-20260706.md`


## 10. 2026-07-06 C1f R2 异地备份复核

状态：完成。

结论：R2 异地备份链路已复核。最新 Redis 备份 `redis/redis-20260706-023215.tar.gz` 已在 R2 `losangeles/` 前缀下可见，远端对象大小为 `136204` 字节，未下载或展开备份内容。已修复 rclone/R2 上传后 HEAD 501 误报，`sync-r2.sh` 增加 `--s3-no-head`；修正后清洁同步日志无 `NotImplemented`、`status code: 501` 或 `ERROR`，R2 同步指标已刷新。

留痕：

- `runbooks/records/losangeles-standards-09-c1f-r2-backup-sync-verification-20260706.md`


## 11. 2026-07-06 C1g R2 隔离恢复演练

状态：完成。

结论：已从 R2 拉回选定恢复点并在临时隔离环境完成恢复验证。Postgres gzip、Redis tar、configs tar、volumes tar 均通过完整性检查；临时 Redis 容器以 `--network none` 加载 `dump.rdb + users.acl` 成功，认证 `PING` 正常，`DBSIZE=188`，`aclfile=/data/users.acl`，`FLUSHALL` 被 ACL 拒绝。演练未覆盖生产数据，未下载后展开敏感内容到 Git，临时容器和临时目录已清理。

留痕：

- `runbooks/records/losangeles-standards-09-c1g-r2-isolated-restore-drill-20260706.md`

## 12. 2026-07-06 C1h Postgres 隔离恢复演练

状态：完成。

结论：已从 R2 拉回 `sub2api-postgres` 与 `account-vault-postgres-1` 的选定 Postgres dump，并导入同版本 `--network none` 临时 Postgres 容器。`sub2api-postgres` 验证结果为 `roles=19`、`databases=2`、`connectable_databases=2`、`total_relations=560`；`account-vault-postgres-1` 验证结果为 `roles=15`、`databases=2`、`connectable_databases=2`、`total_relations=422`。演练发现并规避官方 Postgres 镜像初始化阶段 `pg_isready` 过早通过的竞态；生产容器演练后均为 `running healthy`。

留痕：

- `runbooks/records/losangeles-standards-09-c1h-postgres-isolated-restore-drill-20260706.md`
