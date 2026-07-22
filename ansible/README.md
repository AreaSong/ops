# Ansible 运维剧本

## 目录结构

```
ansible/
├── ansible.cfg
├── baseline.yml          # 基线配置（幂等）
├── audit.yml             # 合规巡检
├── auditd.yml            # LosAngeles auditd 独立部署
├── cosign.yml            # 固定版本 cosign 与 OCI attestation 依赖
├── nginx-cloudflare-origin.yml # Cloudflare-only 源站事务式变更
├── observability-host-jobs.yml # 日报、日志和主机采集作业
├── templates/            # Nginx Cloudflare 访问控制模板
├── inventory/
│   └── hosts.yml         # 从 servers.yaml 自动生成
└── roles/
    ├── common/           # 通用配置（时区、NTP、目录）
    ├── security/         # 安全基线（SSH、防火墙、fail2ban）
    └── node_exporter/    # 旧 systemd 安装角色，仅保留校验过的迁移参考
```

## 前置条件

1. 本机安装 Ansible >= 2.14
2. 安装经过 SHA-256 校验的固定 Ansible collection：

```bash
sudo ./install-collections.sh
```

3. SSH 密钥已配置到目标服务器
4. 生成 inventory：

```bash
python3 scripts/generate-ansible-inventory.py
```

## 基线部署

node-exporter 由 `observability/docker-compose.yml` 单点管理，基线剧本不再安装与
Compose 竞争 9100 端口的 systemd 实例。

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

默认部署常规 host job、完整备份链和 logrotate，覆盖日报、运行快照、Docker、安全、容量、
Cloudflare IP/证书、脱敏业务日志、本地备份、manifest 与 R2 回验；另有 2 个 GitHub Issue
同步/演练 cron，只有显式启用并配置 root-only Token 后才部署。generation 同时包含脚本、
cron、logrotate 和 `generation.sha256`，安装文件只从已激活 generation 读取：

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

正式回滚必须指定已存在且非当前的 40 位 generation。playbook 会先校验目标和当前
generation 的 SHA-256、shell 与 logrotate，再原子切换并安装对应 cron；任一步失败会在
`rescue` 中自动恢复原 generation、cron 和 logrotate：

```bash
ansible-playbook observability-host-jobs-rollback.yml --check --diff --limit LosAngeles \
  -e host_jobs_rollback_release_id=<40位提交>
ansible-playbook observability-host-jobs-rollback.yml --limit LosAngeles \
  -e host_jobs_rollback_release_id=<40位提交>
```

不支持 `generation.sha256` 和 generation 内 cron 的历史版本不能作为事务式回滚目标。

## 注意事项

- 首次运行前先 `--check --diff` 预演
- 生产环境逐组执行，不要一次跑全部
- SSH 配置变更后保持当前会话，新开会话验证
