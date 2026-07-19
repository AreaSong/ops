from __future__ import annotations

import importlib.util
import tempfile
import unittest
from pathlib import Path
from unittest import mock


MODULE_PATH = Path(__file__).resolve().parents[1] / "cloudflare_ip_metrics.py"
SPEC = importlib.util.spec_from_file_location("cloudflare_ip_metrics", MODULE_PATH)
assert SPEC and SPEC.loader
cloudflare_ip_metrics = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(cloudflare_ip_metrics)


class CloudflareIpMetricsTests(unittest.TestCase):
    def test_parse_rejects_wrong_address_family(self) -> None:
        with self.assertRaises(ValueError):
            cloudflare_ip_metrics.parse_networks("2606:4700::/32\n", 4)

    def test_compare_detects_exact_match(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            config_dir = Path(directory)
            config_dir.joinpath("ips-v4.txt").write_text("104.16.0.0/13\n", encoding="utf-8")
            config_dir.joinpath("ips-v6.txt").write_text("2606:4700::/32\n", encoding="utf-8")
            responses = ["104.16.0.0/13\n", "2606:4700::/32\n"]
            with mock.patch.object(cloudflare_ip_metrics, "fetch", side_effect=responses):
                results, success = cloudflare_ip_metrics.compare(config_dir)
        self.assertTrue(success)
        self.assertTrue(results["ipv4"]["match"])
        self.assertTrue(results["ipv6"]["match"])

    def test_compare_reports_official_drift(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            config_dir = Path(directory)
            config_dir.joinpath("ips-v4.txt").write_text("104.16.0.0/13\n", encoding="utf-8")
            config_dir.joinpath("ips-v6.txt").write_text("2606:4700::/32\n", encoding="utf-8")
            responses = ["104.16.0.0/13\n104.24.0.0/14\n", "2606:4700::/32\n"]
            with mock.patch.object(cloudflare_ip_metrics, "fetch", side_effect=responses):
                results, success = cloudflare_ip_metrics.compare(config_dir)
        self.assertTrue(success)
        self.assertFalse(results["ipv4"]["match"])


if __name__ == "__main__":
    unittest.main()
