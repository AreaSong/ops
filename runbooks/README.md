# 故障处置手册

> 真实故障复盘后持续补充。复盘使用 `postmortem-template.md`。

## 手册索引

| 编号 | 场景 | 文件 |
|------|------|------|
| RB-01 | 服务返回 5xx | [service-5xx.md](service-5xx.md) |
| RB-02 | 磁盘空间不足 | [disk-full.md](disk-full.md) |
| RB-03 | MySQL 慢查询 | [mysql-slow-query.md](mysql-slow-query.md) |
| RB-04 | 机器失联 | [host-unreachable.md](host-unreachable.md) |
| RB-05 | LosAngeles 跨机器恢复演练 | [losangeles-cross-machine-restore-drill.md](losangeles-cross-machine-restore-drill.md) |
| RB-06 | LosAngeles 当前运维状态快照 | [losangeles-current-status.md](losangeles-current-status.md) |
| RB-07 | LosAngeles standards/09 验收矩阵 | [losangeles-standards-09-audit.md](losangeles-standards-09-audit.md) |
| RB-08 | LosAngeles standards/09 全量只读检查报告 | [losangeles-standards-09-full-audit-20260705.md](losangeles-standards-09-full-audit-20260705.md) |
| RB-09 | LosAngeles standards/09 整本文档覆盖检查 | [losangeles-standards-09-handbook-coverage-20260705.md](losangeles-standards-09-handbook-coverage-20260705.md) |
| RB-10 | LosAngeles standards/09 批次 A 低风险收敛记录 | [losangeles-standards-09-batch-a-20260705.md](losangeles-standards-09-batch-a-20260705.md) |
| RB-11 | LosAngeles standards/09 C2g sub2api migration 能力分析 | [losangeles-standards-09-c2g-sub2api-migration-capability-analysis-20260706.md](losangeles-standards-09-c2g-sub2api-migration-capability-analysis-20260706.md) |
| RB-12 | LosAngeles standards/09 C1b Redis ACL 兼容性分析 | [losangeles-standards-09-c1b-redis-acl-compatibility-analysis-20260706.md](losangeles-standards-09-c1b-redis-acl-compatibility-analysis-20260706.md) |
| RB-13 | LosAngeles standards/09 C1c Redis ACL 阶段 1 实施 | [losangeles-standards-09-c1c-redis-acl-stage1-implementation-20260706.md](losangeles-standards-09-c1c-redis-acl-stage1-implementation-20260706.md) |
| RB-14 | LosAngeles standards/09 C1d Redis ACL 持久化实施 | [losangeles-standards-09-c1d-redis-acl-persistence-20260706.md](losangeles-standards-09-c1d-redis-acl-persistence-20260706.md) |
| RB-15 | LosAngeles standards/09 C1e Redis ACL 备份覆盖 | [losangeles-standards-09-c1e-redis-acl-backup-coverage-20260706.md](losangeles-standards-09-c1e-redis-acl-backup-coverage-20260706.md) |
| RB-16 | LosAngeles standards/09 C1f R2 异地备份复核 | [losangeles-standards-09-c1f-r2-backup-sync-verification-20260706.md](losangeles-standards-09-c1f-r2-backup-sync-verification-20260706.md) |
| RB-17 | LosAngeles standards/09 C1g R2 隔离恢复演练 | [losangeles-standards-09-c1g-r2-isolated-restore-drill-20260706.md](losangeles-standards-09-c1g-r2-isolated-restore-drill-20260706.md) |
| RB-18 | LosAngeles standards/09 C1h Postgres 隔离恢复演练 | [losangeles-standards-09-c1h-postgres-isolated-restore-drill-20260706.md](losangeles-standards-09-c1h-postgres-isolated-restore-drill-20260706.md) |
| RB-19 | LosAngeles 单机生产收尾与日常运维清单 | [losangeles-single-host-production-closure-20260706.md](losangeles-single-host-production-closure-20260706.md) |
| RB-20 | LosAngeles 标准应用部署流程 | [losangeles-standard-app-deploy.md](losangeles-standard-app-deploy.md) |

## 通用排障原则

1. **先止血再查根因**：回滚/重启/切流量
2. **只读优先**：先收集证据，再提变更方案
3. **一次一个变更**：执行后立即验证
4. **记录一切**：时间线、命令、输出，用于复盘

## 复盘流程

1. 故障结束后 24 小时内填写 `postmortem-template.md`
2. 识别"有监控就能更早发现"的缺口
3. 更新对应 runbook（如发现新排查路径）
4. git 提交复盘记录
