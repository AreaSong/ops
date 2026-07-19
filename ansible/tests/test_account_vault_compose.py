from __future__ import annotations

import unittest
from pathlib import Path

import yaml


REPO_ROOT = Path(__file__).resolve().parents[2]
COMPOSE_PATH = REPO_ROOT / "services" / "account-vault" / "compose.yml"
ROLE_GRANTS_PATH = REPO_ROOT / "scripts" / "deploy" / "account-vault-role-grants.sql"
ROLE_VERIFY_PATH = REPO_ROOT / "scripts" / "deploy" / "account-vault-role-verify.sql"


class AccountVaultComposeTests(unittest.TestCase):
    def setUp(self) -> None:
        self.services = yaml.safe_load(COMPOSE_PATH.read_text(encoding="utf-8"))["services"]

    def test_runtime_and_migration_do_not_receive_the_whole_secret_file(self) -> None:
        self.assertNotIn("env_file", self.services["web"])
        self.assertNotIn("env_file", self.services["migrate"])
        self.assertEqual(len(self.services["migrate"]["environment"]), 1)
        self.assertTrue(self.services["migrate"]["environment"][0].startswith("DATABASE_URL="))

    def test_migration_uses_management_credentials_and_runtime_uses_app_credentials(self) -> None:
        runtime_url = next(value for value in self.services["web"]["environment"] if value.startswith("DATABASE_URL="))
        migration_url = self.services["migrate"]["environment"][0]
        self.assertIn("${DATABASE_APP_USER:?", runtime_url)
        self.assertIn("${DATABASE_APP_PASSWORD:?", runtime_url)
        self.assertIn("${POSTGRES_USER", migration_url)
        self.assertIn("${POSTGRES_PASSWORD:", migration_url)

    def test_release_services_have_bounded_process_and_filesystem_permissions(self) -> None:
        for name in ("web", "migrate"):
            service = self.services[name]
            self.assertEqual(service["user"], "node")
            self.assertTrue(service["read_only"])
            self.assertEqual(service["cap_drop"], ["ALL"])
            self.assertGreater(service["pids_limit"], 0)

    def test_production_values_fail_closed_during_compose_resolution(self) -> None:
        web_environment = "\n".join(self.services["web"]["environment"])
        for variable in (
            "DATABASE_APP_USER",
            "DATABASE_APP_PASSWORD",
            "RP_ID",
            "ORIGIN",
            "SESSION_SECRET",
            "ENCRYPTION_KEY",
            "REGISTRATION_TOKEN",
        ):
            self.assertIn(f"${{{variable}:?", web_environment)
        self.assertIn("${POSTGRES_PASSWORD:?", "\n".join(self.services["postgres"]["environment"]))

    def test_runtime_role_grants_are_future_safe_and_verifiable(self) -> None:
        grants = ROLE_GRANTS_PATH.read_text(encoding="utf-8")
        verify = ROLE_VERIFY_PATH.read_text(encoding="utf-8")
        self.assertIn("REVOKE CREATE ON SCHEMA public FROM PUBLIC", grants)
        self.assertIn("ALTER DEFAULT PRIVILEGES FOR ROLE", grants)
        self.assertIn("SELECT, INSERT, UPDATE, DELETE", grants)
        self.assertIn("REVOKE INSERT, UPDATE, DELETE", grants)
        self.assertIn("has_table_privilege", verify)
        self.assertIn("has_sequence_privilege", verify)
        self.assertIn("has_schema_privilege", verify)
        self.assertIn("rolsuper", verify)
        self.assertIn("rolcreatedb", verify)
        self.assertIn("rolcreaterole", verify)
        self.assertIn("rolbypassrls", verify)
        self.assertIn("pg_auth_members", verify)


if __name__ == "__main__":
    unittest.main()
