# 脚本目录

## 工具与发布脚本

| 脚本 | 用途 |
|------|------|
| generate-ansible-inventory.py | 从 inventory/servers.yaml 生成 Ansible inventory |
| deploy/account-vault-release.sh | 按 GHCR RepoDigest、CI 发布证据、精确备份集和显式批准发布或回滚 Account Vault |
| deploy/account-vault-release-state.sh | 原子记录发布指标、镜像状态和按 digest 归档的当前/上一发布证据 |
| deploy/account-vault-attestation-verify.sh | fail-closed 验证 Account Vault OCI provenance、签名工作流、Git SHA 和来源分支 |
| deploy/account-vault-role-permissions.sh | 分离执行或只读核验 Account Vault runtime 角色的最小权限契约 |
| tests/validate_observability_configs.sh | 使用生产同版本镜像验证 Prometheus、Loki、Promtail、Blackbox 和 Alertmanager 配置 |

Account Vault 操作必须先阅读 [发布与回滚 Runbook](../runbooks/losangeles-account-vault-release.md)。

## 备份脚本

`backup/` 已包含 PostgreSQL、Redis、volume、配置、完整 manifest、R2 同步/验证和隔离恢复脚本。权威说明见 [backup/README.md](backup/README.md) 与 [备份集 Runbook](../runbooks/backup-set-integrity.md)。
