# 故障处置手册

> 真实故障复盘后持续补充。复盘使用 `postmortem-template.md`。

## 当前状态与日常操作

| 场景 | 文件 |
| --- | --- |
| 当前运维状态 | [losangeles-current-status.md](losangeles-current-status.md) |
| 第二轮治理、批准与验收 | [losangeles-round2-governance-20260718.md](losangeles-round2-governance-20260718.md) |
| 加固核查进度 | [losangeles-hardening-progress.md](losangeles-hardening-progress.md) |
| 第一轮单机生产收尾 | [losangeles-single-host-production-closure-20260706.md](losangeles-single-host-production-closure-20260706.md) |
| 标准应用部署 | [losangeles-standard-app-deploy.md](losangeles-standard-app-deploy.md) |
| Account Vault 发布与回滚 | [losangeles-account-vault-release.md](losangeles-account-vault-release.md) |
| 每日运维与流量审计 | [daily-ops-audit.md](daily-ops-audit.md) |
| 完整备份集与 R2 校验 | [backup-set-integrity.md](backup-set-integrity.md) |
| R2 备份操作 | [losangeles-r2-backup.md](losangeles-r2-backup.md) |
| 外部可用性监控 | [github-external-uptime.md](github-external-uptime.md) |
| auditd 安全审计 | [auditd-security-audit.md](auditd-security-audit.md) |
| sub2api SLO 与容量 | [sub2api-slo-capacity.md](sub2api-slo-capacity.md) |
| 合规日志异地归档 | [compliance-log-archive.md](compliance-log-archive.md) |

## 故障与恢复

| 场景 | 文件 |
| --- | --- |
| 服务返回 5xx | [service-5xx.md](service-5xx.md) |
| 磁盘空间不足 | [disk-full.md](disk-full.md) |
| MySQL 慢查询 | [mysql-slow-query.md](mysql-slow-query.md) |
| 主机失联 | [host-unreachable.md](host-unreachable.md) |
| AreaForge 隔离恢复 | [areaforge-isolated-restore-drill.md](areaforge-isolated-restore-drill.md) |
| 跨机器恢复 | [losangeles-cross-machine-restore-drill.md](losangeles-cross-machine-restore-drill.md) |
| 应用级恢复演练 | [losangeles-app-restore-drill-20260704.md](losangeles-app-restore-drill-20260704.md) |
| 本机备份恢复演练 | [losangeles-backup-restore-drill-20260703.md](losangeles-backup-restore-drill-20260703.md) |
| R2 拉回恢复演练 | [losangeles-r2-restore-drill-20260703.md](losangeles-r2-restore-drill-20260703.md) |
| 故障复盘模板 | [postmortem-template.md](postmortem-template.md) |

## 历史迁移与事件

| 记录 | 文件 |
| --- | --- |
| Account Vault 源码迁移 | [losangeles-account-vault-source-migration-20260703.md](losangeles-account-vault-source-migration-20260703.md) |
| sub2api 数据迁移 | [losangeles-sub2api-data-migration-20260703.md](losangeles-sub2api-data-migration-20260703.md) |
| JadeAI 指纹事件 | [losangeles-jadeai-fingerprint-incident-20260703.md](losangeles-jadeai-fingerprint-incident-20260703.md) |
| R2 生命周期策略 | [losangeles-r2-lifecycle-policy-20260703.md](losangeles-r2-lifecycle-policy-20260703.md) |
| root 历史归档 | [losangeles-root-history-archive-20260703.md](losangeles-root-history-archive-20260703.md) |
| root 服务目录审计 | [losangeles-root-service-directory-audit-20260703.md](losangeles-root-service-directory-audit-20260703.md) |

## Standards 09 审计与变更记录

| 主题 | 文件 |
| --- | --- |
| 总验收矩阵 | [losangeles-standards-09-audit.md](losangeles-standards-09-audit.md) |
| 全量只读审计 | [losangeles-standards-09-full-audit-20260705.md](losangeles-standards-09-full-audit-20260705.md) |
| 手册覆盖审计 | [losangeles-standards-09-handbook-coverage-20260705.md](losangeles-standards-09-handbook-coverage-20260705.md) |
| 批次 A | [losangeles-standards-09-batch-a-20260705.md](losangeles-standards-09-batch-a-20260705.md) |
| 批次 B1 | [losangeles-standards-09-batch-b1-20260705.md](losangeles-standards-09-batch-b1-20260705.md) |
| 批次 B2 | [losangeles-standards-09-batch-b2-20260705.md](losangeles-standards-09-batch-b2-20260705.md) |
| A1 Nginx 安全头 | [losangeles-standards-09-a1-nginx-security-headers-20260705.md](losangeles-standards-09-a1-nginx-security-headers-20260705.md) |
| B3 fstab UUID | [losangeles-standards-09-b3-fstab-uuid-20260706.md](losangeles-standards-09-b3-fstab-uuid-20260706.md) |
| C0 容器数据审计 | [losangeles-standards-09-c0-container-data-audit-20260705.md](losangeles-standards-09-c0-container-data-audit-20260705.md) |
| C1 Redis 加固 | [losangeles-standards-09-c1-redis-hardening-20260705.md](losangeles-standards-09-c1-redis-hardening-20260705.md) |
| C1 Redis 认证跟进 | [losangeles-standards-09-c1-redis-auth-followup-20260705.md](losangeles-standards-09-c1-redis-auth-followup-20260705.md) |
| C1 Redis 策略复核 | [losangeles-standards-09-c1-redis-policy-readonly-audit-20260706.md](losangeles-standards-09-c1-redis-policy-readonly-audit-20260706.md) |
| C1b ACL 兼容性 | [losangeles-standards-09-c1b-redis-acl-compatibility-analysis-20260706.md](losangeles-standards-09-c1b-redis-acl-compatibility-analysis-20260706.md) |
| C1c ACL 阶段 1 | [losangeles-standards-09-c1c-redis-acl-stage1-implementation-20260706.md](losangeles-standards-09-c1c-redis-acl-stage1-implementation-20260706.md) |
| C1d ACL 持久化 | [losangeles-standards-09-c1d-redis-acl-persistence-20260706.md](losangeles-standards-09-c1d-redis-acl-persistence-20260706.md) |
| C1e ACL 备份覆盖 | [losangeles-standards-09-c1e-redis-acl-backup-coverage-20260706.md](losangeles-standards-09-c1e-redis-acl-backup-coverage-20260706.md) |
| C1f R2 同步复核 | [losangeles-standards-09-c1f-r2-backup-sync-verification-20260706.md](losangeles-standards-09-c1f-r2-backup-sync-verification-20260706.md) |
| C1g R2 隔离恢复 | [losangeles-standards-09-c1g-r2-isolated-restore-drill-20260706.md](losangeles-standards-09-c1g-r2-isolated-restore-drill-20260706.md) |
| C1h PostgreSQL 隔离恢复 | [losangeles-standards-09-c1h-postgres-isolated-restore-drill-20260706.md](losangeles-standards-09-c1h-postgres-isolated-restore-drill-20260706.md) |
| C2 PostgreSQL 权限计划 | [losangeles-standards-09-c2-postgres-permission-plan-20260705.md](losangeles-standards-09-c2-postgres-permission-plan-20260705.md) |
| C2a sub2api 应用角色 | [losangeles-standards-09-c2a-sub2api-postgres-app-role-20260705.md](losangeles-standards-09-c2a-sub2api-postgres-app-role-20260705.md) |
| C2b sub2api 低权限切换尝试 | [losangeles-standards-09-c2b-sub2api-low-privilege-switch-attempt-20260705.md](losangeles-standards-09-c2b-sub2api-low-privilege-switch-attempt-20260705.md) |
| C2c Account Vault 应用角色 | [losangeles-standards-09-c2c-account-vault-postgres-app-role-20260705.md](losangeles-standards-09-c2c-account-vault-postgres-app-role-20260705.md) |
| C2d Account Vault 低权限切换 | [losangeles-standards-09-c2d-account-vault-switch-low-privilege-db-20260705.md](losangeles-standards-09-c2d-account-vault-switch-low-privilege-db-20260705.md) |
| C2e PostgreSQL 角色复核 | [losangeles-standards-09-c2e-postgres-role-readonly-audit-20260705.md](losangeles-standards-09-c2e-postgres-role-readonly-audit-20260705.md) |
| C2f sub2api migration/runtime | [losangeles-standards-09-c2f-sub2api-migration-runtime-analysis-20260706.md](losangeles-standards-09-c2f-sub2api-migration-runtime-analysis-20260706.md) |
| C2g sub2api migration 能力 | [losangeles-standards-09-c2g-sub2api-migration-capability-analysis-20260706.md](losangeles-standards-09-c2g-sub2api-migration-capability-analysis-20260706.md) |
| C3a 业务资源限制 | [losangeles-standards-09-c3a-business-resource-limits-20260705.md](losangeles-standards-09-c3a-business-resource-limits-20260705.md) |
| C3b 监控资源限制 | [losangeles-standards-09-c3b-observability-resource-limits-20260705.md](losangeles-standards-09-c3b-observability-resource-limits-20260705.md) |
| C3c 运行态资源限制审计 | [losangeles-standards-09-c3c-container-runtime-resource-limit-audit-20260706.md](losangeles-standards-09-c3c-container-runtime-resource-limit-audit-20260706.md) |
| C4 容器日志限制 | [losangeles-standards-09-c4-container-logging-limits-20260705.md](losangeles-standards-09-c4-container-logging-limits-20260705.md) |
| C5 镜像 digest | [losangeles-standards-09-c5-image-digest-pinning-20260705.md](losangeles-standards-09-c5-image-digest-pinning-20260705.md) |
| C6a no-new-privileges | [losangeles-standards-09-c6a-no-new-privileges-20260705.md](losangeles-standards-09-c6a-no-new-privileges-20260705.md) |
| C6b cap-drop | [losangeles-standards-09-c6b-cap-drop-monitoring-helpers-20260705.md](losangeles-standards-09-c6b-cap-drop-monitoring-helpers-20260705.md) |
| C7 PostgreSQL exporter 兼容性 | [losangeles-standards-09-c7-postgres-exporter-pg18-compatibility-20260706.md](losangeles-standards-09-c7-postgres-exporter-pg18-compatibility-20260706.md) |
| D2 云控制面 | [losangeles-standards-09-d2-cloud-control-plane-governance-20260705.md](losangeles-standards-09-d2-cloud-control-plane-governance-20260705.md) |
| G1 受控 Compose | [losangeles-standards-09-g1-service-compose-controlled-copies-20260705.md](losangeles-standards-09-g1-service-compose-controlled-copies-20260705.md) |
| R1 备份恢复 | [losangeles-standards-09-r1-backup-restore-drill-20260705.md](losangeles-standards-09-r1-backup-restore-drill-20260705.md) |

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
