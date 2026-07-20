# LosAngeles JadeAI fingerprint 身份错位事件记录

时间：2026-07-03
服务：`resume-jadeai`
数据卷：`jadeai-data`

## 1. 现象

JadeAI「我的简历」页面先只显示 1 条示例简历，后续一度显示 0 条，用户误以为简历数据被清理或迁移丢失。

## 2. 结论

数据没有被删除。根因是 JadeAI 使用浏览器本地 `jade_fingerprint` 作为匿名用户身份，并通过请求头 `x-fingerprint` 绑定后端 `users` 表。浏览器指纹变化、缓存变化、不同浏览器/Profile 或重新生成 fingerprint 时，会被识别为新的用户，前端只能看到该用户名下的数据。

## 3. 证据

运行时配置：

- compose：`/opt/services/resume-jadeai/compose.yml`
- 数据卷：`jadeai-data -> /app/data`
- SQLite：`/var/lib/docker/volumes/jadeai-data/_data/jade.db`
- 历史源码目录：`/root/JadeAI`，不是运行时数据目录

只读核查结果：

- SQLite `PRAGMA integrity_check` 为 `ok`
- 当前库存在 `resumes=7`、`resume_sections=42`、`users=5`
- 早期本机备份中 `resumes=5`，当前库多于备份，说明不是备份恢复覆盖导致的数据丢失

## 4. 处置

已执行两次非内容性归属修正，只更新带 `user_id` 的归属字段，不修改简历正文、章节内容、分享 token、fingerprint、邮箱或原始用户 ID。

第一次将 7 条简历合并到最新 1 条简历用户，页面仍为空，说明目标不是当前浏览器 fingerprint。

第二次将 7 条简历重新归属到最新 0 条简历用户：

- 目标用户哈希：`95f9b287a40f`
- 更新 `resumes`：7 行
- 更新 `interview_sessions`：2 行
- `resume_sections`：42 行保持不变
- API 验证目标身份返回 7 条简历

## 5. 备份与回滚

处置前快照：

- `/var/backups/ops/manual/jadeai-user-merge-20260703-172231/`
- `/var/backups/ops/manual/jadeai-user-retarget-20260703-172951/`

合并后 volume 备份：

- `/var/backups/ops/volumes/jadeai-data-20260703-172347.tar.gz`
- `/var/backups/ops/volumes/jadeai-data-20260703-173025.tar.gz`

每个手工快照目录内包含 `restore-jadeai-db.sh`，可短暂停止 `resume-jadeai` 后回滚 SQLite 文件。

## 6. 后续建议

JadeAI 当前不适合长期依赖浏览器 fingerprint 承载生产数据。建议后续择一处理：

1. 接入稳定登录/管理员账号。
2. 固定一个服务端默认用户作为单用户部署模式。
3. 增加只允许管理员执行的数据归属合并工具。
4. 在运维 runbook 中保留 fingerprint 排查流程，避免误判为数据丢失。
