# AreaSong Ops 控制面运维手册

## 当前状态

仓库中的 Runner、Web、适配器、Nginx、Cloudflare Access 目标配置、监控、日志、备份和
看板已经形成受控目标态。生产 `areasong-ops-runner.service` 与 Unix Socket 已完成隔离验收；
Web 容器、Nginx、`ops.areasong.top` DNS/Access 和完整可观测接入仍需按独立变更逐项批准。
在对应台账状态晋升前，不把本手册中的其余目标命令视为已执行事实。

## 责任边界

```text
Cloudflare Access -> Nginx -> 非 root Web -> root:areasong-ops Unix Socket -> root Runner
                                                                          |
                                                               固定服务适配器
```

- Cloudflare Access 只负责人员入口，目标策略仅允许 `song80184@gmail.com` Email OTP。
- Web 验证 Access JWT、同源 Origin 和 CSRF，只负责预览、确认与任务展示。
- Web 不挂载 Docker Socket、SQLite、备份目录、业务卷，不提供 Shell 或文件管理。
- Runner 独占 SQLite 和 root 权限，只执行 `services.json` 中声明的服务、动作和适配器。
- 适配器不能从请求接收命令、脚本、Compose 路径、环境文件路径、镜像引用或批量目标。
- Prometheus 是唯一告警规则源；Grafana 只查看 Prometheus 告警和 Alertmanager 静默。

## 关键路径

| 对象 | 路径 |
| --- | --- |
| 受控源码 | `/opt/ops/services/areasong-ops` |
| 运行 Compose | `/opt/services/areasong-ops/compose.yml` |
| 非敏感构建参数 | `/opt/services/areasong-ops/.env` |
| 服务能力声明 | `/etc/areasong-ops/services.json`，`root:root 0600` |
| Access 配置 | `/etc/areasong-ops/web.env`，`root:root 0600` |
| Runner | `/usr/local/libexec/areasong-ops/areasong-ops-runner` |
| Unix Socket | `/run/areasong-ops/runner.sock`，`root:areasong-ops 0660` |
| SQLite 与操作证据 | `/var/lib/areasong-ops` |
| systemd 日志 | `journalctl -u areasong-ops-runner.service` |
| Web Docker 日志 | 容器 `areasong-ops-web` |

## 正常操作流程

1. 在 Web 选择单个服务和类型化动作。
2. Web 请求 Runner 执行只读预检，展示真实当前身份、影响、风险、步骤和回滚说明。
3. 操作者核对预览，在有效期内输入精确确认短语。
4. Runner 再核对操作者哈希、预览快照、幂等键、服务锁和全局备份锁。
5. Runner 按固定阶段执行并持续记录事件；Web 只轮询任务和事件。
6. 任务成功后核对健康、运行身份、告警和日志；失败时按适配器契约回滚。

同一幂等键只能重放完全相同的请求。出现 `recovery_uncertain`、身份漂移、阶段输出不完整
或进程中断时，停止自动重试，先核对容器、Compose、数据库 migration、备份和任务证据。

## 首轮只读诊断

以下命令不修改生产状态：

```bash
sudo systemctl status areasong-ops-runner.service --no-pager
sudo journalctl -u areasong-ops-runner.service -n 200 --no-pager
sudo stat -c '%a %U:%G %n' /run/areasong-ops/runner.sock
sudo curl -fsS --unix-socket /run/areasong-ops/runner.sock http://runner/healthz
sudo docker inspect areasong-ops-web
curl -fsS http://127.0.0.1:3200/healthz
curl -fsS http://127.0.0.1:3200/metrics
```

运行态预检会同时核对 Git revision、Runner 组 GID、Socket 权限、Web 非 root/只读 rootfs、
无 Docker Socket，以及 Web/Runner 两条构建指标的 revision 完全相同：

```bash
sudo /opt/ops/services/areasong-ops/deploy/preflight.sh runtime
```

## 指标、日志和看板

- Prometheus job：`areasong_ops`，固定 `service="areasong-ops"`。
- Access 策略探针：`blackbox_access_policy`，预期未认证请求为 Cloudflare Access `302`。
- Web Docker 日志：`job="docker", service="areasong-ops", component="web"`。
- Runner syslog：`job="syslog", service="areasong-ops", component="runner"`。
- Nginx：`service="areasong-ops"`。
- Grafana：`AreaSong Ops 控制面`，UID `areasong-ops-control-plane`。

Prometheus Explore 到 Loki 的关联只使用共享 `service` 标签。`job` 和 `instance` 在指标与日志
两侧语义不同，不作为关联键。

## 告警处置

### `AreaSongOpsMetricsTargetDown`

1. 查 Web 容器是否运行、健康检查是否通过。
2. 查 Web 是否加入 `areasong-ops-network`，Prometheus 是否加入同一外部网络。
3. 从 Prometheus 容器网络访问 `areasong-ops-web:8080/metrics`。
4. Web 正常但抓取失败时查 Prometheus target 错误，不先重启 Runner。

### `AreaSongOpsRunnerUnreachable`

1. 查 Runner unit、最近 200 行日志和 Socket owner/mode。
2. 查 Web 容器的 supplementary GID 是否等于宿主 `areasong-ops` 组 GID。
3. 禁止通过挂载 Docker Socket或放宽 Socket 为 world-writable 绕过权限问题。
4. 若 revision 不一致，按构建身份回滚或重建单个组件，不继续执行任务。

### `AreaSongOpsActiveTaskOvertime`

1. 在 Web 与 SQLite 只读接口核对任务当前阶段和最后事件。
2. 查对应服务真实容器身份、健康与适配器证据。
3. 不自动重试；确认原任务是否仍持有服务锁或备份锁。
4. 无法证明完成或可回滚时标记人工恢复并保留全部操作证据。

### `AreaSongOpsSqliteSnapshotStale`

1. 查 Runner maintenance 日志中的快照或清理失败。
2. 核对 `/var/lib/areasong-ops/snapshots` 为真实目录且最新 `ops-*.db` 小于 25 小时。
3. 只读执行 SQLite `PRAGMA integrity_check`；不要复制活跃 WAL 数据库代替 `VACUUM INTO` 快照。
4. 快照恢复前不要把后续 volume 归档视为满足 RPO。

### `AreaSongOpsRestoreDrillStale`

选择同一完整 manifest 执行：

```bash
sudo /opt/ops/scripts/backup/restore_areasong_ops_isolated.py \
  --manifest manifests/backup-set-YYYYMMDD-HHMMSS.json
```

脚本只在临时目录恢复 `ops.db` 并检查完整性、外键、关键表、关键列并记录行数；不覆盖生产 SQLite、
不启动 Runner、不绑定端口。失败时保留旧成功时间戳，依据错误修复备份链后重新批准演练。

### `AreaSongOpsAccessPolicyProbeFailed`

1. 从外部确认未认证请求是否仍为 Access 登录 `302`。
2. 核对 DNS 为 proxied、Application hostname、Allow policy 和会话配置。
3. 核对 Nginx 使用 Cloudflare CIDR allowlist，源站 `--resolve` 直连应为 `403`。
4. 不通过临时 Bypass、公开 health 路径或复用 Grafana service token消除告警。

`AreaSongOpsAccessProbeTargetDown` 优先检查共享 Blackbox Exporter 和 Prometheus 抓取路径，
不要误判为 Access 策略本身失效。

## 备份与恢复边界

- 完整备份集固定十个角色，控制面角色是 `volume-areasong-ops-state`。
- `backup-volumes.sh` 只接受 Runner `VACUUM INTO` 生成的新鲜快照，并拒绝 operations 符号链接。
- `backup-configs.sh` 包含 `services.json`、Runner、systemd unit；`web.env` 仅记录为外部密钥前置条件，不进入普通 R2 config 归档。
- 普通恢复演练不能写回 `/var/lib/areasong-ops`。生产 SQLite 恢复属于独立高风险变更，必须另行说明影响、验证与回滚并等待批准。

## 生产变更与回滚

部署顺序以 `services/areasong-ops/deploy/deploy-checklist.md` 为准，一次只执行一个变更并立即
验证。Runner、Web、Nginx、Cloudflare DNS/Access、observability、备份和 inventory 分别
批准，不能用一次授权覆盖后续步骤。

- Runner 回滚：恢复上一 commit 的二进制、适配器、服务声明，只重启 Runner unit。
- Web 回滚：恢复上一 commit tag，只重建 Web；不触碰 Runner 和业务服务。
- Nginx 回滚：恢复站点文件，`nginx -t` 通过后 reload。
- Access 回滚：禁用或删除 Application 前必须确认源站仍受 Cloudflare CIDR 限制。
- 回滚不恢复 SQLite 或任何业务数据库；保留任务、审计和操作证据。

完成生产验收后，必须把 inventory 中的 `planned`、`pending-production-validation` 和
`observed_origin_policy: direct` 更新为实测状态，并记录验收日期与证据。
