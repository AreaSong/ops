from __future__ import annotations

import json
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
DASHBOARD_PATH = REPO_ROOT / "observability" / "grafana" / "dashboards" / "losangeles-server-asset-runtime.json"
DASHBOARD_DIR = REPO_ROOT / "observability" / "grafana" / "dashboards"


class DashboardContractTests(unittest.TestCase):
    def test_panel_ids_are_unique_and_cloudflare_metrics_are_visible(self) -> None:
        dashboard = json.loads(DASHBOARD_PATH.read_text(encoding="utf-8"))
        panels = dashboard["panels"]
        panel_ids = [panel["id"] for panel in panels]
        self.assertEqual(len(panel_ids), len(set(panel_ids)))

        expressions = {
            target["expr"]
            for panel in panels
            for target in panel.get("targets", [])
            if "expr" in target
        }
        self.assertIn("cloudflare_ip_ranges_check_success", expressions)
        self.assertIn("time() - cloudflare_ip_ranges_last_run_timestamp", expressions)
        self.assertIn("cloudflare_ip_ranges_match", expressions)

    def test_change_and_recovery_annotations_are_provisioned(self) -> None:
        expected_queries = {
            "losangeles-service-overview.json": {
                "changes(docker_container_started_at_timestamp_seconds[75s]) > 0",
                "increase(docker_container_restart_count[75s]) > 0",
            },
            "losangeles-services-backups.json": {
                "changes(backup_set_last_success_timestamp[5m]) > 0",
                "changes(r2_backup_last_success_timestamp[5m]) > 0",
                "changes(backup_set_r2_verify_last_success_timestamp[5m]) > 0",
                "changes(areaforge_restore_drill_last_success_timestamp[5m]) > 0",
            },
        }
        for filename, queries in expected_queries.items():
            with self.subTest(filename=filename):
                dashboard = json.loads((DASHBOARD_DIR / filename).read_text(encoding="utf-8"))
                expressions = {item["expr"] for item in dashboard["annotations"]["list"]}
                self.assertTrue(queries.issubset(expressions))

    def test_observability_components_have_dedicated_panels(self) -> None:
        path = DASHBOARD_DIR / "losangeles-observability-selfcheck.json"
        dashboard = json.loads(path.read_text(encoding="utf-8"))
        panels = dashboard["panels"]
        panel_ids = [panel["id"] for panel in panels]
        self.assertEqual(len(panel_ids), len(set(panel_ids)))
        titles = {panel["title"] for panel in panels}
        self.assertTrue(
            {
                "组件抓取状态",
                "组件常驻内存",
                "Alertmanager 告警与通知",
                "Loki Compactor 与保留清理",
            }.issubset(titles)
        )


if __name__ == "__main__":
    unittest.main()
