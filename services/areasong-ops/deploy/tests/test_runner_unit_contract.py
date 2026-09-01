import pathlib
import unittest


DEPLOY_DIR = pathlib.Path(__file__).resolve().parents[1]
RUNNER_UNIT = DEPLOY_DIR / "areasong-ops-runner.service"
PREFLIGHT = DEPLOY_DIR / "preflight.sh"
NGINX_SNIPPET_DIR = "/etc/nginx/snippets/areasong-ops"


class RunnerUnitContractTests(unittest.TestCase):
    def test_runner_unit_allows_only_the_managed_nginx_snippet_directory(self) -> None:
        unit_lines = RUNNER_UNIT.read_text(encoding="utf-8").splitlines()
        nginx_paths = [
            line.removeprefix("ReadWritePaths=")
            for line in unit_lines
            if line.startswith("ReadWritePaths=/etc/nginx")
        ]

        self.assertEqual(nginx_paths, [NGINX_SNIPPET_DIR])

    def test_runtime_preflight_checks_declared_and_effective_write_access(self) -> None:
        preflight = PREFLIGHT.read_text(encoding="utf-8")

        self.assertIn(
            f'require_unit_read_write_path "$UNIT_PATH" {NGINX_SNIPPET_DIR}',
            preflight,
        )
        self.assertIn(
            "require_effective_read_write_path areasong-ops-runner.service "
            f"{NGINX_SNIPPET_DIR}",
            preflight,
        )


if __name__ == "__main__":
    unittest.main()
