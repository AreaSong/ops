# 01 命名与台账规范

> 本目录为运维规范索引。完整规范按域拆分，新建/变更前读取对应文档。

## 文档索引

| 编号 | 文档 | 内容 |
|------|------|------|
| 00 | server-checklist.md | 服务器企业化验收清单(摸底/验收用总标尺) |
| 01 | naming-inventory.md | 命名规范、台账维护 |
| 02 | os-baseline.md | 系统基线（SSH、防火墙、时区、日志） |
| 03 | security-access.md | 账号权限、密钥、云子账号 |
| 04 | deployment.md | 服务部署规范 |
| 05 | change-management.md | 变更流程与回滚 |
| 06 | backup-dr.md | 备份与恢复演练 |
| 07 | patching.md | 补丁与漏洞管理 |
| 08 | observability.md | 监控、告警、日志 |
| 09 | server-ops-handbook.md | 服务器全生命周期运维大全（单文件自包含总纲，31 章 + 4 附录） |

## 仓库禁止入库的内容

- 密码、AccessKey、token、证书私钥等一切凭证
- 备份数据、日志文件、数据库导出文件
- 凭证引用方式：环境变量或 `/opt/ops/secrets.env`（已在 .gitignore 排除）
- 提交前自查：`git diff --cached` 出现形似凭证的字符串，停止提交

## 服务器侧同步

每台服务器将本仓库克隆到 `/opt/ops/`，与 Git 仓库保持同步：

```bash
cd /opt/ops && git pull --ff-only
```

---

修订记录：

- 2026-07-02 初版，拆分为八大域规范体系
