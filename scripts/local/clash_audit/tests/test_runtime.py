import http.client
import json
import socket
import stat
import unittest
from types import SimpleNamespace
from unittest.mock import MagicMock, patch

from support import SECRET, SENSITIVE
from runtime import ENDPOINTS, UnixConnection, get_json, process_socket, read_runtime, validate_socket, trace
from source import AuditError


class RuntimeBoundaryTests(unittest.TestCase):
    def test_four_gets_only_credentials_in_memory(self):
        connection = MagicMock()
        response = connection.getresponse.return_value
        response.status = 200
        response.read.return_value = b'{}'
        with patch("runtime.validate_socket"), patch("runtime.process_socket", return_value="/tmp/mock.sock"), \
                patch("runtime.UnixConnection", return_value=connection):
            result = read_runtime({"secret": SECRET})
        self.assertEqual(set(result), set(ENDPOINTS))
        self.assertEqual([c.args[:2] for c in connection.request.call_args_list], [("GET", x) for x in ENDPOINTS])
        for call in connection.request.call_args_list:
            self.assertEqual(call.kwargs["headers"], {"Authorization": "Bearer " + SECRET})
        self.assertEqual(connection.close.call_count, 4)

    def test_authentication_failure_does_not_retry_or_leak(self):
        for status in (401, 403):
            connection = MagicMock()
            connection.getresponse.return_value.status = status
            connection.getresponse.return_value.read.return_value = json.dumps(dict(zip(("secret", "uuid", "password", "url"), SENSITIVE))).encode()
            with patch("runtime.validate_socket"), patch("runtime.UnixConnection", return_value=connection):
                with self.assertRaises(AuditError) as caught:
                    read_runtime({"secret": SECRET}, "/tmp/mock.sock")
            self.assertEqual(caught.exception.code, "runtime_auth")
            self.assertEqual(connection.request.call_count, 1)
            connection.getresponse.return_value.read.assert_not_called()
            for token in SENSITIVE:
                self.assertNotIn(token, str(caught.exception))

    def test_http_redirect_not_followed(self):
        connection = MagicMock()
        connection.getresponse.return_value.status = 302
        with patch("runtime.UnixConnection", return_value=connection), self.assertRaises(AuditError) as caught:
            get_json("/tmp/mock.sock", "/configs", "")
        self.assertEqual(caught.exception.code, "runtime_http")
        self.assertEqual(connection.request.call_count, 1)

    def test_socket_unavailable_and_timeout_sanitized(self):
        for error in (PermissionError(SECRET), socket.timeout(SECRET), http.client.BadStatusLine(SECRET)):
            connection = MagicMock()
            connection.request.side_effect = error
            with patch("runtime.UnixConnection", return_value=connection), self.assertRaises(AuditError) as caught:
                get_json("/tmp/mock.sock", "/configs", SECRET)
            self.assertEqual(caught.exception.code, "runtime_unavailable")
            self.assertNotIn(SECRET, str(caught.exception))
            connection.close.assert_called_once()

    def test_response_json_shape_and_size(self):
        for payload, code in ((b'[]', "runtime_shape"), (SECRET.encode(), "runtime_json"), (b'x' * (16 * 1024 * 1024 + 1), "runtime_size")):
            connection = MagicMock()
            connection.getresponse.return_value.status = 200
            connection.getresponse.return_value.read.return_value = payload
            with patch("runtime.UnixConnection", return_value=connection), self.assertRaises(AuditError) as caught:
                get_json("/tmp/mock.sock", "/rules", SECRET)
            self.assertEqual(caught.exception.code, code)
            self.assertNotIn(SECRET, str(caught.exception))

    def test_only_unix_connection(self):
        with patch("runtime.socket.socket") as factory:
            connection = UnixConnection("/tmp/mock.sock")
            connection.connect()
            factory.assert_called_once_with(socket.AF_UNIX, socket.SOCK_STREAM)
            factory.return_value.connect.assert_called_once_with("/tmp/mock.sock")
            connection.close()

    def test_reject_remote_path_link_and_missing_socket(self):
        for path in ("https://remote.invalid/control", "127.0.0.1:9090", "relative.sock"):
            with self.assertRaises(AuditError):
                validate_socket(path)
        for mode in (stat.S_IFREG, stat.S_IFLNK):
            with patch("runtime.Path.lstat", return_value=SimpleNamespace(st_mode=mode)), self.assertRaises(AuditError):
                validate_socket("/tmp/mock.sock")
        with patch("runtime.Path.lstat", side_effect=FileNotFoundError(SECRET)), self.assertRaises(AuditError) as caught:
            validate_socket("/tmp/mock.sock")
        self.assertEqual(caught.exception.code, "socket_unavailable")

    def test_process_discovery_uses_current_flags_with_spaces(self):
        responses = [SimpleNamespace(stdout=" 123 /Library/Application Support/service/cores/verge-mihomo\n"),
                     SimpleNamespace(stdout="/Library/Application Support/service/cores/verge-mihomo -d /new runtime -f /new runtime/config.yaml -ext-ctl-unix /new socket/core.sock")]
        with patch("runtime.subprocess.run", side_effect=responses) as process:
            self.assertEqual(process_socket(), "/new socket/core.sock")
        self.assertEqual(process.call_count, 2)
        self.assertNotIn(SECRET, repr(process.call_args_list))

    def test_process_discovery_refuses_ambiguity_and_gui_fallback(self):
        for listing in ("", "1 /bin/mihomo\n2 /bin/verge-mihomo\n"):
            with patch("runtime.subprocess.run", return_value=SimpleNamespace(stdout=listing)), self.assertRaises(AuditError):
                process_socket()
        responses = [SimpleNamespace(stdout="1 /bin/mihomo"), SimpleNamespace(stdout="/bin/mihomo -f /tmp/gui.yaml")]
        with patch("runtime.subprocess.run", side_effect=responses), self.assertRaises(AuditError):
            process_socket()

    def test_reject_non_whitelist_endpoint(self):
        with self.assertRaises(AuditError):
            get_json("/tmp/mock.sock", "/connections", SECRET)

    def test_invalid_auth_not_sent(self):
        with patch("runtime.validate_socket"), patch("runtime.get_json") as request:
            with self.assertRaises(AuditError):
                read_runtime({"secret": "bad\nheader"}, "/tmp/mock.sock")
        request.assert_not_called()

    def test_unresolved_proxy_types_and_dialer_not_claimed(self):
        for node in ({"type": "LoadBalance"}, {"type": "Relay"}, {"type": "VMess", "dialer-proxy": "other"}):
            with self.assertRaises(AuditError) as caught:
                trace({"node": node}, "node")
            self.assertEqual(caught.exception.code, "selection_unverified")


if __name__ == "__main__":
    unittest.main()
