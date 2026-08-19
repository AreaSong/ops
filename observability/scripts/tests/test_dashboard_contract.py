from __future__ import annotations

import json
import re
import unittest
from pathlib import Path

import yaml

from observability.scripts import business_error_log, docker_metrics


REPO_ROOT = Path(__file__).resolve().parents[3]
DASHBOARD_PATH = REPO_ROOT / "observability" / "grafana" / "dashboards" / "losangeles-server-asset-runtime.json"
DASHBOARD_DIR = REPO_ROOT / "observability" / "grafana" / "dashboards"
ALLOWED_PANEL_WIDTHS = {4, 6, 8, 12, 16, 24}


def load_dashboard(filename: str) -> dict:
    return json.loads((DASHBOARD_DIR / filename).read_text(encoding="utf-8"))


def walk_panels(panels: list[dict]):
    for panel in panels:
        yield panel
        yield from walk_panels(panel.get("panels", []))


def assert_no_overlap(test: unittest.TestCase, panels: list[dict], context: str) -> None:
    occupied: set[tuple[int, int]] = set()
    for panel in panels:
        position = panel["gridPos"]
        test.assertIn(position["w"], ALLOWED_PANEL_WIDTHS, f"{context}: {panel['title']}")
        test.assertGreaterEqual(position["x"], 0, f"{context}: {panel['title']}")
        test.assertLessEqual(
            position["x"] + position["w"],
            24,
            f"{context}: {panel['title']}",
        )
        for x in range(position["x"], position["x"] + position["w"]):
            for y in range(position["y"], position["y"] + position["h"]):
                coordinate = (x, y)
                test.assertNotIn(coordinate, occupied, f"{context}: {panel['title']}")
                occupied.add(coordinate)
        if panel.get("panels"):
            assert_no_overlap(test, panel["panels"], f"{context} / {panel['title']}")


class DashboardContractTests(unittest.TestCase):
    def test_prometheus_relabel_replacements_have_required_capture_groups(self) -> None:
        prometheus = yaml.safe_load(
            (REPO_ROOT / "observability/prometheus/prometheus.yml").read_text(
                encoding="utf-8"
            )
        )
        for scrape in prometheus["scrape_configs"]:
            for rule in scrape.get("metric_relabel_configs", []):
                replacement = str(rule.get("replacement", ""))
                references = [int(item) for item in re.findall(r"\$(\d+)", replacement)]
                if not references:
                    continue
                regex = str(rule.get("regex", "(.*)"))
                self.assertGreaterEqual(
                    re.compile(regex).groups,
                    max(references),
                    f"{scrape['job_name']}: {replacement} references a missing group in {regex}",
                )

    def test_prometheus_to_loki_correlations_use_real_shared_labels(self) -> None:
        datasources = yaml.safe_load(
            (
                REPO_ROOT
                / "observability"
                / "grafana"
                / "provisioning"
                / "datasources"
                / "datasources.yml"
            ).read_text(encoding="utf-8")
        )
        prometheus = next(item for item in datasources["datasources"] if item["uid"] == "prometheus")
        expressions = {
            correlation["config"]["target"]["expr"]
            for correlation in prometheus["correlations"]
        }
        self.assertEqual(
            expressions,
            {
                '{service="$${__field.labels.service}"}',
                '{job="business_errors",container="$${__field.labels.name}"}',
            },
        )

    def test_global_dashboard_layout_refresh_and_navigation_contract(self) -> None:
        expected_refresh = {
            "areasong-ops-control-plane.json": "1m",
            "losangeles-app-health.json": "1m",
            "losangeles-certificates-cloudflare.json": "10m",
            "losangeles-daily-audit.json": "5m",
            "losangeles-datastores.json": "1m",
            "losangeles-host-overview.json": "1m",
            "losangeles-observability-selfcheck.json": "1m",
            "losangeles-security-overview.json": "5m",
            "losangeles-server-asset-runtime.json": "10m",
            "losangeles-service-overview.json": "1m",
            "losangeles-services-backups.json": "5m",
            "losangeles-xray-traffic-audit.json": "5m",
            "sub2api-slo-capacity.json": "1m",
        }
        expected_category = {
            "areasong-ops-control-plane.json": "nav-runtime",
            "losangeles-service-overview.json": "nav-runtime",
            "losangeles-app-health.json": "nav-runtime",
            "losangeles-host-overview.json": "nav-runtime",
            "losangeles-datastores.json": "nav-runtime",
            "sub2api-slo-capacity.json": "nav-runtime",
            "losangeles-observability-selfcheck.json": "nav-reliability",
            "losangeles-services-backups.json": "nav-reliability",
            "losangeles-certificates-cloudflare.json": "nav-reliability",
            "losangeles-daily-audit.json": "nav-governance",
            "losangeles-security-overview.json": "nav-governance",
            "losangeles-server-asset-runtime.json": "nav-governance",
            "losangeles-xray-traffic-audit.json": "nav-governance",
        }
        navigation_contract = [
            ("运行", ("nav-runtime",)),
            ("可靠性", ("nav-reliability",)),
            ("安全与审计", ("nav-governance",)),
        ]
        self.assertEqual(
            {path.name for path in DASHBOARD_DIR.glob("*.json")},
            set(expected_refresh),
        )
        for filename, refresh in expected_refresh.items():
            with self.subTest(filename=filename):
                dashboard = load_dashboard(filename)
                self.assertEqual(dashboard["refresh"], refresh)
                panel_ids = [panel["id"] for panel in walk_panels(dashboard["panels"])]
                self.assertEqual(len(panel_ids), len(set(panel_ids)), dashboard["title"])
                assert_no_overlap(self, dashboard["panels"], dashboard["title"])
                category_tags = {
                    tag for tag in dashboard["tags"] if tag.startswith("nav-")
                }
                self.assertEqual(category_tags, {expected_category[filename]})

                navigation_links = [
                    link for link in dashboard["links"] if link["type"] == "dashboards"
                ]
                self.assertEqual(
                    [(link["title"], tuple(link["tags"])) for link in navigation_links],
                    navigation_contract,
                )
                self.assertTrue(all(link["asDropdown"] for link in navigation_links))
                self.assertTrue(all(link["keepTime"] for link in navigation_links))

                stat_panels = [
                    panel for panel in walk_panels(dashboard["panels"])
                    if panel["type"] == "stat"
                ]
                self.assertTrue(
                    all(panel["options"]["colorMode"] == "value" for panel in stat_panels),
                    dashboard["title"],
                )

                first_row = min(
                    (panel for panel in dashboard["panels"] if panel["type"] == "row"),
                    key=lambda panel: panel["gridPos"]["y"],
                )
                later_rows = [
                    panel["gridPos"]["y"]
                    for panel in dashboard["panels"]
                    if panel["type"] == "row"
                    and panel["gridPos"]["y"] > first_row["gridPos"]["y"]
                ]
                first_section_end = min(later_rows) if later_rows else 10**9
                first_section_stats = [
                    panel
                    for panel in dashboard["panels"]
                    if panel["type"] == "stat"
                    and first_row["gridPos"]["y"] < panel["gridPos"]["y"] < first_section_end
                ]
                self.assertLessEqual(len(first_section_stats), 6, dashboard["title"])

                links = dashboard["links"]
                self.assertTrue(
                    dashboard["uid"] == "losangeles-service-overview"
                    or any(
                        link.get("type") == "link"
                        and "losangeles-service-overview" in link.get("url", "")
                        for link in links
                    ),
                    dashboard["title"],
                )
                self.assertTrue(
                    any(
                        link.get("type") == "link"
                        and "/alerting/list" in link.get("url", "")
                        for link in links
                    ),
                    dashboard["title"],
                )

    def test_state_timelines_and_key_data_links_are_actionable(self) -> None:
        expected_timelines = {
            "losangeles-app-health.json": "应用与业务探测状态",
            "losangeles-observability-selfcheck.json": "组件抓取状态",
            "losangeles-service-overview.json": "探测状态",
            "sub2api-slo-capacity.json": "业务链路探测状态",
        }
        for filename, title in expected_timelines.items():
            dashboard = load_dashboard(filename)
            panel = next(panel for panel in walk_panels(dashboard["panels"]) if panel["title"] == title)
            self.assertEqual(panel["type"], "state-timeline", title)
            for target in panel["targets"]:
                self.assertFalse(target.get("instant", False), title)
                self.assertNotIn("vector(0)", target["expr"], title)

        observability = load_dashboard("losangeles-observability-selfcheck.json")
        component_state = next(
            panel
            for panel in walk_panels(observability["panels"])
            if panel["title"] == "组件抓取状态"
        )
        self.assertEqual(
            {target["expr"] for target in component_state["targets"]},
            {
                'min(up{job="prometheus"})',
                'min(up{job="grafana"})',
                'min(up{job="loki"})',
                'min(up{job="promtail"})',
                'min(up{job="alertmanager"})',
                'min(up{job="blackbox_exporter"})',
            },
        )
        self.assertEqual(component_state["gridPos"], {"x": 0, "y": 1, "w": 12, "h": 5})

        overview = load_dashboard("losangeles-service-overview.json")
        by_title = {panel["title"]: panel for panel in walk_panels(overview["panels"])}
        for title in ("活动告警", "探测异常", "停摆容器", "备份超期"):
            links = by_title[title]["fieldConfig"]["defaults"].get("links", [])
            self.assertTrue(links, title)
            self.assertTrue(any("${__url_time_range}" in link["url"] for link in links), title)
        queue_links = by_title["待处理告警"]["fieldConfig"]["defaults"]["links"]
        self.assertEqual(queue_links[0]["url"], "/alerting/list?${__url_time_range}")

        external_links = overview["links"]
        self.assertTrue(
            any("external-uptime.yml" in link.get("url", "") for link in external_links)
        )

    def test_areasong_ops_rotation_panels_define_an_idle_fallback(self) -> None:
        dashboard = load_dashboard("areasong-ops-control-plane.json")
        by_title = {
            panel["title"]: panel for panel in walk_panels(dashboard["panels"])
        }
        metrics = {
            "活动凭据轮换状态": "areasong_ops_credential_rotation_active",
            "凭据轮换持续时间": "areasong_ops_credential_rotation_age_seconds",
        }

        for title, metric in metrics.items():
            expression = by_title[title]["targets"][0]["expr"]
            self.assertIn(metric, expression)
            self.assertIn("or on() label_replace(label_replace(vector(0)", expression)
            self.assertIn('"state", "idle"', expression)

    def test_panel_ids_are_unique_and_docker_cache_metrics_are_visible(self) -> None:
        dashboard = json.loads(DASHBOARD_PATH.read_text(encoding="utf-8"))
        panels = list(walk_panels(dashboard["panels"]))
        panel_ids = [panel["id"] for panel in panels]
        self.assertEqual(len(panel_ids), len(set(panel_ids)))

        expressions = {
            target["expr"]
            for panel in panels
            for target in panel.get("targets", [])
            if "expr" in target
        }
        self.assertIn('docker_storage_size_bytes{type="build_cache"} or on() vector(0)', expressions)
        self.assertIn(
            'docker_storage_reclaimable_bytes{type="build_cache"} or on() vector(0)',
            expressions,
        )
        self.assertIn("time() - docker_build_cache_prune_last_success_timestamp", expressions)
        self.assertIn("docker_build_cache_prune_reclaimed_bytes", expressions)

    def test_change_and_recovery_annotations_are_provisioned(self) -> None:
        expected_queries = {
            "losangeles-services-backups.json": {
                "changes(backup_set_last_success_timestamp[5m]) > 0",
                "changes(r2_backup_last_success_timestamp[5m]) > 0",
                "changes(backup_set_r2_verify_last_success_timestamp[5m]) > 0",
                "changes(areaforge_restore_drill_last_success_timestamp[5m]) > 0",
                "changes(areasong_ops_restore_drill_last_success_timestamp_seconds[5m]) > 0",
                "changes(sub2api_restore_drill_last_success_timestamp_seconds[5m]) > 0",
            },
            "sub2api-slo-capacity.json": {
                'changes(docker_container_started_at_timestamp_seconds{service="sub2api"}[75s]) > 0',
                'increase(docker_container_restart_count{service="sub2api"}[75s]) > 0',
            },
            "losangeles-datastores.json": {
                "changes(backup_set_last_success_timestamp[5m]) > 0",
                "changes(areaforge_restore_drill_last_success_timestamp[5m]) > 0",
            },
            "losangeles-server-asset-runtime.json": {
                "changes(ops_config_drift[2m]) != 0",
            },
        }
        for filename, queries in expected_queries.items():
            with self.subTest(filename=filename):
                dashboard = json.loads((DASHBOARD_DIR / filename).read_text(encoding="utf-8"))
                expressions = {item["expr"] for item in dashboard["annotations"]["list"]}
                self.assertTrue(queries.issubset(expressions))

        overview = load_dashboard("losangeles-service-overview.json")
        overview_annotations = overview["annotations"]["list"]
        self.assertEqual(
            [annotation["name"] for annotation in overview_annotations],
            ["运行变更", "备份与恢复"],
        )
        merged_expressions = "\n".join(
            annotation["expr"] for annotation in overview_annotations
        )
        for fragment in (
            "changes(docker_container_started_at_timestamp_seconds[75s]) > 0",
            "increase(docker_container_restart_count[75s]) > 0",
            "changes(ops_config_drift[2m]) != 0",
            "changes(backup_set_last_success_timestamp[5m]) > 0",
            "changes(areaforge_restore_drill_last_success_timestamp[5m]) > 0",
        ):
            self.assertIn(fragment, merged_expressions)

    def test_observability_components_have_dedicated_panels(self) -> None:
        path = DASHBOARD_DIR / "losangeles-observability-selfcheck.json"
        dashboard = json.loads(path.read_text(encoding="utf-8"))
        panels = list(walk_panels(dashboard["panels"]))
        panel_ids = [panel["id"] for panel in panels]
        self.assertEqual(len(panel_ids), len(set(panel_ids)))
        titles = {panel["title"] for panel in panels}
        self.assertTrue(
            {
                "组件抓取状态",
                "组件常驻内存",
                "Alertmanager 告警与通知",
                "Loki Compactor 与保留清理",
                "Grafana 数据源请求 5xx",
                "Grafana 插件请求 P95",
            }.issubset(titles)
        )
        by_title = {panel["title"]: panel for panel in panels}
        alertmanager_expressions = {
            target["expr"] for target in by_title["Alertmanager 告警与通知"]["targets"]
        }
        self.assertIn(
            'max(alertmanager_runtime_input_stale{kind="config"}) or on() vector(0)',
            alertmanager_expressions,
        )
        self.assertIn(
            'max(alertmanager_runtime_input_stale{kind="credential"}) or on() vector(0)',
            alertmanager_expressions,
        )
        freshness_expressions = {
            target["expr"]
            for target in by_title["高频采集器距上次运行（分钟级任务）"]["targets"]
        }
        self.assertIn(
            "time() - alertmanager_runtime_input_last_check_timestamp_seconds",
            freshness_expressions,
        )

    def test_cloudflare_governance_is_owned_by_tls_dashboard(self) -> None:
        asset = load_dashboard("losangeles-server-asset-runtime.json")
        tls = load_dashboard("losangeles-certificates-cloudflare.json")
        self.assertEqual(tls["title"], "TLS 与 Cloudflare 治理")
        asset_expressions = {
            target.get("expr", "")
            for panel in walk_panels(asset["panels"])
            for target in panel.get("targets", [])
        }
        tls_expressions = {
            target.get("expr", "")
            for panel in walk_panels(tls["panels"])
            for target in panel.get("targets", [])
        }
        for metric, expression in (
            (
                "cloudflare_ip_ranges_check_success",
                'cloudflare_ip_ranges_check_success{service="host"}',
            ),
            (
                "cloudflare_ip_ranges_last_run_timestamp",
                'time() - cloudflare_ip_ranges_last_run_timestamp{service="host"}',
            ),
            (
                "cloudflare_ip_ranges_match",
                'cloudflare_ip_ranges_match{service="host"}',
            ),
        ):
            self.assertFalse(any(metric in item for item in asset_expressions))
            self.assertIn(expression, tls_expressions)
        tls_by_title = {panel["title"]: panel for panel in walk_panels(tls["panels"])}
        self.assertEqual(
            tls_by_title["Cloudflare IP 清单检查"]["gridPos"],
            {"x": 0, "y": 11, "w": 12, "h": 3},
        )
        self.assertEqual(
            tls_by_title["Cloudflare IP 检查距今"]["gridPos"],
            {"x": 0, "y": 14, "w": 12, "h": 3},
        )
        self.assertEqual(
            tls_by_title["Cloudflare 托管与官方 CIDR"]["gridPos"],
            {"x": 12, "y": 11, "w": 12, "h": 6},
        )

    def test_backup_dashboard_matches_rpo_and_guarded_job_metrics(self) -> None:
        path = DASHBOARD_DIR / "losangeles-services-backups.json"
        dashboard = json.loads(path.read_text(encoding="utf-8"))
        panels = list(walk_panels(dashboard["panels"]))
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
        self.assertEqual(
            by_title["演练来源"]["targets"][0]["expr"],
            "areaforge_restore_drill_last_success_timestamp",
        )
        recovery_age = {
            panel["title"]: panel
            for panel in panels
            if panel["title"] in {
                "AreaSong Ops 演练距今(天)",
                "Sub2API 演练距今(天)",
                "AreaForge 演练距今(天)",
            }
        }
        self.assertEqual(
            {
                title: panel["targets"][0]["expr"]
                for title, panel in recovery_age.items()
            },
            {
                "AreaSong Ops 演练距今(天)": (
                    "(time() - areasong_ops_restore_drill_last_success_timestamp_seconds) / 86400"
                ),
                "Sub2API 演练距今(天)": (
                    "(time() - sub2api_restore_drill_last_success_timestamp_seconds) / 86400"
                ),
                "AreaForge 演练距今(天)": (
                    "(time() - areaforge_restore_drill_last_success_timestamp) / 86400"
                ),
            },
        )
        self.assertEqual(set(recovery_age), {
            "AreaSong Ops 演练距今(天)",
            "Sub2API 演练距今(天)",
            "AreaForge 演练距今(天)",
        })
        for panel in recovery_age.values():
            self.assertEqual(panel["fieldConfig"]["defaults"]["noValue"], "无数据")
            values = [
                step["value"]
                for step in panel["fieldConfig"]["defaults"]["thresholds"]["steps"]
                if step["value"] is not None
            ]
            self.assertEqual(values, [28, 35], panel["title"])

    def test_service_overview_handles_normal_and_not_applicable_states(self) -> None:
        path = DASHBOARD_DIR / "losangeles-service-overview.json"
        dashboard = json.loads(path.read_text(encoding="utf-8"))
        by_title = {panel["title"]: panel for panel in walk_panels(dashboard["panels"])}

        top_level_stats = [
            panel["title"] for panel in dashboard["panels"] if panel["type"] == "stat"
        ]
        self.assertEqual(
            top_level_stats,
            ["活动告警", "探测异常", "停摆容器", "备份超期"],
        )
        self.assertEqual(
            [by_title[title]["gridPos"]["w"] for title in top_level_stats],
            [6, 6, 6, 6],
        )

        active_alerts = by_title["活动告警"]
        self.assertEqual(
            [(target["refId"], target["legendFormat"]) for target in active_alerts["targets"]],
            [("A", "严重"), ("B", "警告")],
        )
        self.assertIn('severity="critical"', active_alerts["targets"][0]["expr"])
        self.assertIn('severity="warning"', active_alerts["targets"][1]["expr"])
        warning_override = next(
            item
            for item in active_alerts["fieldConfig"]["overrides"]
            if item["matcher"] == {"id": "byFrameRefID", "options": "B"}
        )
        warning_thresholds = next(
            item["value"]
            for item in warning_override["properties"]
            if item["id"] == "thresholds"
        )
        self.assertEqual(
            warning_thresholds["steps"],
            [{"color": "green", "value": None}, {"color": "orange", "value": 1}],
        )

        missing_telemetry_panels = {
            "探测异常": {
                "expression": (
                    'count((min by (service) (probe_success{service!=""}) == 0) or '
                    '(min by (service) (up{job=~"blackbox_.*",job!="blackbox_exporter",'
                    'service!=""}) == 0)) or on() '
                    '(absent(up{job=~"blackbox_.*",job!="blackbox_exporter",service!=""}) * -1) '
                    "or vector(0)"
                ),
                "missing_text": "探测数据缺失",
            },
            "停摆容器": {
                "expression": (
                    "(count(docker_container_running == 0) and on() "
                    "(min(docker_metrics_check_success) == 1) and on() "
                    "(time() - max(docker_metrics_last_run_timestamp) <= 900)) or on() "
                    "((min(docker_metrics_check_success) == 0) * 0 - 1) or on() "
                    "((time() - max(docker_metrics_last_run_timestamp) > 900) * 0 - 1) or on() "
                    "(absent(docker_metrics_check_success) * -1) or on() "
                    "(absent(docker_metrics_last_run_timestamp) * -1) or vector(0)"
                ),
                "missing_text": "Docker 采集缺失",
            },
        }
        for title, expected in missing_telemetry_panels.items():
            panel = by_title[title]
            self.assertEqual(panel["targets"][0]["expr"], expected["expression"])
            self.assertEqual(
                panel["fieldConfig"]["defaults"]["mappings"][0]["options"]["-1"]["text"],
                expected["missing_text"],
            )
            self.assertEqual(
                panel["fieldConfig"]["defaults"]["thresholds"]["steps"],
                [
                    {"color": "red", "value": None},
                    {"color": "green", "value": 0},
                    {"color": "red", "value": 1},
                ],
            )

        backup_overdue = by_title["备份超期"]
        self.assertEqual(
            backup_overdue["targets"][0]["expr"],
            "count((time() - backup_set_last_success_timestamp) > 24 * 3600) "
            "or on() (absent(backup_set_last_success_timestamp) * -1) or vector(0)",
        )
        self.assertEqual(
            backup_overdue["fieldConfig"]["defaults"]["mappings"][0]["options"]["-1"]["text"],
            "备份数据缺失",
        )

        health = by_title["服务健康矩阵"]
        self.assertEqual(health["gridPos"], {"x": 0, "y": 5, "w": 16, "h": 8})
        self.assertEqual(
            health["options"]["sortBy"],
            [
                {"displayName": "告警", "desc": True},
                {"displayName": "非健康", "desc": True},
                {"displayName": "OOM", "desc": True},
                {"displayName": "探测", "desc": False},
                {"displayName": "服务", "desc": False},
            ],
        )
        self.assertEqual(
            {target["refId"] for target in health["targets"]},
            {"A", "B", "C", "D", "E", "F"},
        )
        self.assertIn("docker_container_running", health["targets"][3]["expr"])
        self.assertIn("docker_container_health_status", health["targets"][4]["expr"])
        self.assertIn("docker_container_oom_killed", health["targets"][5]["expr"])
        cell_options = [
            property["value"]["type"]
            for override in health["fieldConfig"]["overrides"]
            for property in override["properties"]
            if property["id"] == "custom.cellOptions"
        ]
        self.assertTrue(cell_options)
        self.assertEqual(set(cell_options), {"color-text"})

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

        alert_queue = by_title["待处理告警"]
        self.assertEqual(alert_queue["gridPos"], {"x": 16, "y": 5, "w": 8, "h": 8})
        self.assertIn(
            'absent(ALERTS{alertstate="firing"})',
            alert_queue["targets"][0]["expr"],
        )
        self.assertEqual(
            alert_queue["options"]["sortBy"],
            [{"displayName": "级别", "desc": False}],
        )
        severity_mapping = next(
            property["value"][0]["options"]
            for override in alert_queue["fieldConfig"]["overrides"]
            for property in override["properties"]
            if property["id"] == "mappings"
        )
        self.assertEqual(
            list(severity_mapping),
            ["4-normal", "3-other", "2-warning", "1-critical"],
        )

    def test_slo_dashboard_is_sub2api_scoped_and_gates_immature_long_window_data(self) -> None:
        dashboard = json.loads(
            (DASHBOARD_DIR / "sub2api-slo-capacity.json").read_text(encoding="utf-8")
        )
        variables = {item["name"]: item for item in dashboard["templating"]["list"]}
        self.assertEqual(set(variables), {"journey"})
        self.assertIn('service="sub2api"', variables["journey"]["query"])

        generic_panel_ids = {1, 2, 3, 4, 5, 6, 9}
        for panel in walk_panels(dashboard["panels"]):
            if panel["id"] not in generic_panel_ids:
                continue
            for target in panel.get("targets", []):
                self.assertIn('service="sub2api"', target["expr"], panel["title"])
                self.assertIn('journey=~"$journey"', target["expr"], panel["title"])

        by_title = {panel["title"]: panel for panel in walk_panels(dashboard["panels"])}
        availability = by_title["30 天合成可用性"]
        budget = by_title["30 天错误预算剩余"]
        for panel in (availability, budget):
            self.assertIn("service:synthetic_slo_coverage:ratio_30d", panel["targets"][0]["expr"])
            self.assertIn(">= 0.95", panel["targets"][0]["expr"])
        self.assertEqual(availability["fieldConfig"]["defaults"]["noValue"], "预热中")
        self.assertEqual(budget["fieldConfig"]["defaults"]["noValue"], "尚未生效")

        burn_expressions = {target["expr"] for target in by_title["合成 SLO 燃烧窗口"]["targets"]}
        self.assertTrue(any("ratio_30m" in expression for expression in burn_expressions))
        capacity_row = next(panel for panel in dashboard["panels"] if panel["title"] == "依赖与容量")
        self.assertTrue(capacity_row["collapsed"])
        self.assertEqual({panel["id"] for panel in capacity_row["panels"]}, {8, 9, 10, 11})

    def test_raw_log_diagnostics_are_collapsed(self) -> None:
        expected = {
            "losangeles-daily-audit.json": {11},
            "losangeles-security-overview.json": {7, 8, 13},
            "losangeles-server-asset-runtime.json": {14},
            "losangeles-xray-traffic-audit.json": {8},
        }
        for filename, panel_ids in expected.items():
            dashboard = load_dashboard(filename)
            collapsed_ids = {
                child["id"]
                for panel in dashboard["panels"]
                if panel["type"] == "row" and panel.get("collapsed")
                for child in panel.get("panels", [])
            }
            self.assertTrue(panel_ids.issubset(collapsed_ids), filename)

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
        self.assertNotIn("service", node["static_configs"][0]["labels"])
        self.assertEqual(
            node["metric_relabel_configs"],
            [
                {
                    "source_labels": ["service"],
                    "regex": "^$",
                    "target_label": "service",
                    "replacement": "host",
                }
            ],
        )

        grafana = next(
            item for item in prometheus["scrape_configs"] if item["job_name"] == "grafana"
        )
        self.assertEqual(
            grafana["metric_relabel_configs"],
            [
                {
                    "source_labels": ["exported_service"],
                    "regex": "(.+)",
                    "target_label": "component",
                    "replacement": "$1",
                },
                {"action": "labeldrop", "regex": "exported_service"},
            ],
        )

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
            "areasong-ops": "ops.areasong.top",
        }
        for service, domain in expected.items():
            expression = re.compile(replacements[service])
            self.assertGreater(expression.groups, 0, service)
            self.assertIsNotNone(expression.fullmatch(domain), service)

        business_errors = next(
            item for item in promtail["scrape_configs"] if item["job_name"] == "business_errors"
        )
        json_stage = next(
            stage["json"] for stage in business_errors["pipeline_stages"] if "json" in stage
        )
        labels_stage = next(
            stage["labels"] for stage in business_errors["pipeline_stages"] if "labels" in stage
        )
        self.assertEqual(json_stage["expressions"]["container"], "container")
        self.assertIn("container", labels_stage)

        areasong_ops_web = next(
            item for item in promtail["scrape_configs"] if item["job_name"] == "areasong_ops_web"
        )
        docker_json_stages = [
            stage["json"] for stage in areasong_ops_web["pipeline_stages"] if "json" in stage
        ]
        self.assertEqual(docker_json_stages[0]["expressions"]["attrs"], "attrs")
        self.assertEqual(
            docker_json_stages[1],
            {
                "source": "attrs",
                "expressions": {"service": "service", "component": "component"},
            },
        )
        self.assertTrue(
            set(business_error_log.DEFAULT_SOURCES.values()).issubset(
                set(docker_metrics.DEFAULT_CONTAINERS)
            )
        )


if __name__ == "__main__":
    unittest.main()
