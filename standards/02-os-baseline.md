# 02 操作系统基线

> Ubuntu/Debian 与 CentOS/RHEL 双系兼容。Ansible 基线剧本见 `ansible/baseline.yml`。

## 目录结构

代码/配置、数据、日志三分离：

| 路径 | 用途 |
|------|------|
| /opt/apps/\<服务名\>/ | 应用部署（二进制、代码、配置） |
| /opt/compose/\<项目名\>/ | Docker Compose 项目 |
| /opt/ops/ | 本仓库（规范、脚本、台账） |
| /data/\<服务名\>/ | 持久化数据 |
| /var/log/\<服务名\>/ | 应用日志 |

- 有独立数据盘的机器，/data 单独挂载并在台账注明
- 任何服务在任何机器上的位置必须可预测

## SSH 加固

```sshd_config
PermitRootLogin no
PasswordAuthentication no
PubkeyAuthentication yes
MaxAuthTries 3
ClientAliveInterval 300
ClientAliveCountMax 2
```

**修改 sshd_config 铁律**：

1. 改完先 `sshd -t` 验证语法
2. `systemctl reload sshd`（Ubuntu）或 `systemctl reload sshd`（RHEL）
3. 保持当前会话不断开，新开一个会话验证能登录
4. 成功后才算完成

## 防火墙

默认拒绝入站，按端口白名单放行。

| 系统 | 工具 | 常用命令 |
|------|------|----------|
| Ubuntu/Debian | ufw | `ufw default deny incoming` |
| CentOS/RHEL | firewalld | `firewall-cmd --set-default-zone=drop` |

- 每次放行记入 `inventory/services.yaml`
- 禁止安全组放行 0.0.0.0/0（明确公网服务端口除外，且需记录）

## fail2ban

防 SSH 爆破，基线剧本自动安装配置。

| 系统 | 包名 |
|------|------|
| Ubuntu/Debian | fail2ban |
| CentOS/RHEL | fail2ban |

## 时区与 NTP

- 所有服务器统一 **UTC 时区**
- 排障和沟通时，关键时间点同时标注北京时间（UTC+8）

| 系统 | NTP 服务 |
|------|----------|
| Ubuntu/Debian | systemd-timesyncd 或 chrony |
| CentOS/RHEL | chrony |

验证：`timedatectl status` 确认 Time zone: UTC，System clock synchronized: yes

## 自动安全更新

| 系统 | 工具 | 配置 |
|------|------|------|
| Ubuntu/Debian | unattended-upgrades | 仅安全更新自动安装 |
| CentOS/RHEL | dnf-automatic | apply_updates = yes，仅安全 |

详见 `07-patching.md` 补丁管理节奏。

## 内核参数（可选，按需启用）

```sysctl
net.ipv4.tcp_syncookies = 1
net.ipv4.conf.all.rp_filter = 1
net.ipv4.conf.default.rp_filter = 1
net.ipv4.icmp_echo_ignore_broadcasts = 1
fs.file-max = 65535
```

写入 `/etc/sysctl.d/99-ops-baseline.conf`，`sysctl --system` 生效。

## 日志留存

| 日志类型 | 路径 | 保留策略 |
|----------|------|----------|
| 系统日志 | journald | 最大 500M 或 7 天 |
| 应用日志 | /var/log/\<服务名\>/ | logrotate：每日轮转、14 天、压缩 |
| 容器日志 | Compose logging 配置 | max-size: 50m, max-file: 3 |

journald 配置（`/etc/systemd/journald.conf`）：

```ini
[Journal]
SystemMaxUse=500M
MaxRetentionSec=7day
```

## 禁止事项

- chmod 777
- 关闭防火墙/SELinux 作为解决方案
- curl | bash 执行远程脚本

---

修订记录：

- 2026-07-02 初版
