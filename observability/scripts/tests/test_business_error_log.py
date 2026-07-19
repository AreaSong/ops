from __future__ import annotations

import importlib.util
import unittest
from pathlib import Path


MODULE_PATH = Path(__file__).resolve().parents[1] / "business_error_log.py"
SPEC = importlib.util.spec_from_file_location("business_error_log", MODULE_PATH)
assert SPEC and SPEC.loader
business_error_log = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(business_error_log)


class BusinessErrorLogTests(unittest.TestCase):
    def test_redacts_sensitive_values(self) -> None:
        value = "abcdef0123456789" + "abcdef0123456789"
        message = (
            "ERROR user@example.com from 203.0.113.9 "
            f"token={value} url=https://example.test/?code=secret"
        )
        redacted = business_error_log.redact(message)
        self.assertNotIn("user@example.com", redacted)
        self.assertNotIn("203.0.113.9", redacted)
        self.assertNotIn(value, redacted)
        self.assertNotIn("code=secret", redacted)

    def test_redacts_ipv6_uuid_jwt_and_unkeyed_opaque_tokens(self) -> None:
        uuid = "550e8400-e29b-41d4-a716-446655440000"
        jwt = ".".join(
            ("eyJhbGciOiJIUzI1NiJ9", "eyJzdWIiOiIxMjM0NTY3ODkwIn0", "signature0123456789")
        )
        opaque = "A9zYxWvU7tSrQpOnMlKjIhGfEdCbA1234567890_"
        message = f"ERROR client=2001:db8::1 request={uuid} auth={jwt} opaque {opaque}"
        redacted = business_error_log.redact(message)
        self.assertNotIn("2001:db8::1", redacted)
        self.assertNotIn(uuid, redacted)
        self.assertNotIn(jwt, redacted)
        self.assertNotIn(opaque, redacted)

    def test_redacts_identity_values_in_json_style_payloads(self) -> None:
        message = (
            'ERROR payload={"user_id":"550e8400-e29b-41d4-a716-446655440000",'
            '"session":"session-value-123456789","phone":"+1-202-555-0199"}'
        )
        redacted = business_error_log.redact(message)
        self.assertNotIn("550e8400-e29b-41d4-a716-446655440000", redacted)
        self.assertNotIn("session-value-123456789", redacted)
        self.assertNotIn("+1-202-555-0199", redacted)

    def test_process_lines_filters_and_advances_cursor(self) -> None:
        raw = "\n".join(
            [
                "2026-07-18T01:00:01.000000000Z request completed",
                "2026-07-18T01:00:02.000000000Z WARNING retry failed for user@example.com",
                "2026-07-18T01:00:03.000000000Z healthy request",
            ]
        )
        records, newest, matched = business_error_log.process_lines(
            "account-vault", raw, "2026-07-18T01:00:00.000000000Z"
        )
        self.assertEqual(matched, 1)
        self.assertEqual(len(records), 1)
        self.assertEqual(newest, "2026-07-18T01:00:03.000000000Z")
        self.assertNotIn("user@example.com", records[0])

    def test_old_lines_are_not_repeated(self) -> None:
        records, newest, matched = business_error_log.process_lines(
            "sub2api",
            "2026-07-18T01:00:00.000000000Z ERROR old event",
            "2026-07-18T01:00:00.000000000Z",
        )
        self.assertEqual(records, [])
        self.assertEqual(matched, 0)
        self.assertEqual(newest, "2026-07-18T01:00:00.000000000Z")


if __name__ == "__main__":
    unittest.main()
