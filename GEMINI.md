# 运维仓库入口（薄壳）

完整规则与导航中心在 `AGENTS.md`——**先读它**。本文件只内联最小路由表用于对抗上下文压缩；两处如有出入，以 `AGENTS.md` 为准。

**这是正式生产环境**：未经批准不得执行任何变更类命令（写入/重启/安装/删除/云资源变更）。

## Quick Routing（抗压缩路由表）

| 任务 | 必读 | 流程/手册 |
|------|------|----------|
| 部署新服务/应用 | `standards/04-deployment.md`、`templates/app-deploy/` | `runbooks/playbooks/losangeles-standard-app-deploy.md` |
| 服务返回 5xx | `runbooks/gotchas.md` | `runbooks/playbooks/service-5xx.md` |
| 磁盘空间不足 | `runbooks/gotchas.md` | `runbooks/playbooks/disk-full.md` |
| 主机失联 | `inventory/servers.yaml` | `runbooks/playbooks/host-unreachable.md` |
| MySQL 慢查询 | `runbooks/gotchas.md` | `runbooks/playbooks/mysql-slow-query.md` |
| 备份/恢复操作 | `standards/06-backup-dr.md` | `runbooks/playbooks/backup-set-integrity.md`、`runbooks/playbooks/losangeles-r2-backup.md` |
| 配置修改/升级等变更 | `standards/05-change-management.md`、`runbooks/gotchas.md` | 按对应域 standards 执行 |
| 新机器上线/验收 | `standards/00-server-checklist.md`、`standards/01-naming-inventory.md`、`standards/02-os-baseline.md` | — |
| 监控告警建设 | `standards/08-observability.md` | `observability/README.md` |
| 日常巡检/安全审计 | — | `runbooks/playbooks/daily-ops-audit.md`、`runbooks/playbooks/auditd-security-audit.md` |
| **其他/未列任务** | `standards.md` 索引 + `runbooks/README.md` | 按最接近的域文档执行 |

## Auto-Triggers

- **同一会话的每个新任务** → 重读 `AGENTS.md`，重新匹配路由，重读该行全部文件。"我已经读过了"无效——上下文会压缩，不同任务路由不同
- 涉及 Redis / PostgreSQL / docker compose / fstab 变更 → 先读 `runbooks/gotchas.md` 对应条目
- 任何非琐碎任务完成前 → 30 秒自检：新坑/缺规则/过时规则？符合 2/3 录入标准（可重复+代价高+不可见）就提炼进 `runbooks/gotchas.md`
- 变更完成后 → 提醒更新 `inventory/` 台账并 git 提交

## Red Flags — STOP

出现以下念头立即停止，不要自我协商：

- "就这一次跳过批准直接执行" → 停。一切变更必须先说明（做什么/为什么/影响范围/如何回滚）并等待批准
- "这次就不做复盘/不查坑点了" → 停。见 `AGENTS.md` § 同会话多任务纪律
- "路径/服务名应该是 X" → 停。用只读命令查证，查不到就问
- "报错了，换个方法再试试" → 停。先报告，再行动
