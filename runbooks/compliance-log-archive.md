# LosAngeles 合规日志异地归档

## 目标与边界

每日 `00:35 UTC` 归档前一完整 UTC 日的 auditd、SSH/sudo 登录日志、Nginx
访问/错误日志和每日运维报告。归档器只保存目标日的记录，不把原始
`EXECVE`/`PROCTITLE` 复制到 Loki；归档本身是 root-only 敏感数据。

归档器在读取日志时限制单个源文件最多 1 GiB、所有源合计最多 2 GiB、单行最多
16 MiB，并在上传前校验 Manifest 路径、日期范围、日期唯一性和逐日连续性。R2
回验端点必须是无凭据、无路径、无查询参数的 HTTPS origin。

Cloudflare R2 当前不支持 S3 Object Lock、保留日期或 legal hold，因此 R2 的
生命周期规则不能单独证明 WORM。归档写入通过 Cloudflare Worker 的 R2 binding
完成：主机只有 Worker 的追加式 token 和独立只读回验 token，Worker 使用
`If-None-Match: *` 拒绝覆盖，且没有删除、列举或读取接口。Cloudflare 控制面账号
仍然可以改变 Worker/bucket，故账号权限、审计和生命周期必须单独治理。

官方依据：R2 [S3 API compatibility](https://developers.cloudflare.com/r2/api/s3/api/#behaviors--limitations)
将 Object Lock headers 标为不支持；Worker API 的条件写入见
[conditional operations](https://developers.cloudflare.com/r2/api/workers/workers-api-reference/#conditional-operations)。

## 控制面前置条件

1. 创建独立 bucket，例如 `losangeles-compliance-logs`，不要复用备份 bucket 的
   上传密钥。
2. 在 `services/compliance-archive-ingest/` 中配置 bucket 名称后，用已审阅的
   Wrangler 版本部署 Worker。
3. 使用 `wrangler secret put INGEST_TOKEN` 保存随机高熵 token；不要写入 Git。
4. 建立 Worker URL 和 token 文件 `/etc/ops/compliance-archive.env`，权限
   `root:root 0600`：

```bash
COMPLIANCE_INGEST_URL=https://<worker-host>
COMPLIANCE_INGEST_TOKEN=<worker-secret>
```

5. 创建只读 R2 token，限制到归档 bucket 的 `GetObject` 和必要的 `ListBucket`，
   写入 `/etc/ops/compliance-archive-verify.env`，权限 `root:root 0600`。
6. 对 `payload/` 配置至少 210 天生命周期，`manifests/` 不设置删除规则，以保留
   完整哈希链。生命周期不是防篡改证明；若业务要求云厂商级 WORM，迁移归档到
   支持 Object Lock 的对象存储，并保留 R2 作为异地副本。

## 本地部署

先执行 Ansible check，确认两个凭据文件存在且 root-only；正式启用归档 cron 时显式
传入开关：

```bash
cd /opt/ops/ansible
ansible-playbook observability-host-jobs.yml --check --diff --limit LosAngeles \
  -e compliance_archive_enabled=true
ansible-playbook observability-host-jobs.yml --limit LosAngeles \
  -e compliance_archive_enabled=true
```

部署前后保存 Ansible 输出的 `backup_file` 路径。首次上线必须手工运行一次：

```bash
sudo COMPLIANCE_ARCHIVE_DATE="$(date -u -d yesterday +%F)" \
  /opt/ops/scripts/backup/archive-compliance-logs.sh
```

成功标准：本地归档校验通过、Worker 返回所有对象创建成功、只读 token 能回读最新
归档和全部 manifest、链校验通过，并生成：

```text
/var/lib/node_exporter/textfile_collector/compliance-log-archive.prom
```

## 运行态验证

```bash
sudo /opt/ops/scripts/backup/verify-compliance-log-archive.sh
sudo grep -E 'compliance_log_archive_(configured|enabled|append_only_gateway|chain_manifests)' \
  /var/lib/node_exporter/textfile_collector/compliance-log-archive.prom
sudo curl -fsSG http://127.0.0.1:9090/api/v1/query \
  --data-urlencode 'query=compliance_log_archive_last_success_timestamp'
sudo curl -fsSG http://127.0.0.1:3100/loki/api/v1/query_range \
  --data-urlencode 'query={job="auditd",host="LosAngeles"} |= "ops-audit-pipeline-probe"' \
  --data-urlencode 'since=10m'
```

检查归档内容时使用独立临时目录，不将原始日志复制到聊天、Issue 或普通日志：

```bash
sudo find /var/backups/ops/compliance-logs -maxdepth 5 -type f -printf '%M %u:%g %p\n'
```

## 回滚

1. 先移除 `/etc/cron.d/ops-compliance-log-archive`，不删除已经写入的远端对象。
2. 恢复 Ansible 输出的脚本备份；若某项显示 `none (new file)`，只删除对应新增
   文件，不清理其他备份或日志。
3. 保留失败 staging、Worker 响应和验证日志作为证据。
4. 如果 Worker token 泄露，立即在 Cloudflare 控制面轮换 token；旧归档不删除，
   再用只读 token 验证链的连续性。

## 长期验收

当天只能证明“链路工作”。每月使用独立只读 token 在另一台临时机器回读一个历史
归档；控制面记录 bucket、Worker、token 权限和生命周期审计。达到 180 天后，确认
manifest 链仍连续、payload 仍可回读，并决定是否迁移到支持 Object Lock 的对象存储。
