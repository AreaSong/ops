# LosAngeles 内存 limit 收敛（批次 2.1）

日期：2026-07-21  
级别：L1  
主机：LosAngeles  
批准：用户明确「可以，开始吧，你先来完成 AB」  
提交：`cbc2395`  
备份目录：`/root/ops-change-backups/mem-limit-tighten-20260721105113`

## 变更原因

容器 mem_limit 合计纸面超卖（~7Gi+ vs 主机 3.8Gi），Swap 常驻约 55%。实际 RSS 远低于上限，下调空闲容器 limit。

## 变更表

| 容器 | 前 | 后 | 生效方式 |
| --- | --- | --- | --- |
| sub2api-redis | 640m / 768m | 128m / 192m | compose recreate |
| resume-jadeai app | 1024m / 1280m | 384m / 512m | compose recreate |
| account-vault postgres | 512m / 768m | 256m / 384m | compose recreate（需 `ACCOUNT_VAULT_IMAGE` 从运行中 web 注入） |
| areaforge postgres | 512m / 768m | 256m / 384m | compose 已同步；运行态用 `docker update`（缺 `.env`，环境在 `.env.production`） |
| node/blackbox/pg×2/redis exporters | 128m / 192m | 64m / 96m | compose recreate |

## 验收（2026-07-21）

- 九个目标容器 `Memory` 与期望字节一致，均为 `running`，`OOMKilled=false`（`ALL-OK`）
- health：`cpa`/`vault` 200；`resume`/`forge` 307；Grafana `/api/health` ok
- Swap：变更前约 283Mi used → 变更后约 **227Mi used**（下降约 56Mi）

## 回滚

```bash
BK=/root/ops-change-backups/mem-limit-tighten-20260721105113
sudo cp -a $BK/sub2api.compose.before /opt/services/sub2api/compose.yml
sudo cp -a $BK/resume.compose.before /opt/services/resume-jadeai/compose.yml
sudo cp -a $BK/account-vault.compose.before /opt/services/account-vault/compose.yml
sudo cp -a $BK/areaforge.compose.before /opt/areaforge/docker-compose.prod.yml
# 再对对应服务 --force-recreate；或 git revert cbc2395 后 pull
```

## 备注

- account-vault 的 `ACCOUNT_VAULT_IMAGE` 不在 `/etc/account-vault/account-vault.env`，compose 全文件插值时必须额外导出该变量。
- areaforge 生产环境文件为 `/opt/areaforge/.env.production`，后续 recreate 应 `--env-file .env.production`。
