from __future__ import annotations

import importlib.util
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock


MODULE_PATH = Path(__file__).resolve().parents[1] / "alertmanager_runtime_input.py"
SPEC = importlib.util.spec_from_file_location("alertmanager_runtime_input", MODULE_PATH)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class AlertmanagerRuntimeInputTests(unittest.TestCase):
    def test_parse_runtime_metrics_ignores_comments_and_labeled_series(self) -> None:
        values = MODULE.parse_prometheus_metrics(
            "# HELP demo demo\n"
            "process_start_time_seconds 100\n"
            "alertmanager_config_last_reload_success_timestamp_seconds 120\n"
            'labeled_metric{kind="demo"} 1\n'
        )
        self.assertEqual(values["process_start_time_seconds"], 100)
        self.assertEqual(
            values["alertmanager_config_last_reload_success_timestamp_seconds"], 120
        )
        self.assertNotIn("labeled_metric", values)

    def test_evaluate_staleness_uses_reload_for_config_and_start_for_credential(self) -> None:
        stale = MODULE.evaluate_staleness(
            config_mtime=130,
            credential_mtime=110,
            config_reload_timestamp=120,
            process_start_timestamp=100,
        )
        self.assertEqual(stale, {"config": 1, "credential": 1})

        fresh = MODULE.evaluate_staleness(
            config_mtime=120,
            credential_mtime=100,
            config_reload_timestamp=120,
            process_start_timestamp=100,
        )
        self.assertEqual(fresh, {"config": 0, "credential": 0})

    def test_main_writes_successful_atomic_snapshot_without_secret_content(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            config_root = root / "alertmanager"
            config_root.mkdir()
            config = config_root / "alertmanager.yml"
            config.write_text("route: {}\n", encoding="utf-8")
            credential = root / "smtp-password"
            credential.write_text("not-a-real-secret\n", encoding="utf-8")
            output = root / "metrics.prom"
            os.utime(config, (90, 90))
            os.utime(credential, (95, 95))

            environment = {
                "ALERTMANAGER_CONFIG_ROOT": str(config_root),
                "ALERTMANAGER_CREDENTIAL_PATH": str(credential),
                "ALERTMANAGER_RUNTIME_INPUT_OUT": str(output),
            }
            runtime = {
                "alertmanager_config_last_reload_success_timestamp_seconds": 100.0,
                "process_start_time_seconds": 100.0,
            }
            with mock.patch.dict(os.environ, environment, clear=False), mock.patch.object(
                MODULE, "fetch_runtime_metrics", return_value=runtime
            ), mock.patch("sys.argv", [str(MODULE_PATH)]):
                self.assertEqual(MODULE.main(), 0)

            content = output.read_text(encoding="utf-8")
            self.assertIn("alertmanager_runtime_input_check_success 1", content)
            self.assertIn('alertmanager_runtime_input_stale{kind="config"} 0', content)
            self.assertIn('alertmanager_runtime_input_stale{kind="credential"} 0', content)
            self.assertNotIn("not-a-real-secret", content)
            self.assertEqual(output.stat().st_mode & 0o777, 0o644)

    def test_main_publishes_failed_check_without_false_stale_signal(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "metrics.prom"
            environment = {
                "ALERTMANAGER_CONFIG_ROOT": str(Path(directory) / "missing"),
                "ALERTMANAGER_CREDENTIAL_PATH": str(Path(directory) / "missing-secret"),
                "ALERTMANAGER_RUNTIME_INPUT_OUT": str(output),
            }
            with mock.patch.dict(os.environ, environment, clear=False), mock.patch(
                "sys.argv", [str(MODULE_PATH)]
            ):
                self.assertEqual(MODULE.main(), 1)

            content = output.read_text(encoding="utf-8")
            self.assertIn("alertmanager_runtime_input_check_success 0", content)
            self.assertIn('alertmanager_runtime_input_stale{kind="config"} 0', content)
            self.assertIn('alertmanager_runtime_input_stale{kind="credential"} 0', content)


if __name__ == "__main__":
    unittest.main()
