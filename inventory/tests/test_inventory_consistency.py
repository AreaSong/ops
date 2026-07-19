from __future__ import annotations

import unittest
from pathlib import Path

import yaml


INVENTORY_ROOT = Path(__file__).resolve().parents[1]


class InventoryConsistencyTests(unittest.TestCase):
    def setUp(self) -> None:
        self.servers = yaml.safe_load((INVENTORY_ROOT / "servers.yaml").read_text(encoding="utf-8"))
        self.services = yaml.safe_load((INVENTORY_ROOT / "services.yaml").read_text(encoding="utf-8"))["services"]
        assets = yaml.safe_load((INVENTORY_ROOT / "losangeles-assets.yaml").read_text(encoding="utf-8"))
        self.assets = {item["service_id"]: item for item in assets["services"]}
        self.inventory = assets

    def test_production_sources_contain_no_placeholder_hosts_or_addresses(self) -> None:
        serialized = yaml.safe_dump({"servers": self.servers, "services": self.services})
        for placeholder in ("x.x.x.x", "172.16.x.x", "prod-web-01", "prod-monitor-01", "prod-db-01"):
            self.assertNotIn(placeholder, serialized)

    def test_groups_and_service_summaries_reference_real_hosts(self) -> None:
        server_names = {server["hostname"] for server in self.servers["servers"]}
        self.assertEqual(server_names, {"LosAngeles"})
        for members in self.servers["groups"].values():
            self.assertLessEqual(set(members), server_names)
        for service in self.services:
            self.assertIn(service["host"], server_names)

    def test_losangeles_service_summary_matches_the_detailed_asset_source(self) -> None:
        for service in self.services:
            asset = self.assets.get(service["name"])
            self.assertIsNotNone(asset, service["name"])
            if service.get("backup"):
                self.assertTrue(asset.get("backup", {}).get("required"), service["name"])
            declared_host_ports = {
                port["host_port"] for port in asset.get("ports", []) if "host_port" in port
            }
            for port in service.get("ports", []):
                if port.get("expose") != "docker-network":
                    self.assertIn(port["port"], declared_host_ports, service["name"])

    def test_areaforge_runtime_and_controlled_compose_are_authoritative(self) -> None:
        for service_id in ("areaforge-web", "areaforge-postgres"):
            runtime = self.assets[service_id]["runtime"]
            self.assertEqual(runtime["runtime_compose"], "/opt/areaforge/docker-compose.prod.yml")
            self.assertEqual(runtime["controlled_compose"], "/opt/ops/services/areaforge/compose.yml")
        pairs = {item["service_id"]: item for item in self.inventory["config_pairs"]}
        self.assertEqual(pairs["areaforge-web"]["runtime"], "/opt/areaforge/docker-compose.prod.yml")
        self.assertEqual(pairs["areaforge-web"]["controlled"], "/opt/ops/services/areaforge/compose.yml")

    def test_routes_separate_observed_and_desired_origin_policy(self) -> None:
        for route in self.inventory["routes"]:
            self.assertIn(route["observed_origin_policy"], {"direct", "cloudflare-only"})
            self.assertIn(route["desired_origin_policy"], {"direct", "cloudflare-only"})


if __name__ == "__main__":
    unittest.main()
