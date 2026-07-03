# Observability

LosAngeles single-host observability stack.

## Public access

Only Grafana is exposed through Nginx and Cloudflare:

- `https://monitor.areasong.top/` -> Nginx 443 -> `127.0.0.1:3000`

Prometheus, Alertmanager, Loki, Node Exporter, Blackbox Exporter, Postgres Exporter, and Redis Exporter are not exposed publicly.

## Local ports

- Grafana: `127.0.0.1:3000`
- Prometheus: `127.0.0.1:9090`
- Alertmanager: `127.0.0.1:9093`
- Loki: `127.0.0.1:3100`

Node Exporter, Blackbox Exporter, Postgres Exporter, and Redis Exporter are reachable only inside Docker networks.

## Components

- Prometheus for metrics and alert rules
- Grafana for dashboards
- Alertmanager with QQ SMTP email receiver
- Loki for logs
- Promtail for `/var/log/nginx/*.log`, `/var/log/backup/*.log`, and `/var/log/syslog`
- Node Exporter with textfile collector
- Blackbox Exporter for HTTPS and TLS checks
- Postgres Exporter for sub2api and account-vault PostgreSQL metrics
- Redis Exporter for sub2api Redis metrics

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


## Datastore exporter secrets

Root-only env files live outside Git:

- `/etc/observability/postgres-exporter-sub2api.env`
- `/etc/observability/postgres-exporter-account-vault.env`
- `/etc/observability/redis-exporter-sub2api.env`

The exporter containers join both the observability network and the relevant service network. They do not publish ports to the host or public internet.
