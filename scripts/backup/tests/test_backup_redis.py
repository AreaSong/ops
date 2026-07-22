from __future__ import annotations

import os
import subprocess
import tarfile
import tempfile
import time
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
BACKUP_SCRIPT = REPO_ROOT / "scripts" / "backup" / "backup-redis.sh"


class RedisBackupTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temp_dir = tempfile.TemporaryDirectory()
        self.root = Path(self.temp_dir.name)
        self.bin_dir = self.root / "bin"
        self.data_dir = self.root / "redis-data"
        self.backup_root = self.root / "backups"
        self.state = self.root / "bgsave-complete"
        self.bin_dir.mkdir()
        self.data_dir.mkdir()
        self.rdb = self.data_dir / "dump.rdb"
        self.rdb.write_bytes(b"old-rdb")
        old = time.time() - 20
        os.utime(self.rdb, (old, old))
        self._write_fake_docker()
        self._write_fake_stat()

    def tearDown(self) -> None:
        self.temp_dir.cleanup()

    def _write_fake_docker(self) -> None:
        docker = self.bin_dir / "docker"
        docker.write_text(
            """#!/usr/bin/env python3
import os
import sys
import time
from pathlib import Path

args = sys.argv[1:]
rdb = Path(os.environ["FAKE_REDIS_RDB"])
state = Path(os.environ["FAKE_REDIS_STATE"])
if args[:2] == ["ps", "--format"]:
    print("sub2api-redis")
elif args[-2:] == ["INFO", "persistence"]:
    in_progress = "1" if os.environ.get("FAKE_REDIS_ALREADY_RUNNING") == "1" else "0"
    last_save = int(time.time()) + 1 if state.exists() else 100
    print(f"rdb_bgsave_in_progress:{in_progress}")
    print("rdb_last_bgsave_status:ok")
    print(f"rdb_last_save_time:{last_save}")
elif args[-1:] == ["BGSAVE"]:
    if os.environ.get("FAKE_REDIS_BGSAVE_FAIL") == "1":
        raise SystemExit(1)
    if os.environ.get("FAKE_REDIS_KEEP_MTIME") != "1":
        rdb.write_bytes(b"new-rdb")
        now = time.time_ns()
        os.utime(rdb, ns=(now, now))
    state.write_text("done\\n", encoding="utf-8")
    print("Background saving started")
else:
    raise SystemExit(f"unexpected docker arguments: {args}")
""",
            encoding="utf-8",
        )
        docker.chmod(0o755)

    def _write_fake_stat(self) -> None:
        stat = self.bin_dir / "stat"
        stat.write_text(
            """#!/usr/bin/env python3
import os
import sys

value = os.stat(sys.argv[-1]).st_mtime_ns
print(value)
""",
            encoding="utf-8",
        )
        stat.chmod(0o755)

    def _run(self, **overrides: str) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.bin_dir}:{environment['PATH']}",
                "OPS_BACKUP_JOB_WRAPPED": "1",
                "REDIS_BACKUP_ROOT": str(self.backup_root),
                "REDIS_DATA_DIR": str(self.data_dir),
                "BACKUP_LOG_DIR": str(self.root / "logs"),
                "FAKE_REDIS_RDB": str(self.rdb),
                "FAKE_REDIS_STATE": str(self.state),
            }
        )
        environment.update(overrides)
        return subprocess.run(
            [str(BACKUP_SCRIPT)],
            text=True,
            capture_output=True,
            check=False,
            env=environment,
            timeout=10,
        )

    def test_success_requires_new_rdb_and_records_redis_evidence(self) -> None:
        result = self._run()
        self.assertEqual(result.returncode, 0, result.stderr)
        archive = next(self.backup_root.glob("redis-*.tar.gz"))
        with tarfile.open(archive, "r:gz") as handle:
            metadata = handle.extractfile("metadata.txt")
            self.assertIsNotNone(metadata)
            content = metadata.read().decode("utf-8")
        self.assertIn("rdb_last_save_time=", content)
        self.assertIn("rdb_mtime=", content)

    def test_bgsave_failure_does_not_create_an_archive(self) -> None:
        result = self._run(FAKE_REDIS_BGSAVE_FAIL="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(list(self.backup_root.glob("*.tar.gz")), [])

    def test_existing_bgsave_is_rejected(self) -> None:
        result = self._run(FAKE_REDIS_ALREADY_RUNNING="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("already in progress", result.stderr)
        self.assertFalse(self.state.exists())

    def test_unchanged_rdb_mtime_is_rejected(self) -> None:
        result = self._run(FAKE_REDIS_KEEP_MTIME="1")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("mtime did not advance", result.stderr)
        self.assertEqual(list(self.backup_root.glob("*.tar.gz")), [])


if __name__ == "__main__":
    unittest.main()
