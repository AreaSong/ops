from __future__ import annotations

import sys
import unittest
from pathlib import Path
from types import SimpleNamespace

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from release_common import ReleaseError
from release_runtime import Runtime


class SystemdJobContractTests(unittest.TestCase):
    def runtime(self, output: str, code: int = 0):
        calls = []

        def execute(command, **options):
            calls.append((command, options))
            return SimpleNamespace(returncode=code, stdout=output, stderr="")

        return Runtime(None, execute), calls

    def test_systemd_255_empty_output_is_idle(self):
        for output in ("", "\n", " \t\n\n"):
            with self.subTest(output=output):
                runtime, _ = self.runtime(output)
                runtime.assert_no_jobs()

    def test_systemd_255_unrelated_text_job_does_not_block(self):
        runtime, _ = self.runtime("10350082 areaforge-update-agent.service start running\n")
        runtime.assert_no_jobs()

    def test_command_disables_headers_truncation_and_terminal_decoration(self):
        runtime, calls = self.runtime("")
        runtime.assert_no_jobs()
        command, options = calls[0]
        self.assertEqual(command, ["systemctl", "list-jobs", "--no-legend", "--plain", "--full", "--no-pager"])
        for name, value in {"LC_ALL": "C", "SYSTEMD_COLORS": "0", "SYSTEMD_URLIFY": "0"}.items():
            self.assertEqual(options["env"][name], value)

    def test_runner_jobs_always_block(self):
        for state in ("waiting", "running"):
            for action in ("start", "stop", "restart", "verify-active"):
                with self.subTest(state=state, action=action):
                    runtime, _ = self.runtime(f"31 areasong-ops-runner.service {action} {state}\n")
                    with self.assertRaisesRegex(ReleaseError, "排队"):
                        runtime.assert_no_jobs()

    def test_long_updater_job_in_mixed_output_blocks(self):
        unit = "areasong-ops-runner-update@" + "a" * 180 + ".service"
        output = f"1 unrelated.service start running\n  32\t{unit}\tstart waiting\n"
        runtime, _ = self.runtime(output)
        with self.assertRaisesRegex(ReleaseError, "排队"):
            runtime.assert_no_jobs()

    def test_escaped_device_and_root_mount_names_are_valid(self):
        runtime, _ = self.runtime("1 dev-disk-by\\x2duuid-test.device start waiting\n2 -.mount start running\n")
        runtime.assert_no_jobs()

    def test_malformed_or_truncated_output_fails_closed(self):
        samples = (
            "No jobs running.\n", "JOB UNIT TYPE STATE\n", "[]\n",
            "x other.service start running\n", "0 other.service start waiting\n",
            "1 other.service start\n", "1 other.service start running extra\n",
            "1 other.service start finished\n", "1 other.service START running\n",
            "1 areasong-ops-runner.serv… start running\n", "1 other start running\n",
            "1 other.service start running\n1 jobs listed.\n",
            "1 \x1b[31mother.service\x1b[0m start running\n",
        )
        for output in samples:
            with self.subTest(output=output):
                runtime, _ = self.runtime(output)
                with self.assertRaises(ReleaseError):
                    runtime.assert_no_jobs()

    def test_command_failure_is_not_treated_as_idle(self):
        runtime, calls = self.runtime("", code=1)
        with self.assertRaisesRegex(ReleaseError, "读取 systemd 作业失败"):
            runtime.assert_no_jobs()
        self.assertEqual(len(calls), 1)

    def test_oversized_output_is_rejected(self):
        runtime, _ = self.runtime("1 other.service start running\n" * 10000)
        with self.assertRaises(ReleaseError):
            runtime.assert_no_jobs()
