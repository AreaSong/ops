# 故障处置手册

> 目录已分层：**playbooks/ 是可复用流程**（下次同类情况怎么做），**records/ 是一次性记录**（某时某事做了什么）。
> 已知坑点索引见 [gotchas.md](gotchas.md)。复盘使用 [postmortem-template.md](postmortem-template.md)。

## 目录约定

| 位置 | 放什么 | 判据 |
| --- | --- | --- |
| `playbooks/` | 可复用的操作/排障/巡检流程 | 下次遇到同类情况按它执行 |
| `records/` | 审计、演练、迁移、事件的一次性记录 | 描述"某时某事"，不再重复执行 |
| 根目录 | 索引（本文件）、[gotchas.md](gotchas.md)、[postmortem-template.md](postmortem-template.md) | 入口与模板 |

新增文件时按判据归类；复盘发现可复用坑点，按 `gotchas.md` 的录入标准提炼（2/3 门槛：可重复 + 代价高 + 代码不可见）。

## Playbooks（可复用流程）

### 故障排障

| 场景 | 文件 |
| --- | --- |
| 服务返回 5xx | [playbooks/service-5xx.md](playbooks/service-5xx.md) |
| 磁盘空间不足 | [playbooks/disk-full.md](playbooks/disk-full.md) |
| MySQL 慢查询 | [playbooks/mysql-slow-query.md](playbooks/mysql-slow-query.md) |
| 主机失联 | [playbooks/host-unreachable.md](playbooks/host-unreachable.md) |

### 备份与恢复

| 场景 | 文件 |
| --- | --- |
| 完整备份集与 R2 校验 | [playbooks/backup-set-integrity.md](playbooks/backup-set-integrity.md) |
| R2 备份操作 | [playbooks/losangeles-r2-backup.md](playbooks/losangeles-r2-backup.md) |
| AreaForge 隔离恢复 | [playbooks/areaforge-isolated-restore-drill.md](playbooks/areaforge-isolated-restore-drill.md) |
| 跨机器恢复 | [playbooks/losangeles-cross-machine-restore-drill.md](playbooks/losangeles-cross-machine-restore-drill.md) |

### 部署与日常运维

| 场景 | 文件 |
| --- | --- |
| 标准应用部署 | [playbooks/losangeles-standard-app-deploy.md](playbooks/losangeles-standard-app-deploy.md) |
| 每日运维与流量审计 | [playbooks/daily-ops-audit.md](playbooks/daily-ops-audit.md) |
| 外部可用性监控 | [playbooks/github-external-uptime.md](playbooks/github-external-uptime.md) |
| auditd 安全审计 | [playbooks/auditd-security-audit.md](playbooks/auditd-security-audit.md) |
| sub2api SLO 与容量 | [playbooks/sub2api-slo-capacity.md](playbooks/sub2api-slo-capacity.md) |
| 合规日志异地归档 | [playbooks/compliance-log-archive.md](playbooks/compliance-log-archive.md) |

## Records（一次性记录）

### 状态与治理

| 记录 | 文件 |
| --- | --- |
| 当前运维状态 | [records/losangeles-current-status.md](records/losangeles-current-status.md) |
| 第二轮治理、批准与验收 | [records/losangeles-round2-governance-20260718.md](records/losangeles-round2-governance-20260718.md) |
| 加固核查进度 | [records/losangeles-hardening-progress.md](records/losangeles-hardening-progress.md) |
| 第一轮单机生产收尾 | [records/losangeles-single-host-production-closure-20260706.md](records/losangeles-single-host-production-closure-20260706.md) |
| Account Vault 发布与回滚 | [records/losangeles-account-vault-release.md](records/losangeles-account-vault-release.md) |
| xray 流量审计 | [records/losangeles-xray-traffic-audit.md](records/losangeles-xray-traffic-audit.md) |
| x-ui TCP/Nginx 调优 | [records/losangeles-xui-tcp-nginx-tuning-20260721.md](records/losangeles-xui-tcp-nginx-tuning-20260721.md) |
| 优化路线图（A→B 默认路径） | [records/losangeles-optimization-roadmap-20260721.md](records/losangeles-optimization-roadmap-20260721.md) |
| 内存 limit 收敛 | [records/losangeles-mem-limit-tighten-20260721.md](records/losangeles-mem-limit-tighten-20260721.md) |

### 恢复演练记录

| 记录 | 文件 |
| --- | --- |
| 应用级恢复演练 | [records/losangeles-app-restore-drill-20260704.md](records/losangeles-app-restore-drill-20260704.md) |
| 本机备份恢复演练 | [records/losangeles-backup-restore-drill-20260703.md](records/losangeles-backup-restore-drill-20260703.md) |
| R2 拉回恢复演练 | [records/losangeles-r2-restore-drill-20260703.md](records/losangeles-r2-restore-drill-20260703.md) |

### 历史迁移与事件

| 记录 | 文件 |
| --- | --- |
| Account Vault 源码迁移 | [records/losangeles-account-vault-source-migration-20260703.md](records/losangeles-account-vault-source-migration-20260703.md) |
| sub2api 数据迁移 | [records/losangeles-sub2api-data-migration-20260703.md](records/losangeles-sub2api-data-migration-20260703.md) |
| JadeAI 指纹事件 | [records/losangeles-jadeai-fingerprint-incident-20260703.md](records/losangeles-jadeai-fingerprint-incident-20260703.md) |
| R2 生命周期策略 | [records/losangeles-r2-lifecycle-policy-20260703.md](records/losangeles-r2-lifecycle-policy-20260703.md) |
| root 历史归档 | [records/losangeles-root-history-archive-20260703.md](records/losangeles-root-history-archive-20260703.md) |
| root 服务目录审计 | [records/losangeles-root-service-directory-audit-20260703.md](records/losangeles-root-service-directory-audit-20260703.md) |

### Standards 09 审计与变更记录

| 主题 | 文件 |
| --- | --- |
| 总验收矩阵 | [records/losangeles-standards-09-audit.md](records/losangeles-standards-09-audit.md) |
| 全量只读审计 | [records/losangeles-standards-09-full-audit-20260705.md](records/losangeles-standards-09-full-audit-20260705.md) |
| 手册覆盖审计 | [records/losangeles-standards-09-handbook-coverage-20260705.md](records/losangeles-standards-09-handbook-coverage-20260705.md) |
| 批次 A | [records/losangeles-standards-09-batch-a-20260705.md](records/losangeles-standards-09-batch-a-20260705.md) |
| 批次 B1 | [records/losangeles-standards-09-batch-b1-20260705.md](records/losangeles-standards-09-batch-b1-20260705.md) |
| 批次 B2 | [records/losangeles-standards-09-batch-b2-20260705.md](records/losangeles-standards-09-batch-b2-20260705.md) |
| A1 Nginx 安全头 | [records/losangeles-standards-09-a1-nginx-security-headers-20260705.md](records/losangeles-standards-09-a1-nginx-security-headers-20260705.md) |
| B3 fstab UUID | [records/losangeles-standards-09-b3-fstab-uuid-20260706.md](records/losangeles-standards-09-b3-fstab-uuid-20260706.md) |
| C0 容器数据审计 | [records/losangeles-standards-09-c0-container-data-audit-20260705.md](records/losangeles-standards-09-c0-container-data-audit-20260705.md) |
| C1 Redis 加固 | [records/losangeles-standards-09-c1-redis-hardening-20260705.md](records/losangeles-standards-09-c1-redis-hardening-20260705.md) |
| C1 Redis 认证跟进 | [records/losangeles-standards-09-c1-redis-auth-followup-20260705.md](records/losangeles-standards-09-c1-redis-auth-followup-20260705.md) |
| C1 Redis 策略复核 | [records/losangeles-standards-09-c1-redis-policy-readonly-audit-20260706.md](records/losangeles-standards-09-c1-redis-policy-readonly-audit-20260706.md) |
| C1b ACL 兼容性 | [records/losangeles-standards-09-c1b-redis-acl-compatibility-analysis-20260706.md](records/losangeles-standards-09-c1b-redis-acl-compatibility-analysis-20260706.md) |
| C1c ACL 阶段 1 | [records/losangeles-standards-09-c1c-redis-acl-stage1-implementation-20260706.md](records/losangeles-standards-09-c1c-redis-acl-stage1-implementation-20260706.md) |
| C1d ACL 持久化 | [records/losangeles-standards-09-c1d-redis-acl-persistence-20260706.md](records/losangeles-standards-09-c1d-redis-acl-persistence-20260706.md) |
| C1e ACL 备份覆盖 | [records/losangeles-standards-09-c1e-redis-acl-backup-coverage-20260706.md](records/losangeles-standards-09-c1e-redis-acl-backup-coverage-20260706.md) |
| C1f R2 同步复核 | [records/losangeles-standards-09-c1f-r2-backup-sync-verification-20260706.md](records/losangeles-standards-09-c1f-r2-backup-sync-verification-20260706.md) |
| C1g R2 隔离恢复 | [records/losangeles-standards-09-c1g-r2-isolated-restore-drill-20260706.md](records/losangeles-standards-09-c1g-r2-isolated-restore-drill-20260706.md) |
| C1h PostgreSQL 隔离恢复 | [records/losangeles-standards-09-c1h-postgres-isolated-restore-drill-20260706.md](records/losangeles-standards-09-c1h-postgres-isolated-restore-drill-20260706.md) |
| C2 PostgreSQL 权限计划 | [records/losangeles-standards-09-c2-postgres-permission-plan-20260705.md](records/losangeles-standards-09-c2-postgres-permission-plan-20260705.md) |
| C2a sub2api 应用角色 | [records/losangeles-standards-09-c2a-sub2api-postgres-app-role-20260705.md](records/losangeles-standards-09-c2a-sub2api-postgres-app-role-20260705.md) |
| C2b sub2api 低权限切换尝试 | [records/losangeles-standards-09-c2b-sub2api-low-privilege-switch-attempt-20260705.md](records/losangeles-standards-09-c2b-sub2api-low-privilege-switch-attempt-20260705.md) |
| C2c Account Vault 应用角色 | [records/losangeles-standards-09-c2c-account-vault-postgres-app-role-20260705.md](records/losangeles-standards-09-c2c-account-vault-postgres-app-role-20260705.md) |
| C2d Account Vault 低权限切换 | [records/losangeles-standards-09-c2d-account-vault-switch-low-privilege-db-20260705.md](records/losangeles-standards-09-c2d-account-vault-switch-low-privilege-db-20260705.md) |
| C2e PostgreSQL 角色复核 | [records/losangeles-standards-09-c2e-postgres-role-readonly-audit-20260705.md](records/losangeles-standards-09-c2e-postgres-role-readonly-audit-20260705.md) |
| C2f sub2api migration/runtime | [records/losangeles-standards-09-c2f-sub2api-migration-runtime-analysis-20260706.md](records/losangeles-standards-09-c2f-sub2api-migration-runtime-analysis-20260706.md) |
| C2g sub2api migration 能力 | [records/losangeles-standards-09-c2g-sub2api-migration-capability-analysis-20260706.md](records/losangeles-standards-09-c2g-sub2api-migration-capability-analysis-20260706.md) |
| C3a 业务资源限制 | [records/losangeles-standards-09-c3a-business-resource-limits-20260705.md](records/losangeles-standards-09-c3a-business-resource-limits-20260705.md) |
| C3b 监控资源限制 | [records/losangeles-standards-09-c3b-observability-resource-limits-20260705.md](records/losangeles-standards-09-c3b-observability-resource-limits-20260705.md) |
| C3c 运行态资源限制审计 | [records/losangeles-standards-09-c3c-container-runtime-resource-limit-audit-20260706.md](records/losangeles-standards-09-c3c-container-runtime-resource-limit-audit-20260706.md) |
| C4 容器日志限制 | [records/losangeles-standards-09-c4-container-logging-limits-20260705.md](records/losangeles-standards-09-c4-container-logging-limits-20260705.md) |
| C5 镜像 digest | [records/losangeles-standards-09-c5-image-digest-pinning-20260705.md](records/losangeles-standards-09-c5-image-digest-pinning-20260705.md) |
| C6a no-new-privileges | [records/losangeles-standards-09-c6a-no-new-privileges-20260705.md](records/losangeles-standards-09-c6a-no-new-privileges-20260705.md) |
| C6b cap-drop | [records/losangeles-standards-09-c6b-cap-drop-monitoring-helpers-20260705.md](records/losangeles-standards-09-c6b-cap-drop-monitoring-helpers-20260705.md) |
| C7 PostgreSQL exporter 兼容性 | [records/losangeles-standards-09-c7-postgres-exporter-pg18-compatibility-20260706.md](records/losangeles-standards-09-c7-postgres-exporter-pg18-compatibility-20260706.md) |
| D2 云控制面 | [records/losangeles-standards-09-d2-cloud-control-plane-governance-20260705.md](records/losangeles-standards-09-d2-cloud-control-plane-governance-20260705.md) |
| G1 受控 Compose | [records/losangeles-standards-09-g1-service-compose-controlled-copies-20260705.md](records/losangeles-standards-09-g1-service-compose-controlled-copies-20260705.md) |
| R1 备份恢复 | [records/losangeles-standards-09-r1-backup-restore-drill-20260705.md](records/losangeles-standards-09-r1-backup-restore-drill-20260705.md) |

## 通用排障原则

1. **先止血再查根因**：回滚/重启/切流量
2. **只读优先**：先收集证据，再提变更方案
3. **一次一个变更**：执行后立即验证
4. **记录一切**：时间线、命令、输出，用于复盘

## 复盘流程

1. 故障结束后 24 小时内填写 `postmortem-template.md`
2. 识别"有监控就能更早发现"的缺口
3. 更新对应 playbook（如发现新排查路径）；符合录入标准的坑点提炼进 [gotchas.md](gotchas.md)
4. git 提交复盘记录
