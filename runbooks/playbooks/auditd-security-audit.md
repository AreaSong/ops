# LosAngeles auditd 安全审计

## 目标与边界

auditd 用于回答“哪个登录用户修改了关键配置、哪个登录用户执行了提权命令”。
受管规则覆盖身份文件、sudoers、SSH、systemd、auditd 自身配置、`/opt/ops`
以及由 `auid >= 1000` 的登录用户触发且 `euid=0` 的 `execve`。

规则不采集 root 守护进程和容器自身产生的全部命令，避免不可控的事件量。
Promtail 在发送 Loki 前丢弃 `EXECVE` 和 `PROCTITLE`，避免把 argv 中可能存在的
密码、令牌、数据库 URI 或私有路径复制到共享日志存储。原始 audit 日志仍是敏感文件，
仅 root/adm 可读。

## 部署前检查

```bash
ansible-playbook auditd.yml --check --diff --limit LosAngeles
promtool check rules observability/prometheus/rules/*.yml
docker run --rm -v "$PWD/observability/promtail/promtail-config.yml:/etc/promtail/config.yml:ro" \
  grafana/promtail:3.1.0 -config.file=/etc/promtail/config.yml -check-syntax
```

`--check --diff` 只展示包和文件差异，不会执行 auditd restart、规则加载或运行态
巡检；首次安装且 `/etc/audit` 尚不存在时，它还不能模拟目标目录中的模板写入。
部署前还必须在主机核验：

```bash
command -v auditctl >/dev/null && sudo auditctl -s || true
sudo df -h /var/log
sudo find /etc/audit/rules.d -maxdepth 1 -type f -print 2>/dev/null || true
```

若 `auditctl -s` 显示 `enabled 2`，或 `/etc/audit/rules.d/*.rules` 存在有效的 `-e 2`
指令，立即停止；规则已 immutable 或将在加载后变为 immutable，在线加载和回滚都需要
维护窗口重启。默认阈值要求 `/var/log` 所在文件系统至少高于 `space_left` 额外
2 GiB，角色会在覆盖配置前阻断式检查。

生产部署必须单独批准。角色会在写入前把完整 `/etc/audit/`、内核状态和已加载
规则保存到 root-only 的 `/var/backups/ops/auditd-<UTC timestamp>/`，并输出路径。
实施前仍需单独备份 Promtail/Prometheus/Alertmanager 配置和安全 Dashboard。
批准后仅运行 `ansible-playbook auditd.yml --limit LosAngeles`，不要用完整
`baseline.yml` 代替，以免把 SSH、UFW 或 Fail2ban 变更混入同一批次。
可观测文件、security collector cron、Promtail 和 Prometheus 规则必须先完成部署，
再安装 auditd，避免审计已经启用但没有指标和告警的观察盲区。正式执行后使用
`ansible-playbook audit.yml --limit LosAngeles` 进行只读巡检，不要附加 `--check`。

## 运行态验证

```bash
systemctl is-active auditd
systemctl is-enabled auditd
auditctl -s
auditctl -l
ausearch -k sudoers --start today
ausearch -k rootcmd --start today
```

验收条件：

1. `auditd` 为 active/enabled，`auditctl -s` 的 `enabled` 必须为 1、`lost` 为 0。
2. `identity`、`sudoers`、`sshd`、`systemd`、`auditconfig`、`opsconfig`、`rootcmd`
   七个 key 全部存在。
3. 使用不含敏感参数的 `sudo /usr/bin/true` 产生 `rootcmd` 事件。
4. security collector 每分钟写入固定、不含参数的 `ops-audit-pipeline-probe`，并验证
   该事件在 5 分钟内到达 Loki；`audit_log_pipeline_check_success` 必须为 `1`。
5. 安全指标在 5 分钟内新鲜，Auditd 和日志管道告警均不处于 firing。
6. Grafana 安全面板可见 auditd 状态；Loki 查询能看到过滤后的事件，且没有
   `type=EXECVE` 或 `type=PROCTITLE`。

```bash
curl -fsSG http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=audit_log_pipeline_check_success'
curl -fsSG http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=time() - audit_log_pipeline_last_event_timestamp_seconds'
curl -fsSG http://127.0.0.1:3100/loki/api/v1/query_range \
  --data-urlencode 'query={job="auditd",host="LosAngeles"} |= "ops-audit-pipeline-probe"' \
  --data-urlencode 'since=10m' \
  --data-urlencode 'limit=5'
```

collector cron 日志写入 `/var/log/observability/security-metrics.log`，由
`/etc/logrotate.d/ops-observability` 统一轮转。

## 告警处置

- `AuditdServiceDown` / `AuditdKernelDisabled`：确认内核审计状态和 auditd journal，
  在恢复前停止高风险配置变更。
- `AuditdRulesMissing`：比较 `auditctl -l` 与受管规则，不直接在运行态临时追加未知规则。
- `AuditdEventsLost`：检查 backlog、磁盘和 auditd journal；事件丢失属于审计证据缺口。
- `AuditdBacklogHigh`：检查事件突增来源和 `backlog_limit`，不要先关闭规则止警。

## 留存与防篡改

`50 MiB x 14` 的本地轮转只提供约 700 MiB 容量上限，不能证明 180 天留存。
当前 Loki 有 30 天热存储，但与被审计主机同机，仍不能作为防篡改归档。

每日敏感日志封装、哈希链、Worker 追加式写入和独立只读回验见
`runbooks/playbooks/compliance-log-archive.md`。Cloudflare R2 当前不支持 Object Lock；若要求
云厂商级 WORM，必须另选支持保留锁的对象存储。上线前先观测 7 天实际增量，以
`日均字节 x 180 x 1.3` 规划容量；bucket、Worker、token 权限和生命周期属于独立
生产控制面变更。

## 回滚

1. 仅在 `auditctl -s` 不是 `enabled 2` 时继续在线回滚；否则进入维护窗口重启方案。
2. 角色在 handler 或运行态断言失败时，会从报告的快照恢复 `auditd.conf` 和本次
   受管的 `ops-baseline.rules`，重新加载规则、重启服务并再次断言 active；play 仍以
   失败退出，避免把“已回滚”误报为部署成功。
3. 若自动回滚本身失败，使用报告的快照恢复上述两个受管文件，不覆盖其他规则文件，
   然后执行 `augenrules --load` 和 `service auditd restart`。
4. 恢复变更前的 Promtail、Prometheus、Alertmanager 和 Dashboard 文件并重建对应容器。
5. 复核 `systemctl is-active/is-enabled auditd`、`auditctl -s/-l`、七个 key、日志管道
   探针、安全指标和告警恢复。

受管规则暂不使用 `-e 2` 锁定为不可变状态，因为该选项会让规则回滚必须重启主机。
若未来有硬合规要求，应在验证云控制台逃生通道和维护窗口后单独启用。
