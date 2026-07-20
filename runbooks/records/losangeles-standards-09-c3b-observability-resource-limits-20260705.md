# LosAngeles standards/09 C3b：监控栈容器资源限制基线

更新时间：2026-07-05  
服务器：LosAngeles  
范围：Prometheus、Grafana、Loki、Promtail、Alertmanager、exporters  
风险级别：中；已修改 `/opt/ops/observability/docker-compose.yml` 并用 `docker update` 对运行中容器即时应用资源限制，未重启监控容器

## 1. 目标

为监控栈容器增加保守资源边界，避免监控组件异常时挤占业务服务资源。

## 2. 本批次完成项

### 2.1 Compose 基线

已修改：

- `/opt/ops/observability/docker-compose.yml`

为监控服务增加：

- `mem_limit`
- `memswap_limit`
- `cpus`
- `pids_limit`

### 2.2 当前运行容器即时生效

已通过 `docker update` 对当前运行容器应用相同限制。

当前限制：

| 容器 | mem_limit | memswap_limit | cpus | pids_limit |
| --- | ---: | ---: | ---: | ---: |
| `prometheus` | 512m | 768m | 0.75 | 256 |
| `grafana` | 384m | 512m | 0.50 | 256 |
| `loki` | 512m | 768m | 0.75 | 256 |
| `promtail` | 192m | 256m | 0.25 | 128 |
| `alertmanager` | 128m | 192m | 0.25 | 128 |
| `node-exporter` | 128m | 192m | 0.25 | 128 |
| `blackbox-exporter` | 128m | 192m | 0.25 | 128 |
| `postgres-exporter-sub2api` | 128m | 192m | 0.25 | 128 |
| `postgres-exporter-account-vault` | 128m | 192m | 0.25 | 128 |
| `redis-exporter-sub2api` | 128m | 192m | 0.25 | 128 |

说明：

- Prometheus / Loki / Grafana 保留较多余量。
- Exporter 类容器按轻量组件设置较小边界。
- 后续应结合 Prometheus 自身资源曲线继续调优。

## 3. 验证结果

已验证：

- `/opt/ops/observability/docker-compose.yml` 通过 `docker compose config --quiet`。
- 运行中容器 `HostConfig` 已显示目标 memory / swap / CPU / pids 限制。
- Prometheus `/-/ready` 正常。
- Alertmanager `/-/ready` 正常。
- Loki `/ready` 正常。初次检查曾短暂返回 503，随后恢复为 200。
- Grafana `/api/health` 正常。
- 监控容器运行状态正常。
- Prometheus active targets 均为 up。

验证输出留存在：

- `/tmp/losangeles-09-c3b-observability-resource-limits-<timestamp>/`
- `/tmp/losangeles-09-c3b-observability-resource-limits-finish-<timestamp>/`

## 4. 回滚方式

如需回滚 compose：

```bash
sudo cp /root/ops-change-backups/standards09-c3b-observability-resource-limits-<timestamp>/docker-compose.yml.before /opt/ops/observability/docker-compose.yml
```

如需临时放宽运行中容器限制，可使用：

```bash
sudo docker update --memory 0 --memory-swap -1 --cpus 0 --pids-limit -1 <container>
```

建议优先局部放宽异常组件，而不是全量回滚。

## 5. 后续建议

下一步建议进入 C4：现有容器日志限制落地与 compose logging 显式化。

说明：Docker daemon 已有默认日志轮转，但旧容器多数未显式继承；后续可在 compose 中加入 `logging` 策略，并在维护窗口逐服务重建。

状态：完成。
