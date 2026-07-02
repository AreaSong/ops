# Ansible 运维剧本

## 目录结构

```
ansible/
├── ansible.cfg
├── baseline.yml          # 基线配置（幂等）
├── audit.yml             # 合规巡检
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
ansible-playbook audit.yml --check
```

输出各主机与基线的偏差，不实际修改。

## 注意事项

- 首次运行前先 `--check --diff` 预演
- 生产环境逐组执行，不要一次跑全部
- SSH 配置变更后保持当前会话，新开会话验证
