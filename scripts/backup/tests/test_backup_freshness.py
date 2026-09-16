import copy
import datetime as dt
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import backup_freshness as module
import backup_manifest


class BackupFreshnessTests(unittest.TestCase):
    def setUp(self):
        self.now = dt.datetime(2026, 9, 16, 4, tzinfo=dt.timezone.utc)
        self.payload = {
            "created_at": self.now.isoformat(), "artifact_span_seconds": 0,
            "artifacts": [{"role": role, "modified_at": (self.now - dt.timedelta(hours=2)).isoformat()}
                          for role, _, _ in backup_manifest.ARTIFACT_SPECS],
        }

    def test_fresh_complete_set(self):
        module.validate(self.payload, self.now)

    def test_new_manifest_does_not_refresh_old_data(self):
        self.payload["artifacts"][0]["modified_at"] = (self.now - dt.timedelta(days=2)).isoformat()
        with self.assertRaisesRegex(ValueError, "freshness"):
            module.validate(self.payload, self.now)

    def test_old_manifest_is_rejected_even_if_artifacts_are_new(self):
        self.payload["created_at"] = (self.now - dt.timedelta(days=2)).isoformat()
        with self.assertRaisesRegex(ValueError, "stale"):
            module.validate(self.payload, self.now)

    def test_timezone_and_timestamp_are_required(self):
        for value in (None, "invalid", "2026-09-16T04:00:00", 42):
            with self.subTest(value=value), self.assertRaises(ValueError):
                module.timestamp(value)

    def test_future_timestamp_is_rejected(self):
        self.payload["created_at"] = (self.now + dt.timedelta(minutes=2)).isoformat()
        with self.assertRaisesRegex(ValueError, "future"):
            module.validate(self.payload, self.now)

    def test_missing_or_duplicate_role_is_rejected(self):
        self.payload["artifacts"][1] = copy.deepcopy(self.payload["artifacts"][0])
        with self.assertRaisesRegex(ValueError, "roles"):
            module.validate(self.payload, self.now)

    def test_span_and_recorded_span_are_checked(self):
        for recorded in (1, True, None):
            self.payload["artifact_span_seconds"] = recorded
            with self.subTest(recorded=recorded), self.assertRaisesRegex(ValueError, "span"):
                module.validate(self.payload, self.now)

    def test_download_crossing_rpo_boundary_must_be_rechecked(self):
        module.validate(self.payload, self.now)
        with self.assertRaisesRegex(ValueError, "stale"):
            module.validate(self.payload, self.now + dt.timedelta(hours=23))


if __name__ == "__main__":
    unittest.main()
