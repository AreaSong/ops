# Observability

LosAngeles single-host observability stack.

## Public access

Only Grafana is exposed through Nginx and Cloudflare:

- `https://monitor.areasong.top/` -> Nginx 443 -> `127.0.0.1:3000`

Prometheus, Alertmanager, Loki, Node Exporter, and Blackbox Exporter are not exposed publicly.

## Local ports

- Grafana: `127.0.0.1:3000`
- Prometheus: `127.0.0.1:9090`
- Alertmanager: `127.0.0.1:9093`
- Loki: `127.0.0.1:3100`

Node Exporter and Blackbox Exporter are reachable only inside the Docker network.

## Components

- Prometheus for metrics and alert rules
- Grafana for dashboards
- Alertmanager with local-only receiver
- Loki for logs
- Promtail for `/var/log/nginx/*.log`, `/var/log/backup/*.log`, and `/var/log/syslog`
- Node Exporter with textfile collector
- Blackbox Exporter for HTTPS and TLS checks

## Textfile metrics

Metrics directory:

- `/var/lib/node_exporter/textfile_collector/`

Scripts:

- `observability/scripts/write-backup-metrics.sh`
- `observability/scripts/write-docker-metrics.sh`

Cron:

- Docker container metrics every 5 minutes
- Backup freshness metrics daily after backup jobs

## Operations

Start/update:

```bash
cd /opt/ops/observability
docker compose up -d
```

Stop:

```bash
cd /opt/ops/observability
docker compose down
```

Health checks:

```bash
curl http://127.0.0.1:9090/-/ready
curl http://127.0.0.1:3000/api/health
curl http://127.0.0.1:9093/-/ready
curl http://127.0.0.1:3100/ready
```
