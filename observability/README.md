# Observability

LosAngeles single-host observability stack.

## Public access

Only Grafana is exposed through Nginx and Cloudflare:

- `https://monitor.areasong.top/` -> Nginx 443 -> `127.0.0.1:3000`

Cloudflare Access protects the public Grafana route. Human access uses the single
approved email OTP policy with a six-hour session. The GitHub external-uptime workflow
uses its own service token only for `/api/health`; the client secret is stored in GitHub
Actions secrets and never in this repository. The internal readiness probe remains
independent of Access and checks Grafana directly on the Docker network.

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
- Alertmanager with QQ SMTP email receiver, grouped routes, and custom email templates
- Loki for logs
- Promtail for Nginx, backup, observability, syslog, Fail2ban, and auditd logs; positions persist under `/var/lib/observability/promtail/`
- Node Exporter with textfile collector
- Blackbox Exporter for HTTPS and TLS checks
- Blackbox app probes for resume-jadeai, account-vault, and sub2api public health checks
- AreaSong Ops integration: direct Web/Runner aggregate metrics on the external `areasong-ops-network`, a Cloudflare Access 302 policy probe, and unified `service="areasong-ops"` labels for Web, Runner and Nginx logs. Production validation completed on 2026-08-10.
- Postgres Exporter for sub2api and account-vault PostgreSQL metrics
- Redis Exporter for sub2api Redis metrics
- Business blackbox probes for public, read-only key paths:
  - `resume-jadeai` public resume homepage
  - `account-vault` login page and auth status API
  - `sub2api` login page and health JSON
- Sanitized runtime asset snapshots for CPU/load/memory/disk, listeners, systemd, containers, security state, domain routing, Compose paths, and configuration drift
- Inventory entries with `lifecycle: planned` remain visible in the sanitized asset snapshot, but missing planned runtime components and unconverged planned configuration are excluded from production completeness and drift gates until their lifecycle is promoted.
- Sanitized warning/error collection for the four business services, forwarded to Loki as `job="business_errors"`
- Daily comparison of the deployed Cloudflare proxy CIDRs with the official IPv4/IPv6 lists
- Optional Alertmanager critical-alert to GitHub Issue synchronization with failure/recovery lifecycle simulation
- Alertmanager config/template reload and atomically replaced SMTP credential inode freshness checks

## Textfile metrics

Metrics directory:

- `/var/lib/node_exporter/textfile_collector/`

Scripts:

- `observability/scripts/write-backup-metrics.sh`
- `observability/scripts/write-docker-metrics.sh` with running, health, restart, OOM, CPU, memory, and PID metrics
- `observability/scripts/write-security-metrics.sh`
- `observability/scripts/write-sub2api-capacity-metrics.sh`
- `observability/scripts/write-daily-ops-audit.sh`
- `observability/scripts/runtime_snapshot.py`
- `observability/scripts/business_error_log.py`
- `observability/scripts/cloudflare_ip_metrics.py`
- `observability/scripts/alertmanager_runtime_input.py`
- `observability/scripts/alertmanager_github_issues.py`
- `observability/scripts/github_external_heartbeat.py` (阶段 7：关闭状态 heartbeat Issue 更新，复用 root-only Issues 凭据)

Cron:

- Docker container metrics every minute
- Backup freshness metrics daily after backup jobs
- Security log and firewall metrics every minute
- sub2api account-pool symptom metrics every minute
- Runtime asset snapshot and configuration drift every minute
- Alertmanager runtime input freshness every minute
- Sanitized business warning/error collection every minute
- Business access-log metrics every minute
- Fail2ban enrichment every five minutes
- Cloudflare Origin Certificate metrics hourly
- Cloudflare official-IP drift daily
- Previous complete UTC day operations audit at `00:20 UTC`
- Docker BuildKit cache older than 14 days every Sunday at `06:40 UTC` (no image or volume pruning)
- Optional critical-alert GitHub Issue sync every five minutes and monthly failure/recovery simulation
- Optional external heartbeat Issue update every five minutes; GitHub Actions independently detects stale state

The normal host jobs are always managed. The three GitHub jobs (critical-alert sync, monthly failure/recovery simulation, and external heartbeat) are enabled by default and require `/var/lib/areasong-ops/credentials/alertmanager-github.env` to be `root:root 0600`; pass `-e alertmanager_github_issues_enabled=false` to remove them temporarily. AreaSong Ops owns the fixed-purpose credential rotation lifecycle. The file contains the enable flag, a minimum-scope Issues token, and repository identity; it is never mounted into containers or committed. The heartbeat writes only a timestamp and fixed marker to one closed Issue; it never writes logs or secrets.

## Daily operations audit

The daily audit aggregates the previous complete UTC day without persisting raw client
addresses, query strings, credentials, cookies, or log lines. It reports:

- host RX/TX bytes and resource peaks
- per-service HTTP status classes, 5xx error ratio, normalized 5xx paths, response bytes, unique-client count, normalized top paths, and P50/P95/P99 latency
- SSH, sudo, Fail2ban, and UFW events
- current systemd, Docker, firewall, active-alert, local backup, and R2 state

Artifacts:

- report: `/var/log/observability/daily-ops-audit-YYYY-MM-DD.md` (`0640`, retained 180 days)
- task log: `/var/log/observability/daily-ops-audit.log` (30 rotations)
- textfile metrics: `/var/lib/node_exporter/textfile_collector/daily-ops-audit.prom`

Manual generation without email:

```bash
sudo /opt/ops/observability/scripts/write-daily-ops-audit.sh --no-email
```

Send the report through the dedicated Alertmanager receiver only after reviewing the
local Markdown output:

```bash
sudo /opt/ops/observability/scripts/write-daily-ops-audit.sh
```

The email submission reuses Alertmanager's root-only SMTP password file. The audit
scripts do not read or copy SMTP credentials. Same-day reruns keep a stable alert
identity, so Alertmanager does not create parallel reports when severity changes.

Deployment files:

- `/etc/cron.d/ops-daily-ops-audit`
- `/etc/logrotate.d/ops-observability`

Validation:

```bash
python3 -m unittest discover -s observability/scripts/tests -v
python3 -m py_compile observability/scripts/daily_ops_audit*.py
bash -n observability/scripts/write-daily-ops-audit.sh
jq empty observability/grafana/dashboards/losangeles-daily-audit.json
```

Operational response is documented in `runbooks/playbooks/daily-ops-audit.md`.

## Security audit

The managed auditd baseline records writes to identity, sudo, SSH, systemd,
audit, and `/opt/ops` configuration paths. It also records root commands
started by an authenticated non-system login user. Raw auditd records remain
on the host; Promtail drops `EXECVE` and `PROCTITLE` before forwarding to Loki
so command arguments are not copied into the shared log store.

Security textfile metrics expose auditd service state, kernel audit state,
managed rule coverage, lost events, and backlog utilization. The security
dashboard links these metrics with the filtered audit log stream.

The local audit log rotation budget is a capacity guard, not a 180-day archive.
Compliance with the repository's 180-day tamper-resistant retention requirement
needs a separately approved immutable or access-isolated object-store archive.
See `runbooks/playbooks/auditd-security-audit.md` for validation, privacy rules, and rollback.

Daily sensitive-log archiving is a separate pipeline from Loki. When its control-plane
prerequisites are ready, enable the managed cron with
`-e compliance_archive_enabled=true`; it filters the previous UTC day, uploads through
an append-only Worker endpoint, and verifies through a separate read-only R2 token.
Cloudflare R2 currently does not provide S3 Object Lock, so the Worker protects against
host-side overwrite/delete but is not a substitute for a cloud-provider WORM control.
See `runbooks/playbooks/compliance-log-archive.md`.

## Synthetic SLO and sub2api capacity symptoms

Business Blackbox journeys feed an initial 99.9% synthetic availability SLO.
Recording rules expose 30-day availability, observation coverage, remaining error
budget, and 5-minute/30-minute/1-hour/6-hour burn rates. Multi-window alerts only
use the synthetic journey signal; the current five-minute Nginx gauges are not
treated as precise request counters.

The sub2api capacity collector reads only the latest five minutes of Docker logs,
counts the exact `no available account` symptom, emits aggregate metrics, and
deletes its root-only temporary log copy. It never writes raw application logs to
the textfile collector.

See `runbooks/playbooks/sub2api-slo-capacity.md` for objectives, warm-up behavior, and response.

## Grafana investigation and alert handling

Prometheus is the only alert-rule source. The provisioned Alertmanager datasource is
read-only from Grafana's rule-management perspective
(`handleGrafanaManagedAlerts: false`): use Grafana to inspect and filter the existing
Prometheus alerts, and use Alertmanager for notification routing and silences. Do not
create a duplicate Grafana-managed copy of a Prometheus rule.

Operational entry points are available from every provisioned dashboard:

- **运行 / 可靠性 / 安全与审计** split the thirteen provisioned dashboards into three
  purpose-specific dropdowns instead of one flat list while preserving the current
  time range.
- **活动告警** opens `/alerting/list` for label and state filtering.
- **静默管理** opens `/alerting/silences` for time-bounded maintenance silences.
- **返回服务总览** preserves the current time range and returns to the NOC home.
- The service overview also links to the independent GitHub Actions external-uptime run.

The service overview is an exception-first NOC page. Its first row contains only four
actionable signals: active alerts, failed probes, stopped containers, and overdue
backups. A service health matrix and the current firing-alert queue provide the next
investigation step; host trends follow below, while SLO and capacity diagnostics remain
collapsed until requested. The active-alert panel separates critical and warning counts;
the queue and matrix sort incident signals before healthy services. Stat panels color the
value rather than filling healthy panels with green, so red and amber retain their
incident meaning. The overview groups deployment/restart/config-drift events into one
**运行变更** annotation and backup/restore events into one **备份与恢复** annotation.

Every silence must use the narrowest practical matchers and include an owner, reason,
and expiry. Prefer a one-hour, four-hour, or approved maintenance-window duration;
never create a permanent silence. Do not silence shared collector, notification-path,
or backup-integrity root-cause alerts merely to hide their downstream symptoms.

Prometheus Explore correlations use only label contracts that exist on both sides:

- `service` opens the matching Loki service stream.
- Docker metric label `name` opens the matching sanitized
  `{job="business_errors", container="..."}` stream when that business container is
  covered by the sanitizer.

Prometheus `job` and `instance` are intentionally not treated as Loki correlation keys:
their values have different meanings in the two systems. Deployment, container restart,
backup, restore-drill, and configuration-drift annotations provide the change timeline
on the dashboards that own those domains.

Dashboard refresh intervals follow source cost and operational urgency: one minute for
service, application, host, datastore, observability, and Sub2API SLO views; five minutes
for daily audit, service/backup, security, and traffic views; ten minutes for asset and
TLS/Cloudflare governance. Raw logs and low-frequency diagnostics are collapsed by
default so opening a dashboard does not immediately execute every expensive query.

Reloadable Prometheus, Loki, Promtail, and Blackbox configuration is mounted read-only
by directory. Do not replace these mounts with single-file bind mounts: Git updates files
atomically, and a running container can otherwise keep reading the previous inode even
after a successful reload request. Exporter healthchecks that read `/metrics` must fully
consume the response rather than use an early-closing HTTP probe.

Managed host-job deployment:

```bash
cd /opt/ops/ansible
ansible-playbook observability-host-jobs.yml --check --diff --limit LosAngeles
# After production approval:
ansible-playbook observability-host-jobs.yml --limit LosAngeles
```

The playbook deploys all collector and backup entry points; installs version-controlled
`/etc/cron.d/ops-*` jobs for observability, local backup, manifest and verified R2 sync;
optionally installs the two GitHub Issue jobs; and installs the observability logrotate
policy. Scripts, cron and logrotate are read from one commit-addressed generation with a
SHA-256 manifest. The exact legacy backup lines are removed from root crontab only after
the replacement cron files are installed, with a root-only pre-migration copy retained.

Post-deployment validation:

The final assertions for `auditd_check_success` and `audit_log_pipeline_check_success` are
expected to remain `0` until the separately approved auditd deployment has completed. Run
the complete block again after auditd so the full observability gate is meaningful.

```bash
sudo stat -c '%a %U:%G %n' /etc/cron.d/ops-{daily-ops-audit,docker-metrics,runtime-snapshot,business-error-log,cloudflare-ip-metrics,business-log-metrics,cloudflare-origin-cert-metrics,fail2ban-enriched,security-metrics,sub2api-capacity-metrics}
sudo logrotate --debug /etc/logrotate.d/ops-observability
sudo /opt/ops/observability/scripts/write-daily-ops-audit.sh --no-email
sudo /opt/ops/observability/scripts/write-docker-metrics.sh
sudo /opt/ops/observability/scripts/write-security-metrics.sh
sudo /opt/ops/observability/scripts/write-sub2api-capacity-metrics.sh
sudo /opt/ops/observability/scripts/runtime_snapshot.py
sudo /opt/ops/observability/scripts/business_error_log.py
sudo /opt/ops/observability/scripts/cloudflare_ip_metrics.py

prom_value() {
  curl -fsSG http://127.0.0.1:9090/api/v1/query \
    --data-urlencode "query=$1" |
    jq -er '.data.result | if length == 1 then .[0].value[1] | tonumber else error("expected one series") end'
}

now="$(date +%s)"
daily_ts="$(prom_value daily_ops_audit_last_success_timestamp)"
docker_ts="$(prom_value docker_metrics_last_run_timestamp)"
security_ts="$(prom_value security_metrics_last_success_timestamp)"
sub2api_ts="$(prom_value sub2api_capacity_metrics_last_run_timestamp)"
runtime_ts="$(prom_value ops_runtime_snapshot_last_success_timestamp)"
business_error_ts="$(prom_value business_error_log_last_success_timestamp)"
cloudflare_ip_ts="$(prom_value cloudflare_ip_ranges_last_run_timestamp)"

test "$((now - ${daily_ts%.*}))" -lt 100800
test "$(prom_value daily_ops_audit_data_source_failures)" -eq 0
test "$((now - ${docker_ts%.*}))" -lt 600
test "$(prom_value docker_metrics_check_success)" -eq 1
test "$((now - ${security_ts%.*}))" -lt 300
test "$(prom_value auditd_check_success)" -eq 1
test "$(prom_value audit_log_pipeline_check_success)" -eq 1
audit_pipeline_ts="$(prom_value audit_log_pipeline_last_event_timestamp_seconds)"
test "$((now - ${audit_pipeline_ts%.*}))" -lt 300
test "$(prom_value ufw_status_check_success)" -eq 1
test "$(prom_value 'fail2ban_check_success{jail="sshd"}')" -eq 1
test "$((now - ${sub2api_ts%.*}))" -lt 300
test "$(prom_value sub2api_log_check_success)" -eq 1
test "$((now - ${runtime_ts%.*}))" -lt 180
test "$(prom_value ops_runtime_snapshot_check_success)" -eq 1
test "$((now - ${business_error_ts%.*}))" -lt 180
test "$(prom_value business_error_log_check_success)" -eq 1
test "$(prom_value business_error_log_source_failures)" -eq 0
test "$((now - ${cloudflare_ip_ts%.*}))" -lt 300
test "$(prom_value cloudflare_ip_ranges_check_success)" -eq 1
test "$(prom_value 'min(cloudflare_ip_ranges_match)')" -eq 1
```

The first host-job deployment is allowed to report only the explicitly registered
`observed=direct, desired=cloudflare-only` route drift until the separately approved
origin restriction is applied. After all controlled Compose files and Cloudflare-only
routes have converged, the final gate is:

```bash
test "$(prom_value 'sum(ops_config_drift)')" -eq 0
```

Rollback uses `ansible/observability-host-jobs-rollback.yml` with an explicit inactive
40-character generation ID. It validates both target and rescue generations, switches
`current` atomically, installs that generation's cron and logrotate files, and restores
the original generation automatically if post-switch validation fails. Historical
generations without `generation.sha256` and embedded cron are intentionally rejected.

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

The first deployment that migrates Prometheus, Loki, Promtail, or Blackbox from a
single-file bind mount to its read-only directory mount must recreate that component.
After the migration, validate changes to `prometheus.yml` or its rule files and use the
lifecycle endpoint so Prometheus reads the current directory contents without a restart:

```bash
cd /opt/ops/observability
curl -fsS -X POST http://127.0.0.1:9090/-/reload
```

After changing `blackbox.yml`, validate it and recreate Blackbox Exporter so it loads the
new configuration:

```bash
cd /opt/ops/observability
docker compose up -d --force-recreate --no-deps blackbox-exporter
```

After changing `loki.yml`, validate it and recreate Loki. Its persisted data remains on
the `/var/lib/observability/loki` bind mount:

```bash
cd /opt/ops/observability
docker compose up -d --force-recreate --no-deps loki
```

After changing Alertmanager config or templates, validate and recreate Alertmanager:

```bash
cd /opt/ops/observability
docker compose up -d --force-recreate --no-deps alertmanager
```

An atomically replaced SMTP credential is a new host inode while an existing single-file
bind mount still references the old inode. Rotate it only with
`scripts/deploy/rotate-alertmanager-smtp.py`, then recreate Alertmanager as described in
[`alertmanager-notification-delivery.md`](../runbooks/playbooks/alertmanager-notification-delivery.md).
The `alertmanager_runtime_input_stale` metric detects both an unreloaded config tree and
a credential replacement that has not yet been loaded by a recreated container.

After changing Promtail configuration or its position storage mount, validate the
configuration and recreate Promtail:

```bash
cd /opt/ops/observability
install -d -m 0750 /var/lib/observability/promtail
docker compose up -d --force-recreate --no-deps promtail
```


## Datastore exporter secrets

Root-only env files live outside Git:

- `/etc/observability/postgres-exporter-sub2api.env`
- `/etc/observability/postgres-exporter-account-vault.env`
- `/etc/observability/redis-exporter-sub2api.env`

The exporter containers join both the observability network and the relevant service network. They do not publish ports to the host or public internet.
