# LosAngeles 内存 limit 收敛（批次 2.1）

日期：2026-07-21  
级别：L1  
主机：LosAngeles  
批准：用户明确「可以，开始吧，你先来完成 AB」

## 变更原因

容器 mem_limit 合计纸面超卖（~7Gi+ vs 主机 3.8Gi），Swap 常驻约 55%。实际 RSS 远低于上限，下调空闲容器 limit。

## 变更表

| 容器 | 前 | 后 |
| --- | --- | --- |
| sub2api-redis | 640m / swap 768m | 128m / 192m |
| resume-jadeai app | 1024m / 1280m | 384m / 512m |
| account-vault postgres | 512m / 768m | 256m / 384m |
| areaforge postgres | 512m / 768m | 256m / 384m |
| node/blackbox/pg×2/redis exporters | 128m / 192m | 64m / 96m |

## 回滚

恢复对应 compose 中旧 mem_limit，再 `--force-recreate --no-deps` 相关服务。

## 验收

- `docker inspect` Memory 与新值一致
- 入口 health 200；无新增 OOM
- SwapUsed 有下降或至少不再升高
