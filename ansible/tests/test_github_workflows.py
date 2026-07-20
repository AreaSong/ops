from __future__ import annotations

import re
import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
WORKFLOW_ROOT = REPO_ROOT / ".github" / "workflows"


class GitHubWorkflowTests(unittest.TestCase):
    def test_workflows_parse_and_pin_actions(self) -> None:
        for path in WORKFLOW_ROOT.glob("*.yml"):
            content = path.read_text(encoding="utf-8")
            yaml.safe_load(content)
            action_refs = re.findall(r"uses:\s*[^@\s]+@([^\s]+)", content)
            for reference in action_refs:
                self.assertRegex(reference, r"^[0-9a-f]{40}$", path.name)
            for checkout in content.split("uses: actions/checkout@")[1:]:
                self.assertIn("persist-credentials: false", checkout[:300], path.name)

    def test_governance_ci_is_read_only_and_covers_required_gates(self) -> None:
        content = (WORKFLOW_ROOT / "governance-ci.yml").read_text(encoding="utf-8")
        self.assertRegex(content, r"permissions:\s+contents: read")
        for gate in (
            "scripts/deploy/tests",
            "test_backup_integrity_rules.sh",
            "test_log_pipeline_rules.sh",
            "test_runtime_governance_rules.sh",
            "test_slo_rules.sh",
            "validate_observability_configs.sh",
            "compliance-archive-ingest/test",
            "docker compose",
            "validate_structured_files.py",
            "scripts/tests/*.test.mjs",
            "shellcheck",
            "BASE_SHA",
            "git hash-object -t tree /dev/null",
            'git diff --check "$base" HEAD --',
            "gitleaks",
        ):
            self.assertIn(gate, content)

    def test_image_cve_scan_is_read_only_scheduled_and_covers_all_compose_files(self) -> None:
        content = (WORKFLOW_ROOT / "image-cve-scan.yml").read_text(encoding="utf-8")
        self.assertRegex(content, r"permissions:\s+contents: read")
        self.assertIn("schedule", content)
        self.assertIn("workflow_dispatch", content)
        self.assertRegex(content, r"aquasec/trivy:[0-9][^@\s]*@sha256:[0-9a-f]{64}")
        self.assertIn("--ignore-unfixed", content)
        for source in ("observability/docker-compose.yml", "services/*/compose.yml"):
            self.assertIn(source, content)

    def test_monthly_external_simulation_has_a_separate_concurrency_group(self) -> None:
        content = (WORKFLOW_ROOT / "external-uptime.yml").read_text(encoding="utf-8")
        self.assertIn("monthly-simulation", content)
        self.assertIn("github.event.schedule == '17 3 1 * *'", content)


if __name__ == "__main__":
    unittest.main()
