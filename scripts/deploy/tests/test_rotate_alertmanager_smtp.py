from __future__ import annotations

import importlib.util
import os
import stat
import tempfile
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).resolve().parents[1] / "rotate-alertmanager-smtp.py"
SPEC = importlib.util.spec_from_file_location("rotate_alertmanager_smtp", MODULE_PATH)
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class FakeSMTP:
    instances: list["FakeSMTP"] = []

    def __init__(self, host: str, port: int, timeout: int) -> None:
        self.host = host
        self.port = port
        self.timeout = timeout
        self.calls: list[object] = []
        self.__class__.instances.append(self)

    def __enter__(self) -> "FakeSMTP":
        return self

    def __exit__(self, *_args: object) -> None:
        return None

    def ehlo(self) -> None:
        self.calls.append("ehlo")

    def starttls(self, context: object) -> None:
        self.calls.append(("starttls", context is not None))

    def login(self, username: str, authorization_code: str) -> None:
        self.calls.append(("login", username, authorization_code))

    def send_message(self, message: object) -> None:
        self.calls.append(("send_message", message))


class RotateAlertmanagerSmtpTests(unittest.TestCase):
    def setUp(self) -> None:
        FakeSMTP.instances.clear()

    def test_authorization_code_rejects_whitespace_and_short_values(self) -> None:
        with self.assertRaises(ValueError):
            MODULE.validate_authorization_code("short")
        with self.assertRaises(ValueError):
            MODULE.validate_authorization_code("abcd efgh ijkl mnop")
        self.assertEqual(
            MODULE.validate_authorization_code("abcdefghijklmnop"),
            "abcdefghijklmnop",
        )

    def test_smtp_verification_uses_starttls_before_login_and_sends_chinese_message(self) -> None:
        MODULE.verify_smtp_authorization(
            "smtp.example.com",
            587,
            "sender@example.com",
            "abcdefghijklmnop",
            "receiver@example.com",
            smtp_factory=FakeSMTP,
        )
        client = FakeSMTP.instances[0]
        self.assertEqual(client.calls[0], "ehlo")
        self.assertEqual(client.calls[1][0], "starttls")
        self.assertEqual(client.calls[2], "ehlo")
        self.assertEqual(
            client.calls[3],
            ("login", "sender@example.com", "abcdefghijklmnop"),
        )
        message = client.calls[4][1]
        self.assertIn("轮换验证", str(message["Subject"]))
        self.assertIn("STARTTLS", message.get_content())

    def test_rotate_and_restore_preserve_target_mode_without_logging_secret(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            credential = root / "smtp-password"
            credential.write_text("old-auth-code-12\n", encoding="utf-8")
            credential.chmod(0o440)
            current_uid = os.getuid()

            backup = MODULE.rotate_credential(
                credential,
                root / "backups",
                "abcdefghijklmnop",
                expected_uid=current_uid,
            )

            self.assertEqual(credential.read_text(encoding="utf-8"), "abcdefghijklmnop\n")
            self.assertEqual(stat.S_IMODE(credential.stat().st_mode), 0o440)
            self.assertEqual(backup.read_text(encoding="utf-8"), "old-auth-code-12\n")
            self.assertEqual(stat.S_IMODE(backup.stat().st_mode), 0o600)

            MODULE.restore_credential(credential, backup, expected_uid=current_uid)
            self.assertEqual(credential.read_text(encoding="utf-8"), "old-auth-code-12\n")
            self.assertEqual(stat.S_IMODE(credential.stat().st_mode), 0o440)


if __name__ == "__main__":
    unittest.main()
