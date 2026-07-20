# LosAngeles 应用级恢复演练记录

日期：2026-07-04
演练时间：13:53 BST
服务器：LosAngeles
范围：非破坏性应用级恢复验证

## 1. 目标

验证最新备份不仅可以解包或导入，还可以被临时业务容器读取并启动健康响应。

本次演练覆盖：

- resume-jadeai / JadeAI 数据目录
- account-vault PostgreSQL dump 与 web 容器
- sub2api PostgreSQL dump、Redis RDB、数据目录与应用容器

## 2. 安全边界

本次演练严格使用隔离临时资源：

- 仅创建临时 Docker network。
- 仅创建临时容器。
- 仅使用 `/tmp/app-restore-drill-*` 临时目录。
- 不发布宿主机端口。
- 不修改 Nginx。
- 不重启生产容器。
- 不写入生产数据库、Redis 或生产 volume。
- 不打印 `.env`、私钥、数据库内容或任何密钥。
- 演练完成后已删除临时容器、临时网络和临时目录。

## 3. 使用的备份

- account-vault PostgreSQL：`account-vault-postgres-1-20260704-021001.sql.gz`
- sub2api PostgreSQL：`sub2api-postgres-20260704-021001.sql.gz`
- Redis：`redis-20260704-023001.tar.gz`
- JadeAI 数据目录：`jadeai-data-20260704-033001.tar.gz`
- sub2api 数据目录：`sub2api-data-20260704-033001.tar.gz`

## 4. 验证结果

最终演练 ID：

`app-restore-drill-20260704-135307`

结果：

- `PASS jadeai_data_extract`
- `PASS jadeai_home http_code=307`
- `PASS account_vault_postgres_restore`
- `PASS account_vault_health http_code=200`
- `PASS sub2api_postgres_restore`
- `PASS sub2api_data_extract`
- `PASS redis_dump_extract`
- `PASS sub2api_redis_start`
- `PASS sub2api_health http_code=200`
- `result=PASS`

完成时间：

`2026-07-04T13:53:57+01:00`

## 5. 过程备注

第一轮演练中，临时 account-vault PostgreSQL 容器在官方镜像初始化阶段出现短暂 `pg_isready` 可用窗口，脚本过早开始导入 dump，导致恢复管道失败。

随后单独诊断确认同一份 account-vault dump 可完整恢复，说明备份本身没有损坏。演练脚本改为二次确认 PostgreSQL 最终启动完成后，第二轮全量演练通过。

该问题只发生在临时演练容器中，未影响生产容器、生产数据库或生产数据。

## 6. 结论

最新本机备份已通过应用级恢复验证：

- JadeAI 恢复数据可被临时业务容器读取，首页返回重定向响应。
- account-vault PostgreSQL dump 可导入临时数据库，web 容器可连接恢复库并通过 `/health`。
- sub2api PostgreSQL dump、Redis RDB 和数据目录可恢复到临时资源，应用容器可启动并通过 `/health`。

这补齐了此前“只验证到数据导入、RDB 校验和文件解包层面”的缺口。

## 7. 未覆盖项

- 未做跨机器恢复。
- 未切换公网入口或 Nginx 到临时应用。
- 未访问登录后的敏感业务路径。
- 未执行真实用户写入类业务操作。

