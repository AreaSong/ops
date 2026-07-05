# LosAngeles standards/09 C3c：容器资源限制运行态复核

更新时间：2026-07-06  
服务器：LosAngeles  
范围：业务容器、数据库/Redis 容器、监控栈容器  
风险级别：只读复核；未修改 compose，未执行 `docker update`，未重启容器

## 1. 结论

本次只读复核确认：当前 16 个运行中容器均已有明确的 `HostConfig.Memory` 内存上限，并且主要 compose 文件均已写入资源限制。

已覆盖 compose 文件：

- `/opt/services/account-vault/compose.yml`
- `/opt/services/resume-jadeai/compose.yml`
- `/opt/services/sub2api/compose.yml`
- `/opt/ops/observability/docker-compose.yml`

运行态复核结果：

- 无 `Memory=0` 的运行容器。
- 业务容器资源限制已由 C3a 落地。
- 监控栈资源限制已由 C3b 落地。
- 本次 C3c 仅做运行态复核和文档口径同步。

## 2. 运行态资源限制摘要

| 容器 | mem_limit | memswap_limit | cpu_limit | 当前内存快照 |
| --- | ---: | ---: | ---: | ---: |
| `resume-jadeai-app-1` | 1.0GiB | 1.2GiB | 1.00 | 92.59MiB / 1GiB |
| `sub2api` | 512MiB | 768MiB | 1.00 | 63.73MiB / 512MiB |
| `sub2api-postgres` | 768MiB | 1.0GiB | 1.00 | 164.4MiB / 768MiB |
| `sub2api-redis` | 640MiB | 768MiB | 0.50 | 5.551MiB / 640MiB |
| `account-vault-web-1` | 384MiB | 512MiB | 0.50 | 38.03MiB / 384MiB |
| `account-vault-postgres-1` | 512MiB | 768MiB | 0.75 | 24.71MiB / 512MiB |
| `prometheus` | 512MiB | 768MiB | 0.75 | 67.82MiB / 512MiB |
| `grafana` | 384MiB | 512MiB | 0.50 | 51.58MiB / 384MiB |
| `loki` | 512MiB | 768MiB | 0.75 | 82.19MiB / 512MiB |
| `promtail` | 192MiB | 256MiB | 0.25 | 27.97MiB / 192MiB |
| `alertmanager` | 128MiB | 192MiB | 0.25 | 16.61MiB / 128MiB |
| `node-exporter` | 128MiB | 192MiB | 0.25 | 9.367MiB / 128MiB |
| `blackbox-exporter` | 128MiB | 192MiB | 0.25 | 19.88MiB / 128MiB |
| `postgres-exporter-sub2api` | 128MiB | 192MiB | 0.25 | 8.98MiB / 128MiB |
| `postgres-exporter-account-vault` | 128MiB | 192MiB | 0.25 | 9.703MiB / 128MiB |
| `redis-exporter-sub2api` | 128MiB | 192MiB | 0.25 | 10.49MiB / 128MiB |

## 3. 当前观察

当前内存占用最高的容器：

- `sub2api-postgres`：164.4MiB / 768MiB
- `resume-jadeai-app-1`：92.59MiB / 1GiB
- `loki`：82.19MiB / 512MiB
- `prometheus`：67.82MiB / 512MiB
- `sub2api`：63.73MiB / 512MiB

结论：

- 当前资源限制整体较保守，未见接近上限的容器。
- `sub2api-postgres` 是当前内存占用最高的容器，但仍有明显余量。
- 后续不需要继续将容器资源边界列为未完成项。

## 4. 后续建议

- 持续通过 Prometheus / Grafana 观察容器内存使用趋势。
- 如果出现 OOM、重启或接近上限告警，优先按单个服务调高限制，不做全量放宽。
- 后续新增服务时，继续在 compose 中显式写入 `mem_limit`、`memswap_limit`、`cpus` 和 `pids_limit`。

## 5. 验证留痕

只读采集输出：

- `/tmp/codex-container-limits-audit.out`

状态：完成。
