# 脚本目录

## 工具与发布脚本

| 脚本 | 用途 |
|------|------|
| generate-ansible-inventory.py | 从 inventory/servers.yaml 生成 Ansible inventory |
| deploy/account-vault-release.sh | 按 GHCR RepoDigest、CI 发布证据、精确备份集和显式批准发布或回滚 Account Vault |
| deploy/account-vault-release-state.sh | 原子记录发布指标、镜像状态和按 digest 归档的当前/上一发布证据 |
| deploy/account-vault-attestation-verify.sh | fail-closed 验证 Account Vault cosign keyless OCI provenance、签名工作流、Git SHA 和来源分支 |
| deploy/account-vault-role-permissions.sh | 分离执行或只读核验 Account Vault runtime 角色的最小权限契约 |
| deploy/update-control.py | 以严格 TTL、幂等、expected-before、服务锁和追加审计执行固定服务适配器；请求不能传命令、镜像或 Compose 路径 |
| tests/validate_observability_configs.sh | 使用生产同版本镜像验证 Prometheus、Loki、Promtail、Blackbox 和 Alertmanager 配置 |
| tests/validate_agent_governance.py | 校验 Agent 导航体系：五处路由表一致、路由引用存在、runbooks 分层、全库 markdown 无死链 |

Account Vault 操作必须先阅读 [发布与回滚 Runbook](../runbooks/records/losangeles-account-vault-release.md)。

通用在线更新控制面必须先阅读 [在线更新控制面 Runbook](../runbooks/playbooks/online-update-control-plane.md)。AreaForge 已部署；Sub2API `v0.1.168` 已完成并退休一次性目标，适配器保留供下一固定版本重新审批启用。自动更新保持关闭。

## 备份脚本

`backup/` 已包含 PostgreSQL、Redis、volume、配置、完整 manifest、R2 同步/验证和隔离恢复脚本。权威说明见 [backup/README.md](backup/README.md) 与 [备份集 Runbook](../runbooks/playbooks/backup-set-integrity.md)。
