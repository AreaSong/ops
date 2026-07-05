# LosAngeles standards09 C5 镜像 digest 固定

日期：2026-07-05  
范围：业务与监控 Docker Compose 镜像治理  
目标：减少 `latest` 与可变 tag 导致的不可复现部署风险。

## 变更结论

已完成：

- 去除当前生产 compose 中的 `latest` 镜像引用。
- 将业务依赖镜像、数据库、Redis、监控栈、Exporter 镜像固定到当前运行 digest。
- 本次仅修改 compose 配置，不执行镜像拉取、不升级版本、不重建业务容器。

## 已固定镜像

业务运行配置：

- `/opt/services/sub2api/compose.yml`
  - `weishaw/sub2api@sha256:b12017d69050ba83e2a3dfa1fd342c25720912937aee5043d5793c6cce0a459e`
  - `postgres:18-alpine@sha256:54451ecb8ab38c24c3ec123f2fd501303a3a1856a5c66e98cecf2460d5e1e9d7`
  - `redis:8-alpine@sha256:c5e375abb885e6b2021c0377879e4890bf76f9065b8922ffc113f2b226b9fc17`
- `/opt/services/account-vault/compose.yml`
  - `postgres:15-alpine@sha256:cd17e2ac98240fce1541ad2a803b34009b4eea5aec8a832363cdc7eca62e722e`
- `/opt/services/resume-jadeai/compose.yml`
  - `twwch/jadeai@sha256:7c714e104d6110aaafa135a6936af3735398c83c479549b467d22fc9c0ab5917`

监控运行配置：

- `/opt/ops/observability/docker-compose.yml`
- `/opt/ops/observability/promtail/docker-compose.yml`

监控栈镜像已按当前运行 digest 固定，包括 Prometheus、Alertmanager、Grafana、Loki、Promtail、Node Exporter、Blackbox Exporter、Postgres Exporter、Redis Exporter。

## 例外项

- `account-vault-web-1` 使用本地 `build: /opt/services/account-vault/app` 生成的本地镜像 `account-vault-web`，不是远端 registry 镜像；本次不做远端 digest 固定。
- `/opt/services/*/compose.yml` 是服务器运行配置，当前不在 `/opt/ops` Git 跟踪范围内；本次 Git 提交记录运维仓库内监控栈配置与本 runbook，业务 compose 的实际变更已在服务器本机落地。

## 验证结果

已执行：

- 所有相关 compose 文件 `docker compose config` 通过。
- 生产 compose 中已无 `image: .*:latest`。
- 本次未重启容器；现有运行容器状态保持正常。
- 本机入口验证：
  - `sub2api /health`：通过。
  - `account-vault`：返回 HTTP 200。
  - `resume-jadeai`：返回 HTTP 307，属于应用重定向响应。
  - Prometheus / Alertmanager / Loki / Grafana ready：通过。

## 后续建议

- 后续镜像升级单独走变更流程：先在测试或维护窗口拉取新镜像，记录新 digest，再更新 compose。
- 建议后续把 `/opt/services/*/compose.yml` 纳入 `/opt/ops` 的受控配置或以清单方式同步，减少运行配置只存在服务器本机的问题。
