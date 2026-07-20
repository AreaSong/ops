# Ops — 企业级服务器治理体系

> 治理即代码：规范、台账、剧本、监控全部进 Git，服务器 `/opt/ops/` 是同步副本。

## 目录结构

```
ops/
├── AGENTS.md              # Agent 规则与任务路由（导航中心，Warp 自动加载）
├── CLAUDE.md / CODEX.md / GEMINI.md   # 各 AI 工具薄壳入口（路由表内联，指向 AGENTS.md）
├── .cursor/rules/         # Cursor 薄壳入口（ops-routing.mdc）
├── warp/                  # Warp Profile + allowlist/denylist 配置
├── standards/             # 八大域运维规范
├── inventory/             # 结构化台账（YAML + Markdown）
├── ansible/               # 基线剧本 + 合规巡检
├── observability/         # Prometheus + Grafana + Loki + Alertmanager
├── runbooks/              # playbooks/ 可复用流程 + records/ 一次性记录 + gotchas.md 坑点索引
└── scripts/               # 备份脚本、工具脚本
```

## 快速开始

### 1. Warp 配置

1. 阅读 [warp/profiles.md](warp/profiles.md)
2. 创建 Prod / Test 两个 Profile
3. 导入 [warp/allowlist.txt](warp/allowlist.txt) 和 [warp/denylist.txt](warp/denylist.txt)

### 2. 填写台账

1. 编辑 [inventory/servers.yaml](inventory/servers.yaml) 填入真实机器
2. 编辑 [inventory/services.yaml](inventory/services.yaml) 填入真实服务
3. 同步更新 [inventory/servers.md](inventory/servers.md) 和 [inventory/ports.md](inventory/ports.md)

### 3. 服务器基线

```bash
python3 scripts/generate-ansible-inventory.py
cd ansible
ansible-playbook baseline.yml --check --diff   # 预演
ansible-playbook baseline.yml                   # 执行
```

### 4. 可观测栈

```bash
# 在 prod-monitor-01 上
cp observability/.env.example observability/.env
# 编辑 .env 设置 Grafana 密码
docker compose -f observability/docker-compose.yml up -d
```

### 5. 各服务器部署 Promtail

```bash
# 在各服务器上
cd /opt/ops/observability/promtail
# 修改 promtail-config.yml 中的 MONITOR_IP
docker compose up -d
```

## 规范索引

| 场景 | 文档 |
|------|------|
| 命名、台账 | [standards/01-naming-inventory.md](standards/01-naming-inventory.md) |
| 系统基线 | [standards/02-os-baseline.md](standards/02-os-baseline.md) |
| 安全权限 | [standards/03-security-access.md](standards/03-security-access.md) |
| 服务部署 | [standards/04-deployment.md](standards/04-deployment.md) |
| 变更管理 | [standards/05-change-management.md](standards/05-change-management.md) |
| 备份恢复 | [standards/06-backup-dr.md](standards/06-backup-dr.md) |
| 补丁管理 | [standards/07-patching.md](standards/07-patching.md) |
| 监控告警 | [standards/08-observability.md](standards/08-observability.md) |

## 服务器同步

每台服务器克隆本仓库到 `/opt/ops/`：

```bash
git clone <repo-url> /opt/ops
cd /opt/ops && git pull --ff-only
```

凭证放 `/opt/ops/secrets.env`（不入库）。

### 生产服务器 Git 操作模型

生产服务器上的 `/opt/ops` 保持 `root:root` 管理，包含备份脚本等 root-only 运维内容。普通用户不直接配置 `safe.directory`，也不放宽 `scripts/backup` 等敏感目录权限。

需要变更时，由运维人员在共享终端执行一次 `sudo -v` 授权，然后统一使用 `sudo git -C /opt/ops ...` 完成查看、提交和推送。变更完成后执行 `sudo -k` 清理 sudo 缓存。

常用命令：

```bash
sudo -v
sudo git -C /opt/ops status --short
sudo git -C /opt/ops diff --check
sudo git -C /opt/ops add <paths>
sudo git -C /opt/ops commit -m '[scope] 描述'
sudo env GIT_SSH_COMMAND='ssh -i /root/.ssh/id_ed25519_ops_github -o IdentitiesOnly=yes -o StrictHostKeyChecking=accept-new' \
  git -C /opt/ops push origin main
sudo -k
```
