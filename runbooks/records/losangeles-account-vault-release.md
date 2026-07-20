# LosAngeles Account Vault 发布与回滚

适用服务：`sorryiossearch.areasong.top`
生产脚本：`/opt/ops/scripts/deploy/account-vault-release.sh`
常规窗口：北京时间 22:00-23:00

## 1. 安全模型

- 生产只接受 `ghcr.io/areasong/sorryiossearch@sha256:<digest>`。
- Web 使用低权限 `DATABASE_APP_USER`；migration 使用 PostgreSQL 管理角色。
- 生产镜像必须声明 `USER node`，Compose 同时强制 `user: node`、只读根文件系统、`cap_drop: ALL` 和 `no-new-privileges`。
- CI 阻断 HIGH/CRITICAL，生成 CycloneDX SBOM，并要求 Prisma migration 为 expand-only（只扩展、保持旧镜像兼容）。
- 发布脚本要求 GitHub CI 生成的 `published-release-manifest.json`，将 Git SHA、候选 Image ID、RepoDigest、SBOM、Trivy、migration tree 和 cosign keyless OCI attestation 绑定。
- migration 之后只重建 Web，不重建 PostgreSQL。

## 2. 首次发布前数据库权限

首次启用新链路前必须单独确认数据库权限变更，并验证：

- 管理角色拥有执行 Prisma migration 所需 DDL 权限。
- `account_vault_app` 无 superuser、createdb、createrole 和 schema CREATE。
- `account_vault_app` 不属于任何其他角色，不能修改 Prisma 的 `_prisma_migrations` 元数据。
- 管理角色的 default privileges 会把新表的 SELECT/INSERT/UPDATE/DELETE 和新 sequence 的 USAGE/SELECT/UPDATE 授予 `account_vault_app`。
- 现有表与 sequence 已补齐同样权限。

任何 grant/default-privilege 变更均属于生产数据库写操作，不能在只读审计中执行。

## 3. 发布前门禁

1. Account Vault `main` 的 backend、frontend、migration、secret-scan、image-security 和 publish 全部成功。
2. GHCR package 为 private，生产凭据只有 packages:read。
3. 经批准运行 `ansible/cosign.yml` 安装校验和固定的 cosign 与 jq；`/etc/account-vault/github-read-token` 必须为 `root:root 0600`，仅具备读取私有 package 的权限。发布脚本使用该文件创建一次性 `DOCKER_CONFIG`，从 Registry manifest 的 `config.digest` 核对候选 Image ID，并在成功、失败或中断后删除临时认证目录，不复用 root 的持久 Docker 登录配置。
4. 发布脚本必须成功验证 OCI attestation 的 signer workflow、`refs/heads/main`、Git SHA 和 GitHub-hosted runner；SLSA provenance、CycloneDX SBOM 与 Trivy 扫描 predicate 三类证明缺一不可，并合并生成 root-only 回执。
5. 从同一成功 run 下载 `account-vault-published-release-<git-sha>` artifact，将 JSON 放到 root-only 临时批准路径并设置 `root:root 0600`。
6. `/etc/account-vault/account-vault.env` 为 `root:root 0600`，包含脚本要求的全部变量，且 runtime 用户与管理用户、密码均不同。
7. `/var/backups/ops/manifests/latest-manifest.txt` 指向完整备份集；manifest 中的 Account Vault PostgreSQL artifact 新鲜且校验通过。
8. `/var/lib/ops/backup-set-r2-verify/state` 必须记录同一 manifest 路径和 SHA-256，且验证时间未过期。
9. 分别记录 migration 与角色 GRANT/default privileges 的批准、变更号、批准 digest、Git SHA、备份 manifest 和回滚镜像。

## 4. 发布

```bash
sudo /opt/ops/scripts/deploy/account-vault-release.sh deploy \
  'ghcr.io/areasong/sorryiossearch@sha256:<64-hex>' \
  --evidence /root/approved/account-vault-published-release.json \
  --approve-migration \
  --approve-role-grants \
  --role-grants-change-id AV-ROLE-YYYYMMDD-NNN \
  --change-id AV-YYYYMMDD-NNN
```

脚本按顺序执行：锁、窗口、环境、CI 证据、精确备份集/R2、RepoDigest、Image ID、镜像用户与 revision、Compose 快照、明确批准的角色权限、migration、权限复核、Web 重建、容器 health、本机 `/ready`、`/api/auth/status` 和公网 `/health`。

发布后记录位于 `/var/lib/ops/account-vault-release/`。每次成功状态先完整写入 generation，再原子切换 `current`；证据和 attestation 回执按镜像 digest 归档，并分别生成当前/上一镜像视图。该目录已纳入配置备份。

## 5. 观察与验收

```bash
sudo /opt/ops/scripts/deploy/account-vault-release.sh verify
```

同时确认：

- `docker inspect account-vault-web-1` 的配置镜像为批准 RepoDigest、用户为 `node`、健康为 `healthy`。
- `/ready` 返回 JSON 且 `database=ready`，不是 SPA HTML。
- `/api/auth/status` 可由低权限角色读取业务表。
- Prometheus `account_vault_release_last_success` 为 1，Grafana 发布面板显示正确镜像。
- 30、60、120 分钟内无 5xx、数据库权限、重启或 OOM 异常。

## 6. 镜像与 Compose 回滚

```bash
sudo /opt/ops/scripts/deploy/account-vault-release.sh rollback \
  --approve-rollback \
  --change-id AV-YYYYMMDD-NNN-RB
```

脚本从 `current/previous-image` 和 `current/previous-compose.yml` 回滚，同步切换当前/上一镜像证据与 attestation 回执，验证容器 health、业务表读取和公网健康。自动失败处理也先恢复发布前 Compose，再尝试旧镜像，并保留实际失败候选的指标。

## 7. 数据库边界

镜像回滚不执行 schema downgrade。CI 使用保守 allowlist：只允许新表/枚举、普通索引、同一 migration 新表的约束，以及现有表的可空无约束新列；未知或收缩性语句全部阻断。首次生产发布还必须完成一次“迁移后切回 N-1 镜像”的真实回滚演练。

角色 GRANT、default privileges 和 `_prisma_migrations` 写权限撤销属于 forward-only 权限变更：镜像回滚不会撤销这些权限。它们同时兼容新旧 Web 镜像；若权限 helper 失败，停止发布并先修正权限契约，不通过放宽到管理角色来恢复业务。

若数据库出现损坏或 migration 非事务性部分失败：

1. 立即停止继续发布，不直接覆盖生产数据库。
2. 使用最新 manifest 在隔离 PostgreSQL 中验证恢复。
3. 比较数据与 schema，形成精确恢复方案。
4. 说明数据丢失窗口、停机影响和回滚路径后，再取得单独确认执行生产恢复。
