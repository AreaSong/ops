# LosAngeles /root 历史目录归档记录

日期：2026-07-03 16:52 BST
服务器：LosAngeles
类型：历史目录加强审计与非破坏性归档

## 1. 结论

已对 `/root/JadeAI` 和 `/root/sorryiosSearch` 完成加强审计与归档。

本次没有删除原目录，也没有重启生产服务。

结果：

- `/root/JadeAI`：未发现 Docker runtime、进程 cwd/exe/fd、Nginx、systemd、cron 运行时引用。
- `/root/sorryiosSearch`：未发现 Docker runtime mount 或进程 cwd/exe/fd 引用，但仍被 `/opt/services/account-vault/compose.yml` 作为 build context 和 env_file 引用。
- 因此 `/root/JadeAI` 可列入删除候选；`/root/sorryiosSearch` 暂不能删除，需先迁移 `account-vault` 的 build context 和 env_file。

## 2. 归档信息

- 审计文件：`/var/backups/ops/manual/root-history-archive-20260703-165244/root-history-runtime-audit-20260703-165244.txt`
- 归档文件：`/var/backups/ops/manual/root-history-archive-20260703-165244/root-history-dirs-20260703-165244.tar.gz`
- SHA256 文件：`/var/backups/ops/manual/root-history-archive-20260703-165244/root-history-dirs-20260703-165244.tar.gz.sha256`
- 归档完整性：`tar -tzf` 已通过
- R2 同步：已通过 `/opt/ops/scripts/backup/sync-r2.sh` 同步到 Cloudflare R2；第一次上传返回 501 后 rclone 自动重试，第二次成功。

## 3. 审计范围

审计覆盖：

- Docker 容器 labels、working_dir、config_files、bind mounts
- 进程 cwd、exe、fd 引用
- `/etc/nginx`、`/etc/systemd`、cron、`/opt/services`、`/opt/ops` 中的安全路径引用扫描
- 目录存在性、大小和候选文件名

未读取或打印：

- `.env` 文件内容
- 私钥
- 数据库内容
- Redis key/value

## 4. 关键发现

`/opt/services/account-vault/compose.yml` 当前仍包含：

```yaml
build: /root/sorryiosSearch
env_file:
  - /root/sorryiosSearch/.env
```

这意味着当前运行中的容器可以继续运行，但将来如果执行 `docker compose up --build`、重建 web 容器或恢复服务，仍依赖 `/root/sorryiosSearch`。

## 5. 推荐下一步

先规范化 `account-vault`：

1. 将 `/root/sorryiosSearch` 复制到 `/opt/services/account-vault/app`，排除 `.env` 和 `.git`。
2. 将 `/root/sorryiosSearch/.env` 移到 root-only 路径，例如 `/etc/account-vault/account-vault.env`。
3. 修改 `/opt/services/account-vault/compose.yml`：
   - `build` 改为 `/opt/services/account-vault/app`
   - `env_file` 改为 `/etc/account-vault/account-vault.env`
4. 先执行 `docker compose config --no-interpolate` 和 `docker compose build web`。
5. 做 account-vault 数据库备份。
6. 短暂停机重建 `account-vault-web-1`，验证 `127.0.0.1:8392` 和 Nginx 入口。
7. 稳定后再决定是否删除 `/root/sorryiosSearch`。

`/root/JadeAI` 若确认只是历史源码目录，可以在下一轮归档确认后删除。
