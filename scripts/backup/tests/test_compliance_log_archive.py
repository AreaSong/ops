from __future__ import annotations

import argparse
import datetime as dt
import gzip
import hashlib
import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from scripts.backup import compliance_log_archive

UTC = dt.timezone.utc


class ComplianceLogArchiveTests(unittest.TestCase):
    def setUp(self) -> None:
        self.timezone_patch = mock.patch.object(compliance_log_archive, "LOCAL_TZ", UTC)
        self.timezone_patch.start()
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.source = self.root / "source"
        self.output = self.root / "output"
        self.day = dt.date(2026, 7, 15)
        for path in (
            self.source / "var/log/audit",
            self.source / "var/log/nginx",
            self.source / "var/log/observability",
        ):
            path.mkdir(parents=True)
        self._write_sources(self.day)

    def tearDown(self) -> None:
        self.timezone_patch.stop()
        self.temporary.cleanup()

    def _write_sources(self, day: dt.date) -> None:
        start = dt.datetime.combine(day, dt.time.min, UTC)
        audit = self.source / "var/log/audit/audit.log"
        audit.write_text(
            f"type=USER msg=audit({start.timestamp() + 3600:.3f}:1): in-range\n"
            f"type=USER msg=audit({start.timestamp() - 1:.3f}:2): out-of-range\n",
            encoding="utf-8",
        )
        with gzip.open(self.source / "var/log/auth.log.1.gz", "wt", encoding="utf-8") as handle:
            handle.write(f"{day:%b} {day.day:2d} 02:03:04 host sshd[1]: Accepted publickey\n")
            handle.write("Jan  1 00:00:00 host sshd[2]: old\n")
        (self.source / "var/log/nginx/access.log").write_text(
            f'client - - [{day:%d/%b/%Y}:03:04:05 +0000] "GET / HTTP/1.1" 200 10\n'
            'client - - [01/Jan/2026:03:04:05 +0000] "GET /old HTTP/1.1" 200 10\n',
            encoding="utf-8",
        )
        (self.source / "var/log/nginx/error.log").write_text(
            f"{day:%Y/%m/%d} 04:05:06 [error] 1#1: in-range\n"
            "2026/01/01 04:05:06 [error] 1#1: old\n",
            encoding="utf-8",
        )
        report = self.source / "var/log/observability" / f"daily-ops-audit-{day}.md"
        report.write_text(f"# Daily audit {day}\n\nAll source summaries.\n", encoding="utf-8")

    def _build(
        self,
        day: dt.date | None = None,
        previous: str = compliance_log_archive.ZERO_SHA256,
        archive_id: str = "20260715-003500000000Z-0123abcd",
    ) -> Path:
        selected_day = day or self.day
        args = argparse.Namespace(
            date=selected_day.isoformat(),
            source_root=str(self.source),
            output_root=str(self.output),
            host="LosAngeles",
            previous_manifest_sha256=previous,
            created_at=f"{selected_day}T00:35:00+00:00",
            archive_id=archive_id,
            part_size_bytes=128,
        )
        return compliance_log_archive.build_archive(args)

    def test_build_filters_one_utc_day_and_verifies_parts(self) -> None:
        archive_dir = self._build()
        manifest = compliance_log_archive.verify_archive_dir(archive_dir)

        self.assertEqual(manifest["host"], "LosAngeles")
        self.assertEqual(manifest["day"], self.day.isoformat())
        self.assertGreater(len(manifest["payload"]["parts"]), 1)
        self.assertEqual(manifest["sources"]["auditd"]["records"], 1)
        self.assertEqual(manifest["sources"]["auth"]["records"], 1)
        self.assertEqual(manifest["sources"]["nginx-access"]["records"], 1)
        self.assertEqual(manifest["sources"]["nginx-error"]["records"], 1)
        self.assertEqual(len(manifest["payload"]["files"]), 5)

    def test_corrupt_part_is_rejected(self) -> None:
        archive_dir = self._build()
        manifest = json.loads((archive_dir / "manifest.json").read_text(encoding="utf-8"))
        part = archive_dir / manifest["payload"]["parts"][0]["path"]
        content = bytearray(part.read_bytes())
        content[0] ^= 1
        part.write_bytes(content)
        with self.assertRaisesRegex(ValueError, "part verification failed"):
            compliance_log_archive.verify_archive_dir(archive_dir)

    def test_manifest_chain_detects_validly_rehashed_break(self) -> None:
        first = self._build()
        first_sha = compliance_log_archive.sha256_file(first / "manifest.json")
        second_day = self.day + dt.timedelta(days=1)
        self._write_sources(second_day)
        second = self._build(
            day=second_day,
            previous=first_sha,
            archive_id="20260716-003500000000Z-89abcdef",
        )
        self.assertEqual(compliance_log_archive.verify_chain(self.output), 2)

        manifest_path = second / "manifest.json"
        payload = json.loads(manifest_path.read_text(encoding="utf-8"))
        payload["previous_manifest_sha256"] = compliance_log_archive.ZERO_SHA256
        content = (json.dumps(payload, indent=2, sort_keys=True) + "\n").encode()
        manifest_path.write_bytes(content)
        (second / "manifest.json.sha256").write_text(
            f"{hashlib.sha256(content).hexdigest()}  manifest.json\n",
            encoding="utf-8",
        )
        with self.assertRaisesRegex(ValueError, "hash chain is broken"):
            compliance_log_archive.verify_chain(self.output)

    def test_manifest_chain_rejects_a_skipped_day(self) -> None:
        first = self._build()
        first_sha = compliance_log_archive.sha256_file(first / "manifest.json")
        third_day = self.day + dt.timedelta(days=2)
        self._write_sources(third_day)
        self._build(
            day=third_day,
            previous=first_sha,
            archive_id="20260717-003500000000Z-89abcdef",
        )

        with self.assertRaisesRegex(ValueError, "archive day gap"):
            compliance_log_archive.verify_chain(self.output)

    def test_manifest_chain_rejects_duplicate_days(self) -> None:
        first = self._build()
        first_sha = compliance_log_archive.sha256_file(first / "manifest.json")
        self._build(
            previous=first_sha,
            archive_id="20260715-013500000000Z-89abcdef",
        )

        with self.assertRaisesRegex(ValueError, "duplicate compliance archive day"):
            compliance_log_archive.verify_chain(self.output)

    def test_manifest_chain_rejects_path_identity_mismatch(self) -> None:
        archive_dir = self._build()
        manifest_path = archive_dir / "manifest.json"
        payload = json.loads(manifest_path.read_text(encoding="utf-8"))
        payload["archive_id"] = "20260715-013500000000Z-89abcdef"
        content = (json.dumps(payload, indent=2, sort_keys=True) + "\n").encode()
        manifest_path.write_bytes(content)
        (archive_dir / "manifest.json.sha256").write_text(
            f"{hashlib.sha256(content).hexdigest()}  manifest.json\n",
            encoding="utf-8",
        )

        with self.assertRaisesRegex(ValueError, "archive id does not match its path"):
            compliance_log_archive.verify_chain(self.output)

    def test_uncompressed_source_limit_blocks_gzip_expansion(self) -> None:
        auth_log = self.source / "var/log/auth.log.1.gz"
        with gzip.open(auth_log, "wt", encoding="utf-8") as handle:
            handle.write("x" * 2048)

        with (
            mock.patch.object(compliance_log_archive, "MAX_SOURCE_FILE_BYTES", 1024),
            mock.patch.object(compliance_log_archive, "MAX_SOURCE_TOTAL_BYTES", 4096),
            mock.patch.object(compliance_log_archive, "READ_CHUNK_BYTES", 128),
        ):
            with self.assertRaisesRegex(ValueError, "uncompressed log source exceeds"):
                self._build()

    def test_timezone_less_logs_use_the_host_timezone(self) -> None:
        local_timezone = dt.timezone(dt.timedelta(hours=8))
        with mock.patch.object(compliance_log_archive, "LOCAL_TZ", local_timezone):
            auth = compliance_log_archive.parse_auth_timestamp(
                "Jul 15 08:00:00 host sshd[1]: Accepted publickey",
                self.day,
            )
            nginx = compliance_log_archive.parse_nginx_error_timestamp(
                "2026/07/15 08:00:00 [error] test",
                self.day,
            )

        self.assertEqual(auth, dt.datetime(2026, 7, 15, 0, 0, tzinfo=UTC))
        self.assertEqual(nginx, dt.datetime(2026, 7, 15, 0, 0, tzinfo=UTC))

    def test_missing_daily_report_blocks_archive(self) -> None:
        report = self.source / "var/log/observability" / f"daily-ops-audit-{self.day}.md"
        report.unlink()
        with self.assertRaisesRegex(ValueError, "daily operations report is missing"):
            self._build()


if __name__ == "__main__":
    unittest.main()
