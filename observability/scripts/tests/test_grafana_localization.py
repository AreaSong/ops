from __future__ import annotations

import json
import re
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
DASHBOARD_DIR = REPO_ROOT / "observability" / "grafana" / "dashboards"
COMPOSE_PATH = REPO_ROOT / "observability" / "docker-compose.yml"
PROVIDER_PATH = (
    REPO_ROOT
    / "observability"
    / "grafana"
    / "provisioning"
    / "dashboards"
    / "dashboards.yml"
)
HAN_RE = re.compile(r"[\u4e00-\u9fff]")

EXPECTED_TITLES = {
    "应用与业务健康",
    "证书与 Cloudflare",
    "每日运维审计",
    "数据库与缓存",
    "主机资源总览",
    "安全态势总览",
    "资产与运行配置",
    "服务与备份",
    "Sub2API SLO 与容量",
    "流量审计",
}


def load_dashboards() -> list[dict]:
    return [
        json.loads(path.read_text(encoding="utf-8"))
        for path in sorted(DASHBOARD_DIR.glob("*.json"))
    ]


class GrafanaLocalizationTests(unittest.TestCase):
    def test_dashboards_use_chinese_titles_and_section_rows(self) -> None:
        dashboards = load_dashboards()
        self.assertEqual({dashboard["title"] for dashboard in dashboards}, EXPECTED_TITLES)

        for dashboard in dashboards:
            self.assertEqual(dashboard["timezone"], "browser")
            self.assertFalse(dashboard["editable"])
            panels = dashboard["panels"]
            panel_ids = [panel["id"] for panel in panels]
            self.assertEqual(len(panel_ids), len(set(panel_ids)))
            self.assertTrue(all(isinstance(panel_id, int) for panel_id in panel_ids))
            self.assertGreaterEqual(
                sum(panel["type"] == "row" for panel in panels),
                2,
                dashboard["title"],
            )
            for panel in panels:
                self.assertRegex(panel["title"], HAN_RE, panel["title"])

    def test_tables_hide_prometheus_transport_fields(self) -> None:
        for dashboard in load_dashboards():
            for panel in dashboard["panels"]:
                if panel["type"] != "table":
                    continue
                organize = next(
                    transformation
                    for transformation in panel.get("transformations", [])
                    if transformation["id"] == "organize"
                )
                excluded = organize["options"]["excludeByName"]
                for field in ("Time", "__name__", "instance", "job"):
                    self.assertTrue(excluded[field], f"{panel['title']}: {field}")

    def test_http_status_thresholds_are_semantically_correct(self) -> None:
        dashboard = next(
            dashboard
            for dashboard in load_dashboards()
            if dashboard["title"] == "应用与业务健康"
        )
        panel = next(panel for panel in dashboard["panels"] if panel["title"] == "HTTP 状态码")
        steps = panel["fieldConfig"]["defaults"]["thresholds"]["steps"]
        self.assertEqual(
            steps,
            [
                {"color": "green", "value": None},
                {"color": "orange", "value": 400},
                {"color": "red", "value": 500},
            ],
        )

    def test_grafana_defaults_to_chinese_and_disallows_ui_drift(self) -> None:
        compose = COMPOSE_PATH.read_text(encoding="utf-8")
        provider = PROVIDER_PATH.read_text(encoding="utf-8")
        self.assertIn("GF_USERS_DEFAULT_LANGUAGE: zh-Hans", compose)
        self.assertIn("allowUiUpdates: false", provider)


if __name__ == "__main__":
    unittest.main()
