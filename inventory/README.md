# 台账维护说明

## 文件说明

| 文件 | 格式 | 用途 |
|------|------|------|
| servers.yaml | YAML | 主机清单（机器可读，Ansible 消费） |
| services.yaml | YAML | 服务与端口（机器可读） |
| servers.md | Markdown | 主机清单（人类可读摘要） |
| ports.md | Markdown | 端口分配表（人类可读摘要） |

## 维护规则

1. **YAML 是唯一事实源**，Markdown 是人类可读视图
2. 变更时 YAML 和 Markdown **同步更新**
3. git commit message 格式：`[inventory] 描述变更内容`
4. Warp Agent 会话开始时读取 YAML 文件了解环境

## YAML 字段说明

### servers.yaml

| 字段 | 必填 | 说明 |
|------|------|------|
| hostname | 是 | 主机名，遵循命名规范 |
| cloud | 是 | aliyun / tencent |
| region | 是 | 云区域 |
| public_ip | 否 | 公网 IP，无则留空 |
| private_ip | 是 | 内网 IP |
| os | 是 | 操作系统 |
| roles | 是 | 角色列表 |
| services | 否 | 运行的服务列表 |
| data_disk | 否 | 是否有独立数据盘 |
| notes | 否 | 备注 |

### services.yaml

| 字段 | 必填 | 说明 |
|------|------|------|
| name | 是 | 服务名 |
| host | 是 | 所在主机 |
| deploy_type | 是 | docker-compose / systemd / k8s |
| compose_path | 否 | Compose 目录路径 |
| ports | 否 | 端口列表 |
| dependencies | 否 | 依赖的其他服务 |
| backup | 否 | 是否需要备份 |
| monitoring | 否 | 已接入的监控组件 |

## 从 YAML 生成 Ansible Inventory

```bash
python3 scripts/generate-ansible-inventory.py
```

输出到 `ansible/inventory/hosts.yml`。
