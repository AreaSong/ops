# Ansible 运维剧本

## 目录结构

```
ansible/
├── ansible.cfg
├── baseline.yml          # 基线配置（幂等）
├── audit.yml             # 合规巡检
├── auditd.yml            # LosAngeles auditd 独立部署
├── observability-host-jobs.yml # 日报、日志和主机采集作业
├── inventory/
│   └── hosts.yml         # 从 servers.yaml 自动生成
└── roles/
    ├── common/           # 通用配置（时区、NTP、目录）
    ├── security/         # 安全基线（SSH、防火墙、fail2ban）
    └── node_exporter/    # 监控采集端
```

## 前置条件

1. 本机安装 Ansible >= 2.14
2. SSH 密钥已配置到目标服务器
3. 生成 inventory：

```bash
python3 scripts/generate-ansible-inventory.py
```

## 基线部署

```bash
# 预演（不实际执行）
ansible-playbook baseline.yml --check --diff

# 确认后执行
ansible-playbook baseline.yml

# 仅特定组
ansible-playbook baseline.yml --limit prod
```

## 合规巡检

```bash
ansible-playbook audit.yml --limit LosAngeles
```

这是只读运行态巡检，不要附加 `--check`；`--check` 会跳过部分 auditd 服务、内核和
规则验证，不能作为 auditd 上线通过证据。

## LosAngeles auditd 独立部署

auditd 与 SSH/UFW/Fail2ban 分批实施。先预演，取得单次生产批准后再执行：

```bash
ansible-playbook auditd.yml --check --diff --limit LosAngeles
ansible-playbook auditd.yml --limit LosAngeles
```

## LosAngeles 日报、日志审计和合规归档

先部署四类 host collector 和 logrotate：

```bash
ansible-playbook observability-host-jobs.yml --check --diff --limit LosAngeles
ansible-playbook observability-host-jobs.yml --limit LosAngeles
```

Cloudflare Worker、两个 root-only 凭据文件和只读 R2 token 就绪后，再显式启用每日
合规归档；默认不开启，避免把未配置的云端链路误装成成功：

```bash
ansible-playbook observability-host-jobs.yml --check --diff --limit LosAngeles \
  -e compliance_archive_enabled=true
ansible-playbook observability-host-jobs.yml --limit LosAngeles \
  -e compliance_archive_enabled=true
```

play 输出的 `backup_file` 必须记录。回滚时先移除受管 cron，再按输出路径恢复原文件，
例如：

```bash
sudo install -o root -g root -m 0755 "$BACKUP_FILE" /opt/ops/observability/scripts/<name>
sudo install -o root -g root -m 0644 "$CRON_BACKUP" /etc/cron.d/<name>
```

## 注意事项

- 首次运行前先 `--check --diff` 预演
- 生产环境逐组执行，不要一次跑全部
- SSH 配置变更后保持当前会话，新开会话验证
