from __future__ import annotations

import os
import shutil
import subprocess
import time
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[3]
GRANTS_SQL = (REPO_ROOT / "scripts" / "deploy" / "account-vault-role-grants.sql").read_text(
    encoding="utf-8"
)
VERIFY_SQL = (REPO_ROOT / "scripts" / "deploy" / "account-vault-role-verify.sql").read_text(
    encoding="utf-8"
)
POSTGRES_IMAGE = (
    "postgres:15-alpine@sha256:cd17e2ac98240fce1541ad2a803b34009b4eea5aec8a832363cdc7eca62e722e"
)


class AccountVaultRolePermissionIntegrationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        if shutil.which("docker") is None:
            raise unittest.SkipTest("docker is required")

    def setUp(self) -> None:
        self.container = f"account-vault-role-test-{os.getpid()}-{time.time_ns()}"
        result = subprocess.run(
            [
                "docker",
                "run",
                "-d",
                "--name",
                self.container,
                "-e",
                "POSTGRES_USER=account_user",
                "-e",
                "POSTGRES_PASSWORD=test-admin-password",
                "-e",
                "POSTGRES_DB=accountvault",
                "--health-cmd",
                "pg_isready -U account_user -d accountvault",
                "--health-interval",
                "1s",
                "--health-timeout",
                "5s",
                "--health-retries",
                "60",
                POSTGRES_IMAGE,
            ],
            text=True,
            capture_output=True,
            check=False,
        )
        self.assertEqual(result.returncode, 0, result.stderr)
        self.addCleanup(self.remove_container)
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            health = subprocess.run(
                ["docker", "inspect", "--format", "{{.State.Health.Status}}", self.container],
                text=True,
                capture_output=True,
                check=False,
            )
            if health.stdout.strip() == "healthy":
                break
            time.sleep(0.5)
        else:
            logs = subprocess.run(
                ["docker", "logs", self.container],
                text=True,
                capture_output=True,
                check=False,
            )
            self.fail(f"PostgreSQL did not become healthy within 90 seconds:\n{logs.stderr}{logs.stdout}")
        ready = self.psql("SELECT 1", check=False)
        self.assertEqual(ready.returncode, 0, ready.stderr)

    def remove_container(self) -> None:
        subprocess.run(
            ["docker", "rm", "-f", self.container],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            check=False,
        )

    def psql(
        self,
        sql: str,
        *,
        user: str = "account_user",
        variables: tuple[str, ...] = (),
        check: bool = True,
    ) -> subprocess.CompletedProcess[str]:
        command = [
            "docker",
            "exec",
            "-i",
            self.container,
            "psql",
            "-v",
            "ON_ERROR_STOP=1",
        ]
        for variable in variables:
            command.extend(("-v", variable))
        command.extend(("-U", user, "-d", "accountvault", "-At", "-f", "-"))
        return subprocess.run(
            command,
            input=sql,
            text=True,
            capture_output=True,
            check=check,
        )

    def test_existing_and_future_tables_are_granted_without_schema_create(self) -> None:
        self.psql(
            "CREATE ROLE account_vault_app LOGIN PASSWORD 'test-app-password';\n"
            "CREATE TABLE existing_record (id integer PRIMARY KEY);\n"
            'CREATE TABLE "_prisma_migrations" (id text PRIMARY KEY);\n'
        )
        variables = ("migration_user=account_user", "app_user=account_vault_app")
        self.psql(GRANTS_SQL, variables=variables)
        self.assertEqual(
            self.psql(VERIFY_SQL, variables=("app_user=account_vault_app",)).stdout.strip(),
            "0|0|false|1|true|false|false|false|false|false|0|false",
        )

        self.psql("CREATE TABLE future_record (id integer PRIMARY KEY);\n")
        self.assertEqual(
            self.psql(VERIFY_SQL, variables=("app_user=account_vault_app",)).stdout.strip(),
            "0|0|false|1|true|false|false|false|false|false|0|false",
        )
        self.psql(
            "INSERT INTO existing_record VALUES (1);\n"
            "UPDATE existing_record SET id = 2 WHERE id = 1;\n"
            "DELETE FROM existing_record WHERE id = 2;\n",
            user="account_vault_app",
        )
        create = self.psql(
            "CREATE TABLE forbidden_record (id integer);\n",
            user="account_vault_app",
            check=False,
        )
        self.assertNotEqual(create.returncode, 0)
        self.assertIn("permission denied for schema public", create.stderr)
        for operation in (
            'INSERT INTO "_prisma_migrations" VALUES (\'forbidden\')',
            'UPDATE "_prisma_migrations" SET id = \'forbidden\'',
            'DELETE FROM "_prisma_migrations"',
        ):
            result = self.psql(operation + ";\n", user="account_vault_app", check=False)
            self.assertNotEqual(result.returncode, 0, operation)


if __name__ == "__main__":
    unittest.main()
