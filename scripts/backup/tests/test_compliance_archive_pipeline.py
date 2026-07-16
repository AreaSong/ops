from __future__ import annotations

import datetime as dt
import gzip
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

UTC = dt.timezone.utc
REPO_ROOT = Path(__file__).resolve().parents[3]
IMAGE = os.environ.get("COMPLIANCE_ARCHIVE_TEST_IMAGE", "python:3.12-slim")


class ComplianceArchivePipelineTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        if shutil.which("docker") is None:
            raise unittest.SkipTest("docker is required for the compliance archive integration test")

    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.source = self.root / "source"
        self.archive = self.root / "archive"
        self.remote = self.root / "remote"
        self.metrics = self.root / "metrics"
        self.config = self.root / "config"
        self.fake_bin = self.root / "fake-bin"
        for path in (
            self.source / "var/log/audit",
            self.source / "var/log/nginx",
            self.source / "var/log/observability",
            self.archive,
            self.remote,
            self.metrics,
            self.config,
            self.fake_bin,
        ):
            path.mkdir(parents=True, exist_ok=True)
        self.day = dt.date(2026, 7, 15)
        self._write_sources(self.day)
        self._write_config()
        self._write_fake_commands()

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def _write_sources(self, day: dt.date) -> None:
        start = dt.datetime.combine(day, dt.time.min, UTC)
        (self.source / "var/log/audit/audit.log").write_text(
            f"type=USER msg=audit({start.timestamp() + 60:.3f}:1): pipeline-test\n",
            encoding="utf-8",
        )
        with gzip.open(self.source / "var/log/auth.log.1.gz", "wt", encoding="utf-8") as handle:
            handle.write(f"{day:%b} {day.day:2d} 00:02:00 host sshd[1]: Accepted publickey\n")
        (self.source / "var/log/nginx/access.log").write_text(
            f'client - - [{day:%d/%b/%Y}:00:03:00 +0000] "GET / HTTP/1.1" 200 10\n',
            encoding="utf-8",
        )
        (self.source / "var/log/nginx/error.log").write_text(
            f"{day:%Y/%m/%d} 00:04:00 [error] pipeline-test\n",
            encoding="utf-8",
        )
        report = self.source / "var/log/observability" / f"daily-ops-audit-{day}.md"
        report.write_text("# Daily audit\n\nPipeline integration test.\n", encoding="utf-8")

    def _write_config(self) -> None:
        ingest = self.config / "compliance-archive.env"
        ingest.write_text(
            "COMPLIANCE_INGEST_URL=https://worker.invalid\n"
            "COMPLIANCE_INGEST_TOKEN=test-ingest-token\n",
            encoding="utf-8",
        )
        verify = self.config / "compliance-archive-verify.env"
        verify.write_text(
            "R2_BUCKET=test-bucket\n"
            "R2_ENDPOINT=https://example.invalid\n"
            "R2_PREFIX=\n"
            "R2_ACCESS_KEY_ID=test-read-key\n"
            "R2_SECRET_ACCESS_KEY=test-read-secret\n",
            encoding="utf-8",
        )
        ingest.chmod(0o600)
        verify.chmod(0o600)

    def _write_fake_commands(self) -> None:
        (self.fake_bin / "flock").write_text("#!/usr/bin/env bash\nexit 0\n", encoding="utf-8")
        (self.fake_bin / "rclone").write_text(
            """#!/usr/bin/env bash
set -euo pipefail
if [ "${1:-}" = "--config" ]; then shift 2; fi
action="$1"
shift
remote_path() {
  local relative="${1#*:}"
  relative="${relative#*/}"
  printf '%s/%s' "$FAKE_R2_ROOT" "$relative"
}
case "$action" in
  lsf)
    [ "${FAKE_RCLONE_LSF_FAIL:-0}" -eq 0 ] || exit 41
    source="$(remote_path "$1")"
    [ -d "$source" ] || exit 0
    cd "$source"
    find . -type f -name manifest.json -print | sed 's#^./##' | sort
    ;;
  copyto)
    source="$(remote_path "$1")"
    destination="$2"
    cp "$source" "$destination"
    ;;
  copy)
    source="$(remote_path "$1")"
    destination="$2"
    mkdir -p "$destination"
    cp -R "$source/." "$destination/"
    ;;
  *) exit 2 ;;
esac
""",
            encoding="utf-8",
        )
        (self.fake_bin / "curl").write_text(
            """#!/usr/bin/env bash
set -euo pipefail
data_file=""
expected_sha=""
url=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --data-binary) data_file="${2#@}"; shift 2 ;;
    --header)
      case "$2" in X-Content-SHA256:*) expected_sha="${2#X-Content-SHA256: }" ;; esac
      shift 2
      ;;
    http*) url="$1"; shift ;;
    --request|--retry) shift 2 ;;
    *) shift ;;
  esac
done
[ -n "$data_file" ] && [ -n "$expected_sha" ] && [ -n "$url" ]
key="${url#*/v1/archive/}"
destination="$FAKE_R2_ROOT/$key"
[ ! -e "$destination" ] || exit 22
actual_sha="$(sha256sum "$data_file" | awk '{print $1}')"
[ "$actual_sha" = "$expected_sha" ]
mkdir -p "$(dirname "$destination")"
cp "$data_file" "$destination"
printf '{"stored":true}\n'
""",
            encoding="utf-8",
        )
        for path in self.fake_bin.iterdir():
            path.chmod(0o755)

    def _run_archive(
        self,
        day: dt.date,
        *,
        fail_remote_list: bool = False,
    ) -> subprocess.CompletedProcess[str]:
        command = [
            "docker",
            "run",
            "--rm",
            "-e",
            "PATH=/fake-bin:/usr/local/bin:/usr/bin:/bin",
            "-e",
            "FAKE_R2_ROOT=/remote",
            "-e",
            "COMPLIANCE_ARCHIVE_TIMEOUT_ACTIVE=1",
            "-e",
            f"COMPLIANCE_ARCHIVE_DATE={day}",
            "-e",
            f"FAKE_RCLONE_LSF_FAIL={int(fail_remote_list)}",
            "-e",
            "COMPLIANCE_ARCHIVE_SOURCE_ROOT=/source",
            "-e",
            "COMPLIANCE_ARCHIVE_ROOT=/archive",
            "-e",
            "COMPLIANCE_ARCHIVE_ENV=/config/compliance-archive.env",
            "-e",
            "COMPLIANCE_ARCHIVE_VERIFY_ENV=/config/compliance-archive-verify.env",
            "-e",
            "COMPLIANCE_ARCHIVE_METRIC_OUT=/metrics/compliance.prom",
            "-e",
            "COMPLIANCE_ARCHIVE_LOCK_FILE=/tmp/archive.lock",
            "-e",
            "COMPLIANCE_ARCHIVE_VERIFY_LOCK_FILE=/tmp/verify.lock",
            "-v",
            f"{REPO_ROOT}:/repo:ro",
            "-v",
            f"{self.source}:/source:ro",
            "-v",
            f"{self.archive}:/archive",
            "-v",
            f"{self.remote}:/remote",
            "-v",
            f"{self.metrics}:/metrics",
            "-v",
            f"{self.config}:/config:ro",
            "-v",
            f"{self.fake_bin}:/fake-bin:ro",
            IMAGE,
            "/repo/scripts/backup/archive-compliance-logs.sh",
        ]
        return subprocess.run(command, capture_output=True, text=True, timeout=120)

    def test_two_archives_form_verified_remote_chain(self) -> None:
        first = self._run_archive(self.day)
        self.assertEqual(first.returncode, 0, first.stderr)
        second_day = self.day + dt.timedelta(days=1)
        self._write_sources(second_day)
        second = self._run_archive(second_day)
        self.assertEqual(second.returncode, 0, second.stderr)

        manifests = sorted(self.remote.glob("manifests/LosAngeles/**/manifest.json"))
        payload_parts = sorted(self.remote.glob("payload/LosAngeles/**/payload.tar.gz.part-*"))
        self.assertEqual(len(manifests), 2)
        self.assertGreaterEqual(len(payload_parts), 2)
        metric = (self.metrics / "compliance.prom").read_text(encoding="utf-8")
        self.assertIn("compliance_log_archive_enabled 1", metric)
        self.assertIn("compliance_log_archive_append_only_gateway 1", metric)
        self.assertIn("compliance_log_archive_chain_manifests 2", metric)

    def test_repeated_or_skipped_day_is_rejected_before_upload(self) -> None:
        first = self._run_archive(self.day)
        self.assertEqual(first.returncode, 0, first.stderr)

        repeated = self._run_archive(self.day)
        self.assertNotEqual(repeated.returncode, 0)
        self.assertIn("archive day must continue the remote chain", repeated.stderr)

        skipped = self._run_archive(self.day + dt.timedelta(days=2))
        self.assertNotEqual(skipped.returncode, 0)
        self.assertIn("archive day must continue the remote chain", skipped.stderr)
        self.assertEqual(len(list(self.remote.glob("manifests/LosAngeles/**/manifest.json"))), 1)

    def test_remote_list_failure_is_not_treated_as_an_empty_archive(self) -> None:
        result = self._run_archive(self.day, fail_remote_list=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("failed to list remote compliance manifests", result.stderr)
        self.assertEqual(list(self.remote.glob("manifests/LosAngeles/**/manifest.json")), [])


if __name__ == "__main__":
    unittest.main()
