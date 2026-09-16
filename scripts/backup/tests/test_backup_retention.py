from __future__ import annotations

import os
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest
from contextlib import contextmanager
from pathlib import Path

import test_backup_configs as configs
import test_backup_redis as redis
import test_backup_volumes as volumes


BACKUP_DIR = Path(__file__).resolve().parents[1]
EXPIRED_NS = 946684800_000000000
PAYLOAD = b"existing backup must survive\n"
JOBS = ("postgres", "redis", "configs", "volumes")


class PostgresFixture:
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.backups = Path("/var/backups/ops/postgres")
        self.backups.mkdir(mode=0o700, parents=True, exist_ok=True)
        if any(self.backups.iterdir()):
            raise RuntimeError("PostgreSQL 夹具只接受空的私有临时备份目录")
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        docker = self.bin_dir / "docker"
        docker.write_text(
            "#!/usr/bin/env python3\n"
            "import sys\n"
            "args = sys.argv[1:]\n"
            "if args[:2] == ['ps', '--format']:\n"
            "    print('sub2api-postgres\\naccount-vault-postgres-1\\nareaforge-postgres')\n"
            "elif args[:1] == ['exec']:\n"
            "    print('-- isolated pg_dumpall fixture\\nSELECT 1;')\n"
            "else:\n"
            "    raise SystemExit(2)\n",
            encoding="utf-8",
        )
        docker.chmod(0o755)

    def tearDown(self) -> None:
        # 固定路径由 setUpClass 证明位于容器私有 tmpfs；不允许接触宿主备份。
        for path in self.backups.iterdir():
            if path.is_symlink() or not path.is_file():
                raise RuntimeError("私有 PostgreSQL 夹具出现非预期成员")
            path.unlink()
        self.temporary.cleanup()


class BackupRetentionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        if os.environ.get("OPS_BACKUP_RETENTION_TEST") != "1":
            raise unittest.SkipTest("仅在显式启用的离线 Linux 容器中运行")
        if sys.platform != "linux" or os.geteuid() != 0 or not Path("/.dockerenv").exists():
            raise RuntimeError("必须使用无网络 Linux root 测试容器")
        mounts = [line.split() for line in Path("/proc/mounts").read_text().splitlines()]
        if not any(row[1:3] == ["/var/backups/ops", "tmpfs"] for row in mounts):
            raise RuntimeError("/var/backups/ops 必须是独立 tmpfs，不能是宿主目录")
        if Path("/var/run/docker.sock").exists():
            raise RuntimeError("测试容器不得挂载 Docker Socket")
        if {path.name for path in Path("/sys/class/net").iterdir()} != {"lo"}:
            raise RuntimeError("测试容器必须使用 network none")

    @contextmanager
    def fixture(self, job: str):
        factories = {
            "postgres": PostgresFixture,
            "redis": redis.RedisBackupTests,
            "configs": configs.BackupConfigsTests,
            "volumes": volumes.BackupVolumesTests,
        }
        fixture = factories[job]()
        fixture.setUp()
        try:
            yield fixture
        finally:
            fixture.tearDown()

    @staticmethod
    def source_environment(job: str, fixture) -> tuple[Path, dict[str, str]]:
        if job == "postgres":
            return fixture.backups, {}
        if job == "configs":
            return fixture.backups, {
                "BACKUP_CONFIG_SOURCE_ROOT": str(fixture.source),
                "BACKUP_CONFIG_BACKUP_ROOT": str(fixture.backups),
                "BACKUP_CONFIG_LOG_DIR": str(fixture.root / "logs"),
            }
        if job == "redis":
            return fixture.backup_root, {
                "REDIS_BACKUP_ROOT": str(fixture.backup_root),
                "REDIS_DATA_DIR": str(fixture.data_dir),
                "BACKUP_LOG_DIR": str(fixture.root / "logs"),
                "FAKE_REDIS_RDB": str(fixture.rdb),
                "FAKE_REDIS_STATE": str(fixture.state),
            }
        return fixture.root / "backups", {
            "BACKUP_VOLUME_BACKUP_ROOT": str(fixture.root / "backups"),
            "BACKUP_VOLUME_LOG_DIR": str(fixture.root / "logs"),
            "BACKUP_AREASONG_OPS_STATE_ROOT": str(fixture.state),
            "BACKUP_AREASONG_OPS_SNAPSHOT_MAX_AGE_SECONDS": "90000",
        }

    @staticmethod
    def old_file(path: Path, *, expired: bool = True) -> None:
        path.write_bytes(PAYLOAD)
        path.chmod(0o600)
        if expired:
            os.utime(path, ns=(EXPIRED_NS, EXPIRED_NS))

    @staticmethod
    def state(path: Path) -> tuple[bytes, int, int]:
        info = path.stat()
        return path.read_bytes(), info.st_mtime_ns, stat.S_IMODE(info.st_mode)

    @staticmethod
    def archive_interceptor(directory: Path, job: str, scenario: str, arriving: Path) -> None:
        name = "gzip" if job == "postgres" else "tar"
        executable = shutil.which(name)
        if executable is None:
            raise RuntimeError(f"missing fixture dependency: {name}")
        condition = "'-c' in sys.argv[1:]" if name == "gzip" else "'-czf' in sys.argv[1:]"
        script = directory / name
        script.write_text(
            "#!/usr/bin/env python3\n"
            "import os, sys\n"
            "from pathlib import Path\n"
            f"creating = {condition}\n"
            + "if creating:\n"
            + (f"    path = Path({str(arriving)!r})\n"
               f"    path.write_bytes({PAYLOAD!r})\n"
               "    path.chmod(0o600)\n"
               f"    os.utime(path, ns=({EXPIRED_NS}, {EXPIRED_NS}))\n"
               if scenario in {"arriving", "expiring"} else "    pass\n")
            + ("    raise SystemExit(73)\n" if scenario == "failure" else "")
            + f"os.execv({executable!r}, [{executable!r}, *sys.argv[1:]])\n",
            encoding="utf-8",
        )
        script.chmod(0o755)

    def exercise(self, job: str, scenario: str) -> None:
        with self.fixture(job) as fixture:
            backups, overrides = self.source_environment(job, fixture)
            backups.mkdir(parents=True, exist_ok=True)
            suffix = ".sql.gz" if job == "postgres" else ".tar.gz"
            prefix = "sub2api-postgres" if job == "postgres" else job
            old = backups / f"{prefix}-20000101-000000{suffix}"
            arriving = backups / f"{prefix}-20000102-000000{suffix}"
            self.old_file(old, expired=scenario not in {"arriving", "expiring"})
            before = self.state(old)
            if scenario == "expiring":
                # 归档期间改变夹具 mtime，确定性模拟进入过期范围，不修改系统时钟。
                self.old_file(arriving, expired=False)
                self.assertGreater(arriving.stat().st_mtime_ns, EXPIRED_NS)
            else:
                self.assertFalse(arriving.exists())
            bin_dir = fixture.root / "retention-bin"
            bin_dir.mkdir()
            self.archive_interceptor(bin_dir, job, scenario, arriving)
            result = self.run_backup(job, fixture, overrides, bin_dir)
            self.assertEqual(result.returncode == 0, scenario != "failure", result.stdout + result.stderr)
            self.assertTrue(old.exists(), "备份任务删除了既有过期产物")
            self.assertEqual(self.state(old), before)
            if scenario in {"arriving", "expiring"}:
                self.assertTrue(arriving.exists(), "执行期间进入清理范围的产物被删除")
                self.assertEqual(self.state(arriving), (PAYLOAD, EXPIRED_NS, 0o600))
            self.check_result(job, fixture, backups, result, success=scenario != "failure")

    @staticmethod
    def run_backup(job: str, fixture, overrides: dict, bin_dir: Path):
        temporary = fixture.root / "tmp"
        temporary.mkdir(exist_ok=True)
        commands = getattr(fixture, "bin_dir", getattr(fixture, "fake_bin", fixture.root))
        environment = {
            **os.environ,
            **overrides,
            "PATH": f"{bin_dir}:{commands}:{os.environ['PATH']}",
            "TMPDIR": str(temporary),
            "OPS_BACKUP_JOB_WRAPPED": "0",
            "BACKUP_JOB_METRIC_DIR": str(fixture.root / "metrics"),
            "BACKUP_JOB_LOCK_DIR": str(fixture.root / "locks"),
            "BACKUP_JOB_TIMEOUT_SECONDS": "15",
        }
        return subprocess.run(
            [str(BACKUP_DIR / f"backup-{job}.sh")],
            env=environment, text=True, capture_output=True, check=False, timeout=25,
        )

    def check_result(self, job: str, fixture, backups: Path, result, *, success: bool) -> None:
        metric = (fixture.root / "metrics" / f"backup-job-{job}.prom").read_text()
        self.assertIn(f'backup_job_last_result{{backup_job="{job}"}} {int(success)}', metric)
        self.assertEqual(list((fixture.root / "tmp").iterdir()), [])
        if success:
            paths = [Path(line) for line in result.stdout.splitlines() if line]
            self.assertEqual(len(paths), 3 if job == "postgres" else 1)
            for path in paths:
                self.assertEqual(path.parent, backups)
                self.assertTrue(path.is_file())
                self.assertGreater(path.stat().st_size, 0)
                self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_existing_backups_survive_success(self) -> None:
        for job in JOBS:
            with self.subTest(job=job):
                self.exercise(job, "success")

    def test_existing_backups_survive_failure(self) -> None:
        for job in JOBS:
            with self.subTest(job=job):
                self.exercise(job, "failure")

    def test_expired_artifacts_arriving_during_backup_survive(self) -> None:
        for job in JOBS:
            with self.subTest(job=job):
                self.exercise(job, "arriving")

    def test_artifacts_expiring_during_backup_survive(self) -> None:
        for job in JOBS:
            with self.subTest(job=job):
                self.exercise(job, "expiring")


if __name__ == "__main__":
    unittest.main()
