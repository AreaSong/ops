from __future__ import annotations

import json
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
DASHBOARD_PATH = REPO_ROOT / "observability" / "grafana" / "dashboards" / "losangeles-server-asset-runtime.json"


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


if __name__ == "__main__":
    unittest.main()
