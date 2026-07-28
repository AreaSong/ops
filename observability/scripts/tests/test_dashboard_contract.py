from __future__ import annotations

import json
import re
import unittest
from pathlib import Path

import yaml


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
        self.assertIn('docker_storage_size_bytes{type="build_cache"} or on() vector(0)', expressions)
        self.assertIn(
            'docker_storage_reclaimable_bytes{type="build_cache"} or on() vector(0)',
            expressions,
        )
        self.assertIn("time() - docker_build_cache_prune_last_success_timestamp", expressions)
        self.assertIn("docker_build_cache_prune_reclaimed_bytes", expressions)

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

    def test_backup_dashboard_matches_rpo_and_guarded_job_metrics(self) -> None:
        path = DASHBOARD_DIR / "losangeles-services-backups.json"
        dashboard = json.loads(path.read_text(encoding="utf-8"))
        panels = dashboard["panels"]
        panel_ids = [panel["id"] for panel in panels]
        self.assertEqual(len(panel_ids), len(set(panel_ids)))
        by_title = {panel["title"]: panel for panel in panels}
        self.assertIn("任务结果", by_title)
        self.assertIn("任务耗时", by_title)
        self.assertEqual(by_title["任务结果"]["targets"][0]["expr"], "backup_job_last_result")
        self.assertEqual(
            by_title["任务耗时"]["targets"][0]["expr"],
            "backup_job_last_duration_seconds",
        )
        for title in ("备份最旧(h)", "备份集(h)", "R2 同步(h)", "R2 校验(h)"):
            values = [
                step["value"]
                for step in by_title[title]["fieldConfig"]["defaults"]["thresholds"]["steps"]
                if step["value"] is not None
            ]
            self.assertEqual(values, [20, 24], title)
        compliance_expr = by_title["合规归档启用"]["targets"][0]["expr"]
        self.assertEqual(compliance_expr, "compliance_log_archive_configured or on() vector(0)")
        self.assertEqual(
            by_title["用户表"]["targets"][0]["expr"],
            "areaforge_restore_drill_user_tables",
        )

        occupied: set[tuple[int, int]] = set()
        for panel in panels:
            position = panel["gridPos"]
            for x in range(position["x"], position["x"] + position["w"]):
                for y in range(position["y"], position["y"] + position["h"]):
                    coordinate = (x, y)
                    self.assertNotIn(coordinate, occupied, panel["title"])
                    occupied.add(coordinate)

    def test_service_overview_handles_normal_and_not_applicable_states(self) -> None:
        path = DASHBOARD_DIR / "losangeles-service-overview.json"
        dashboard = json.loads(path.read_text(encoding="utf-8"))
        by_title = {panel["title"]: panel for panel in dashboard["panels"]}

        containers = by_title["容器运行状态（重启/健康/OOM）"]
        self.assertNotIn(
            "filterByValue",
            {item["id"] for item in containers["transformations"]},
        )
        self.assertIn(
            "0 * max by (name, service)",
            containers["targets"][1]["expr"],
        )

        health = by_title["服务健康总表"]
        budget_override = next(
            item
            for item in health["fieldConfig"]["overrides"]
            if item["matcher"].get("options") == "错误预算"
        )
        mapping = next(
            item["value"]
            for item in budget_override["properties"]
            if item["id"] == "mappings"
        )
        self.assertEqual(mapping[0]["options"]["match"], "null")
        self.assertEqual(mapping[0]["options"]["result"]["text"], "N/A")

    def test_slo_dashboard_filters_generic_panels_by_service_and_journey(self) -> None:
        dashboard = json.loads(
            (DASHBOARD_DIR / "sub2api-slo-capacity.json").read_text(encoding="utf-8")
        )
        variables = {item["name"]: item for item in dashboard["templating"]["list"]}
        self.assertEqual(set(variables), {"service", "journey"})
        self.assertIn('service=~"$service"', variables["journey"]["query"])

        generic_panel_ids = {1, 2, 3, 4, 5, 6, 9}
        for panel in dashboard["panels"]:
            if panel["id"] not in generic_panel_ids:
                continue
            for target in panel.get("targets", []):
                self.assertIn('service=~"$service"', target["expr"], panel["title"])
                self.assertIn('journey=~"$journey"', target["expr"], panel["title"])

    def test_business_dashboard_uses_golden_signal_recordings(self) -> None:
        dashboard = json.loads(
            (DASHBOARD_DIR / "losangeles-app-health.json").read_text(encoding="utf-8")
        )
        expressions = {
            target["expr"]
            for panel in dashboard["panels"]
            for target in panel.get("targets", [])
            if "expr" in target
        }
        self.assertTrue(
            {
                "service:business_http_requests:sum_5m",
                "service:business_http_4xx:ratio_5m",
                "service:business_http_5xx:ratio_5m",
                "service:business_http_slow:ratio_5m",
            }.issubset(expressions)
        )

    def test_xray_email_is_parsed_at_query_time_not_indexed_in_loki(self) -> None:
        promtail = yaml.safe_load(
            (REPO_ROOT / "observability" / "promtail" / "promtail-config.yml").read_text(
                encoding="utf-8"
            )
        )
        xray = next(item for item in promtail["scrape_configs"] if item["job_name"] == "xray_access")
        labels_stage = next(stage["labels"] for stage in xray["pipeline_stages"] if "labels" in stage)
        self.assertNotIn("email", labels_stage)

        dashboard = json.loads(
            (DASHBOARD_DIR / "losangeles-xray-traffic-audit.json").read_text(encoding="utf-8")
        )
        email_variable = next(item for item in dashboard["templating"]["list"] if item["name"] == "email")
        self.assertEqual(email_variable["datasource"]["uid"], "prometheus")
        self.assertEqual(email_variable["query"], "label_values(xray_user_traffic_bytes_total, email)")
        for panel in dashboard["panels"]:
            for target in panel.get("targets", []):
                expression = target.get("expr", "")
                self.assertIsNone(
                    re.search(r'\{[^}]*job="xray_access"[^}]*email=~', expression),
                    panel["title"],
                )

    def test_service_label_contract_maps_business_logs_and_xui_probe(self) -> None:
        prometheus = yaml.safe_load(
            (REPO_ROOT / "observability" / "prometheus" / "prometheus.yml").read_text(
                encoding="utf-8"
            )
        )
        blackbox_https = next(
            item for item in prometheus["scrape_configs"] if item["job_name"] == "blackbox_https"
        )
        self.assertEqual(blackbox_https["static_configs"][0]["labels"]["service"], "x-ui")

        node = next(item for item in prometheus["scrape_configs"] if item["job_name"] == "node")
        self.assertEqual(node["static_configs"][0]["labels"]["service"], "host")

        promtail = yaml.safe_load(
            (
            REPO_ROOT / "observability" / "promtail" / "promtail-config.yml"
            ).read_text(encoding="utf-8")
        )
        nginx = next(item for item in promtail["scrape_configs"] if item["job_name"] == "nginx")
        stages = nginx["pipeline_stages"][0]["match"]["stages"]
        replacements = {
            stage["replace"]["replace"]: stage["replace"]["expression"]
            for stage in stages
            if "replace" in stage
        }
        expected = {
            "resume-jadeai": "resume.areasong.top",
            "account-vault": "sorryiossearch.areasong.top",
            "sub2api": "cpa.areasong.top",
            "areaforge": "forge.areasong.top",
        }
        for service, domain in expected.items():
            expression = re.compile(replacements[service])
            self.assertGreater(expression.groups, 0, service)
            self.assertIsNotNone(expression.fullmatch(domain), service)


if __name__ == "__main__":
    unittest.main()
