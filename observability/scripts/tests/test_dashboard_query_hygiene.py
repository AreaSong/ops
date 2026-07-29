from __future__ import annotations

import json
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[3]
DASHBOARD_DIR = REPO_ROOT / "observability" / "grafana" / "dashboards"


def load_dashboard(filename: str) -> dict:
    return json.loads((DASHBOARD_DIR / filename).read_text(encoding="utf-8"))


def walk_panels(panels: list[dict]):
    for panel in panels:
        yield panel
        yield from walk_panels(panel.get("panels", []))


def panel_expressions(dashboard: dict, fragment: str) -> list[str]:
    return [
        target["expr"]
        for panel in walk_panels(dashboard["panels"])
        for target in panel.get("targets", [])
        if fragment in target.get("expr", "")
    ]


class DashboardQueryHygieneTests(unittest.TestCase):
    def test_postgres_deadlock_queries_use_counter_compatible_name(self) -> None:
        for filename in ("losangeles-datastores.json", "sub2api-slo-capacity.json"):
            queries = panel_expressions(
                load_dashboard(filename), "pg_stat_database_deadlocks"
            )
            self.assertTrue(queries, filename)
            self.assertTrue(
                all("increase(pg_stat_database_deadlocks" not in item for item in queries),
                filename,
            )
            self.assertTrue(
                all("pg_stat_database_deadlocks_total" in item for item in queries),
                filename,
            )

        alerts = yaml.safe_load(
            (REPO_ROOT / "observability/prometheus/rules/alerts.yml").read_text(
                encoding="utf-8"
            )
        )
        deadlock_alert = next(
            rule
            for group in alerts["groups"]
            for rule in group["rules"]
            if rule.get("alert") == "PostgresDeadlocksDetected"
        )
        self.assertNotIn("increase(pg_stat_database_deadlocks", deadlock_alert["expr"])
        self.assertIn("pg_stat_database_deadlocks_total", deadlock_alert["expr"])
        self.assertIn("[15m:]", deadlock_alert["expr"])

    def test_discrete_legends_and_values_are_human_readable(self) -> None:
        security = load_dashboard("losangeles-security-overview.json")
        nginx = next(
            panel
            for panel in walk_panels(security["panels"])
            if panel["title"] == "Nginx 请求（按状态码，5 分钟）"
        )
        self.assertEqual(nginx["targets"][0]["legendFormat"], "{{status_class}}")

        selfcheck = load_dashboard("losangeles-observability-selfcheck.json")
        notifications = next(
            panel
            for panel in walk_panels(selfcheck["panels"])
            if panel["title"] == "Alertmanager 告警与通知"
        )
        queries = {
            target["legendFormat"]: target["expr"]
            for target in notifications["targets"]
            if target["legendFormat"].startswith("1 小时")
        }
        self.assertEqual(set(queries), {"1 小时通知", "1 小时失败"})
        self.assertTrue(all(item.startswith("round(") for item in queries.values()))

    def test_cloudflare_history_queries_select_current_host_series(self) -> None:
        tls = load_dashboard("losangeles-certificates-cloudflare.json")
        queries = [
            *panel_expressions(tls, "cloudflare_origin_"),
            *panel_expressions(tls, "cloudflare_ip_ranges_"),
            *(
                item["expr"]
                for item in tls["annotations"]["list"]
                if "cloudflare_" in item.get("expr", "")
            ),
        ]
        self.assertTrue(queries)
        self.assertTrue(all('service="host"' in item for item in queries))

        selfcheck = load_dashboard("losangeles-observability-selfcheck.json")
        selfcheck_queries = [
            *panel_expressions(
                selfcheck, "cloudflare_origin_cert_metrics_last_run_timestamp"
            ),
            *panel_expressions(selfcheck, "cloudflare_ip_ranges_last_run_timestamp"),
        ]
        self.assertTrue(selfcheck_queries)
        self.assertTrue(all('service="host"' in item for item in selfcheck_queries))


if __name__ == "__main__":
    unittest.main()
