# LosAngeles standards09 G1 业务 Compose 受控副本

日期：2026-07-05  
范围：业务服务 Compose 配置治理  
目标：将 `/opt/services/*/compose.yml` 的当前生产配置纳入 `/opt/ops` Git 留痕，提升恢复、审计、复盘能力。

> 历史边界：本文记录 2026-07-05 的首次受控副本建立过程。当时采用的“先改运行文件、
> 再回填 Git”顺序已废止；当前权威流程以 `services/README.md` 为准，必须先修改、审查并
> 提交受控文件，再经批准同步到运行路径。

## 变更结论

已完成：

- 创建 `/opt/ops/services/` 受控配置目录。
- 同步当前生产运行 Compose 文件：
  - `/opt/services/sub2api/compose.yml` -> `/opt/ops/services/sub2api/compose.yml`
  - `/opt/services/account-vault/compose.yml` -> `/opt/ops/services/account-vault/compose.yml`
  - `/opt/services/resume-jadeai/compose.yml` -> `/opt/ops/services/resume-jadeai/compose.yml`
- 创建 `/opt/ops/services/README.md`，说明运行文件与受控副本的关系。

## 安全检查

已检查：

- 未提交 `.env` 文件。
- 未提交 root-only 密钥文件。
- Compose 文件中的 `PASSWORD`、`SECRET`、`TOKEN`、`KEY` 字段均为环境变量引用或注释说明，不包含明文密钥值。

## 注意事项

- 当前实际运行文件仍在 `/opt/services/<service>/compose.yml`。
- `/opt/ops/services/*/compose.yml` 是 Git 受控副本，不会自动驱动生产服务。
- 本文当时使用的 runtime-first 流程仅保留为历史记录，不得用于后续生产变更。

## 验证结果

已执行：

- 三份业务 Compose 受控副本与运行文件逐字节一致。
- 受控副本 `docker compose config` 均可解析；`sub2api` 验证时使用运行目录 `/opt/services/sub2api/.env` 作为 env 来源，但未提交该文件。
- 未改变运行容器、未重启服务。
