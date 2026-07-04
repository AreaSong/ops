# LosAngeles standards/09 批次 B1：日志与内核基线收敛

更新时间：2026-07-05  
服务器：LosAngeles  
范围：日志保留、journald 限额、基础 sysctl 基线  
风险级别：低到中；未重启 Docker，未修改 Redis，未修改 fstab，未限制 SSH 来源 IP

## 1. 本批次完成项

### 1.1 journald 持久化与容量上限

已新增：

- `/etc/systemd/journald.conf.d/90-ops-limits.conf`

配置：

```ini
[Journal]
Storage=persistent
SystemMaxUse=1G
RuntimeMaxUse=256M
```

目的：

- 保留 systemd journal 日志，便于故障追溯。
- 限制日志最大占用，避免日志无限增长挤占磁盘。

已执行：

- `systemctl restart systemd-journald`

影响：

- 只重启 journald，不影响 Docker、Nginx、数据库或业务容器。

### 1.2 logrotate 保留期延长

已将以下日志轮转保留期从默认短保留调整为 26 周：

- `/etc/logrotate.d/rsyslog`
- `/etc/logrotate.d/fail2ban`
- `/etc/logrotate.d/ufw`

目的：

- 保留约半年主机安全与系统日志，方便审计、追溯和安全事件复盘。

验证：

- `logrotate -d /etc/logrotate.conf` 通过。
- 已清理上一次失败执行遗留在 `/etc/logrotate.d` 内的 `.bak-*` 文件；这些备份文件会被 logrotate 误识别为配置，造成 duplicate log entry。

### 1.3 sysctl 基线

已新增：

- `/etc/sysctl.d/99-ops-baseline.conf`

当前基线：

```ini
net.ipv4.tcp_syncookies = 1
net.ipv4.conf.all.rp_filter = 2
net.ipv4.conf.default.rp_filter = 2
net.ipv4.ip_forward = 1
net.ipv4.tcp_congestion_control = bbr
net.core.default_qdisc = fq
kernel.kptr_restrict = 1
kernel.yama.ptrace_scope = 1
kernel.unprivileged_bpf_disabled = 2
```

说明：

- `net.ipv4.ip_forward` 保持为 `1`，因为 Docker 网络依赖转发能力。
- 本批次未做会影响业务网络模型的激进内核参数调整。

## 2. 回滚记录

配置变更前备份位于：

- `/root/ops-change-backups/standards09-b1-<timestamp>/`

如需回滚，可从该目录恢复对应配置后重新执行：

```bash
sudo sysctl --system
sudo systemctl restart systemd-journald
sudo logrotate -d /etc/logrotate.conf
```

## 3. 验收结论

本批次完成后：

- journald 具备持久化与容量上限。
- 系统、认证、UFW、Fail2ban 日志保留期提升到约半年。
- 基础 sysctl 参数形成可追踪配置文件。
- 未触碰 Docker 重启、Redis 密码、fstab、SSH 来源限制等维护窗口事项。

状态：完成。
