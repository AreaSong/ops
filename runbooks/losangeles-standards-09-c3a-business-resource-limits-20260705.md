# LosAngeles standards/09 C3a：业务容器资源限制基线

更新时间：2026-07-05  
服务器：LosAngeles  
范围：`sub2api`、`account-vault`、`resume-jadeai`  
风险级别：中；已修改 compose 文件并用 `docker update` 对运行中容器即时应用资源限制，未重启业务容器

## 1. 目标

为主要业务容器增加保守的资源边界，避免单个服务异常时吃满整机 CPU / 内存 / 进程数。

本批次只处理业务服务，不处理监控栈。监控栈将放到后续批次单独收敛。

## 2. 本批次完成项

### 2.1 Compose 基线

已修改：

- `/opt/services/sub2api/compose.yml`
- `/opt/services/account-vault/compose.yml`
- `/opt/services/resume-jadeai/compose.yml`

为业务服务增加：

- `mem_limit`
- `memswap_limit`
- `cpus`
- `pids_limit`

### 2.2 当前运行容器即时生效

已通过 `docker update` 对当前运行容器应用相同限制，避免仅修改 compose 但运行态不生效。

当前限制：

| 容器 | mem_limit | memswap_limit | cpus | pids_limit |
| --- | ---: | ---: | ---: | ---: |
| `sub2api` | 512m | 768m | 1.00 | 512 |
| `sub2api-postgres` | 768m | 1024m | 1.00 | 512 |
| `sub2api-redis` | 640m | 768m | 0.50 | 256 |
| `account-vault-web-1` | 384m | 512m | 0.50 | 256 |
| `account-vault-postgres-1` | 512m | 768m | 0.75 | 512 |
| `resume-jadeai-app-1` | 1024m | 1280m | 1.00 | 768 |

说明：

- Redis `maxmemory` 已在 C1 设置为 512m，因此容器限制设置为 640m，给 Redis 进程和 AOF/运行开销保留余量。
- `resume-jadeai` 可能涉及 Chromium/渲染场景，因此给 1g 内存和较高 pids 上限。
- 这些限制是防失控边界，不是容量规划终值；后续应结合 Prometheus 曲线继续调优。

## 3. 验证结果

已验证：

- 三个 compose 文件均通过 `docker compose config --quiet`。
- 运行中容器 `HostConfig` 已显示目标 memory / swap / CPU / pids 限制。
- `sub2api`、`sub2api-postgres`、`sub2api-redis` 状态正常。
- `account-vault-web-1`、`account-vault-postgres-1` 状态正常。
- `resume-jadeai-app-1` 状态正常。
- `account-vault` 本机 HTTP 探测返回 200。

验证输出留存在：

- `/tmp/losangeles-09-c3a-business-resource-limits-<timestamp>/`

## 4. 回滚方式

如需回滚 compose：

```bash
sudo cp /root/ops-change-backups/standards09-c3a-business-resource-limits-<timestamp>/opt/services/sub2api/compose.yml.before /opt/services/sub2api/compose.yml
sudo cp /root/ops-change-backups/standards09-c3a-business-resource-limits-<timestamp>/opt/services/account-vault/compose.yml.before /opt/services/account-vault/compose.yml
sudo cp /root/ops-change-backups/standards09-c3a-business-resource-limits-<timestamp>/opt/services/resume-jadeai/compose.yml.before /opt/services/resume-jadeai/compose.yml
```

如需临时放宽运行中容器限制，可使用：

```bash
sudo docker update --memory 0 --memory-swap -1 --cpus 0 --pids-limit -1 <container>
```

实际回滚前建议先确认是哪个容器触发 OOM、健康检查失败或性能异常，再局部放宽，不要全量回滚。

## 5. 后续建议

下一步建议进入 C3b：监控栈资源限制。

待处理容器包括：

- Prometheus
- Grafana
- Loki
- Promtail
- Alertmanager
- Node Exporter
- Blackbox Exporter
- Postgres / Redis exporters

状态：完成。
