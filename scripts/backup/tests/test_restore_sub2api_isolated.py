from __future__ import annotations

import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
SCRIPT = REPO_ROOT / "scripts" / "backup" / "restore-sub2api-isolated.sh"


class RestoreSub2APIIsolatedTests(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.operation = self.root / "operation"
        self.operation.mkdir()
        self.bin_dir = self.root / "bin"
        self.bin_dir.mkdir()
        self.env_file = self.root / ".env"
        self.env_file.write_text(
            "POSTGRES_USER=sub2api\nPOSTGRES_PASSWORD=test-only\nPOSTGRES_DB=sub2api\n",
            encoding="utf-8",
        )
        self.backup_root = self.root / "backups"
        for directory in ("postgres", "redis", "volumes"):
            (self.backup_root / directory).mkdir(parents=True)
        self.backup_scripts = []
        backup_outputs = {
            "postgres.sh": "postgres/sub2api-postgres-test.sql.gz",
            "redis.sh": "redis/redis-test.tar.gz",
            "volumes.sh": "volumes/sub2api-data-test.tar.gz",
        }
        for name, output in backup_outputs.items():
            path = self.root / name
            path.write_text(
                f"#!/bin/sh\n: > \"$BACKUP_ROOT/{output}\"\n",
                encoding="utf-8",
            )
            path.chmod(0o755)
            self.backup_scripts.append(path)
        self._write_fake_id()
        self._write_fake_docker()
        self._write_fake_find()

    def _write_fake_id(self) -> None:
        path = self.bin_dir / "id"
        path.write_text(
            "#!/bin/sh\n"
            "if [ \"${1:-}\" = -u ]; then printf '0\\n'; else exec /usr/bin/id \"$@\"; fi\n",
            encoding="utf-8",
        )
        path.chmod(0o755)

    def _write_fake_docker(self) -> None:
        path = self.bin_dir / "docker"
        path.write_text(
            """#!/usr/bin/env bash
set -eu
target_digest='weishaw/sub2api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
target_id='sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
current_digest='weishaw/sub2api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
current_id='sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
case "${1:-}:${2:-}" in
  pull:*) exit 0 ;;
  image:inspect)
    reference="$3"
    if [[ "$reference" == 'weishaw/sub2api:0.1.173' ]]; then
      jq -cn --arg digest "$target_digest" --arg id "$target_id" '[{Id:$id,RepoDigests:[$digest],Config:{Labels:{"org.opencontainers.image.version":"0.1.173","org.opencontainers.image.revision":"target-commit"}}}]'
    elif [[ "$reference" == "$target_digest" ]]; then
      jq -cn --arg id "$target_id" '[{Id:$id,Config:{Labels:{"org.opencontainers.image.version":"0.1.173","org.opencontainers.image.revision":"target-commit"}}}]'
    elif [[ "$reference" == "$current_digest" ]]; then
      jq -cn --arg id "$current_id" '[{Id:$id,Config:{Labels:{"org.opencontainers.image.version":"0.1.168","org.opencontainers.image.revision":"current-commit"}}}]'
    else
      printf 'unexpected image inspect: %s\n' "$reference" >&2
      exit 1
    fi
    ;;
  inspect:--format)
    container="$4"
    case "$container" in
      sub2api) printf '%s\n' "$current_digest" ;;
      sub2api-postgres) printf '%s\n' 'postgres:18-alpine@sha256:cccc' ;;
      sub2api-redis) printf '%s\n' 'redis:8-alpine@sha256:dddd' ;;
      *) exit 1 ;;
    esac
    ;;
  inspect:sub2api-postgres)
    printf '%s\n' '[{"Config":{"Env":["POSTGRES_USER=sub2api","POSTGRES_DB=sub2api"]}}]'
    ;;
  exec:sub2api-postgres)
    printf '237\n'
    ;;
  *) printf 'unexpected docker invocation: %s\n' "$*" >&2; exit 1 ;;
esac
""",
            encoding="utf-8",
        )
        path.chmod(0o755)

    def _write_fake_find(self) -> None:
        path = self.bin_dir / "find"
        path.write_text(
            """#!/usr/bin/env bash
set -eu
pattern=''
while (( $# > 0 )); do
  if [[ "$1" == -name ]]; then pattern="$2"; break; fi
  shift
done
case "$pattern" in
  'sub2api-postgres-*.sql.gz') file="$BACKUP_ROOT/postgres/sub2api-postgres-test.sql.gz" ;;
  'redis-*.tar.gz') file="$BACKUP_ROOT/redis/redis-test.tar.gz" ;;
  'sub2api-data-*.tar.gz') file="$BACKUP_ROOT/volumes/sub2api-data-test.tar.gz" ;;
  *) exit 1 ;;
esac
printf '9999999999 %s\n' "$file"
""",
            encoding="utf-8",
        )
        path.chmod(0o755)

    def run_preflight(self, target: str) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.bin_dir}:{environment['PATH']}",
                "BACKUP_ROOT": str(self.backup_root),
                "SUB2API_RESTORE_ENV_FILE": str(self.env_file),
                "SUB2API_RESTORE_BACKUP_POSTGRES": str(self.backup_scripts[0]),
                "SUB2API_RESTORE_BACKUP_REDIS": str(self.backup_scripts[1]),
                "SUB2API_RESTORE_BACKUP_VOLUMES": str(self.backup_scripts[2]),
            }
        )
        return subprocess.run(
            [str(SCRIPT), "prepare", "preflight", str(self.operation), target, ""],
            text=True,
            capture_output=True,
            env=environment,
            check=False,
        )

    def run_backup(self) -> subprocess.CompletedProcess[str]:
        environment = os.environ.copy()
        environment.update(
            {
                "PATH": f"{self.bin_dir}:{environment['PATH']}",
                "BACKUP_ROOT": str(self.backup_root),
                "SUB2API_RESTORE_ENV_FILE": str(self.env_file),
                "SUB2API_RESTORE_BACKUP_POSTGRES": str(self.backup_scripts[0]),
                "SUB2API_RESTORE_BACKUP_REDIS": str(self.backup_scripts[1]),
                "SUB2API_RESTORE_BACKUP_VOLUMES": str(self.backup_scripts[2]),
            }
        )
        return subprocess.run(
            [str(SCRIPT), "prepare", "backup", str(self.operation), "v0.1.173", ""],
            text=True,
            capture_output=True,
            env=environment,
            check=False,
        )

    def test_prepare_preflight_pins_target_and_production_baseline(self) -> None:
        result = self.run_preflight("v0.1.173")
        self.assertEqual(result.returncode, 0, result.stderr)
        output = json.loads(result.stdout)
        self.assertTrue(output["ok"])
        self.assertEqual(output["data"]["targetIdentity"]["version"], "0.1.173")
        self.assertEqual(output["data"]["targetIdentity"]["image"], "weishaw/sub2api@sha256:" + "b" * 64)
        state = json.loads((self.operation / "sub2api-drill-state.json").read_text(encoding="utf-8"))
        self.assertEqual(state["target"], "v0.1.173")
        self.assertEqual(state["targetIdentity"]["version"], "0.1.173")
        self.assertEqual(state["current"]["version"], "0.1.168")
        self.assertEqual(state["productionDatabase"], {"user": "sub2api", "database": "sub2api", "migrations": 237})

    def test_backup_returns_valid_empty_data_and_records_fresh_artifacts(self) -> None:
        preflight = self.run_preflight("v0.1.173")
        self.assertEqual(preflight.returncode, 0, preflight.stderr)

        result = self.run_backup()

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(json.loads(result.stdout)["data"], {})
        backups = json.loads((self.operation / "sub2api-drill-backups.json").read_text(encoding="utf-8"))
        self.assertEqual(backups["schemaVersion"], 1)
        self.assertTrue(backups["postgres"]["path"].endswith("sub2api-postgres-test.sql.gz"))
        self.assertTrue(backups["redis"]["path"].endswith("redis-test.tar.gz"))
        self.assertTrue(backups["data"]["path"].endswith("sub2api-data-test.tar.gz"))

    def test_prepare_preflight_rejects_invalid_release_tag(self) -> None:
        result = self.run_preflight("latest")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("target release tag is invalid", result.stderr)

    def test_drill_uses_postgres_18_volume_layout_and_retains_diagnostics(self) -> None:
        script = SCRIPT.read_text(encoding="utf-8")

        self.assertIn("pgdata:/var/lib/postgresql\n", script)
        self.assertNotIn("pgdata:/var/lib/postgresql/data", script)
        self.assertIn('isolated-compose.template.yml', script)
        self.assertIn('capture_diagnostics postgres "$postgres_id"', script)
        self.assertIn('capture_diagnostics redis "$redis_id"', script)
        self.assertIn('capture_diagnostics target-app "$app_id"', script)
        self.assertIn('capture_diagnostics old-app "$app_id"', script)


if __name__ == "__main__":
    unittest.main()
