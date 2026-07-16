# AreaForge 隔离恢复演练

## 目标

验证 AreaForge 的 PostgreSQL、生产配置、uploads 和 ops-state 备份能够从本地或 R2 拉回，并在不连接生产网络、不暴露端口、不覆盖生产数据的条件下完成恢复。

## 安全边界

- R2 恢复必须指定带 SHA-256 sidecar 的完整 backup-set manifest；本地旧恢复点才允许显式指定四个产物和镜像。禁止分别选择各目录的“最新文件”。
- R2 恢复必须通过 `R2_VERIFY_ENV` 使用独立只读凭据；脚本会拒绝上传配置路径或同一底层文件，部署前仍需在 Cloudflare 控制台核验该 token 只有读取权限。SHA-256 证明对象与 manifest 一致，但对象锁或外部签名完成前不代表存储端不可篡改。
- 临时 PostgreSQL 必须使用 manifest 记录的镜像 ID；可变 tag 只有仍解析到同一 ID 时才可使用。容器使用唯一临时 Docker volume、`--network none` 和未发布端口。
- 所有解包操作位于 root-only 临时目录。配置归档只提取两个必需文件；uploads 和 ops-state 只允许普通文件与目录，并拒绝链接、设备文件、重复成员、绝对路径和 `..` 穿越。
- 不打印 SQL、业务数据、环境文件内容、数据库密码或 R2 凭据。
- 默认在结束时删除临时容器和目录。

## 执行

manifest 示例：

```bash
sudo /opt/ops/scripts/backup/restore-areaforge-isolated.sh \
  --source r2 \
  --manifest manifests/backup-set-YYYYMMDD-HHMMSS.json \
  --compare-production
```

本地旧恢复点兼容示例：

```bash
sudo /opt/ops/scripts/backup/restore-areaforge-isolated.sh \
  --source local \
  --postgres-artifact postgres/areaforge-postgres-YYYYMMDD-HHMMSS.sql.gz \
  --configs-artifact configs/configs-YYYYMMDD-HHMMSS.tar.gz \
  --uploads-artifact volumes/areaforge-uploads-YYYYMMDD-HHMMSS.tar.gz \
  --ops-state-artifact volumes/areaforge-ops-state-YYYYMMDD-HHMMSS.tar.gz \
  --postgres-image postgres:16-alpine \
  --compare-production
```

R2 示例执行前需要显式传入只读凭据文件：

```bash
sudo env R2_VERIFY_ENV=/etc/ops/r2-verify.env \
  /opt/ops/scripts/backup/restore-areaforge-isolated.sh \
  --source r2 \
  --manifest manifests/backup-set-YYYYMMDD-HHMMSS.json
```

生产比对默认开启。`--no-compare-production` 仅用于脱离生产容器的离线导入检查，且不会发布恢复成功指标。只有在需要人工检查临时解包结果时才使用 `--keep-workdir`；检查结束后必须手工删除脚本输出的目录。

## 验收

- manifest sidecar 和所选四个产物的 SHA-256 全部通过。
- 四个产物可以读取并通过 gzip/tar 完整性检查，安全提取器未发现链接、特殊文件或路径穿越。
- tar 的成员数和展开字节数与 manifest 一致，工作目录与 Docker 根目录都通过容量预检。
- 配置归档包含 `docker-compose.prod.yml` 和 `.env.production`。
- 临时数据库完成导入且可以执行 `select 1`。
- 用户 Schema 和表名称清单与生产一致，并记录恢复库与生产库大小；恢复库不得小于生产库的安全比例下限。
- 恢复前已按 SQL 解压大小和生产库大小完成 Docker 存储空间预检。
- 脚本退出后没有临时容器残留。
- `areaforge_restore_drill_last_success_timestamp` 已写入 Node Exporter textfile collector。

## 失败处理

- 不要切换生产服务或将失败的恢复目录复制回生产路径。
- 保留 `/var/log/backup/areaforge-restore-*.log` 作为证据。
- 若 R2 与本机 SHA-256 不一致，立即停止同步和恢复，按备份完整性事件处理。
- 若 Schema 或表名称不一致，先确认生产是否在备份后执行过迁移，再选择新的同批恢复点重试。
