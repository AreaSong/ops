# 台账维护说明

## 文件说明

| 文件 | 格式 | 用途 |
|------|------|------|
| servers.yaml | YAML | 主机清单（机器可读，Ansible 消费） |
| services.yaml | YAML | 服务与端口（机器可读） |
| servers.md | Markdown | 主机清单（人类可读摘要） |
| ports.md | Markdown | 端口分配表（人类可读摘要） |
| cloudflare-areasong-top.md | Markdown | Cloudflare DNS/代理状态、证书策略和控制台待确认项 |

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
| cloud | 是 | 云厂商或可核验网络归属；未知时使用 `unknown` 并在 notes/provider_evidence 写明证据 |
| region | 是 | 云区域或可核验地理区域；未知时使用 `unknown` |
| public_ip | 否 | 公网 IP，无则留空 |
| private_ip | 否 | 主机级内网 IP；无 RFC1918 私网地址时留空并在 notes 说明 |
| owner | 是 | 运维/业务负责人 |
| os | 是 | 操作系统 |
| roles | 是 | 角色列表 |
| services | 否 | 运行的服务列表 |
| data_disk | 否 | 是否有独立数据盘 |
| provider_evidence | 否 | provider/region 判断依据，例如 RDAP、ASN、metadata、虚拟化信息 |
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
