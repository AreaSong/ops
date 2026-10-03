import contextlib
import copy
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import yaml

from support import fixture, write_yaml, snapshot, SECRET, SENSITIVE, PRIMARY, PROTECTED
from clash_audit import main, emit
from source import AuditError


class ContractTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = fixture(self.root)

    def cli(self, *flags):
        output, errors = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            code = main(["--app-dir", str(self.root), "--json", *flags])
        self.assertEqual(errors.getvalue(), "")
        value = json.loads(output.getvalue())
        self.assertEqual(code, value["exit_code"])
        for sensitive in SENSITIVE:
            self.assertNotIn(sensitive, output.getvalue())
        return value

    def test_index_missing_target_duplicate_uid_wrong_link(self):
        path = self.root / "profiles.yaml"
        original = yaml.safe_load(path.read_text())
        cases = []
        missing = copy.deepcopy(original)
        missing["items"][0]["name"] = "elsewhere"
        cases.append(missing)
        duplicate = copy.deepcopy(original)
        duplicate["items"][1]["uid"] = duplicate["items"][0]["uid"]
        cases.append(duplicate)
        wrong = copy.deepcopy(original)
        wrong["items"][0]["option"]["script"] = "new-merge"
        cases.append(wrong)
        for case in cases:
            write_yaml(path, case)
            self.assertEqual(self.cli()["exit_code"], 2)

    def test_missing_generated_and_symlink(self):
        path = self.root / "clash-verge.yaml"
        path.unlink()
        self.assertEqual(self.cli()["exit_code"], 2)
        path.symlink_to(self.root / "profiles/merge.yaml")
        self.assertEqual(self.cli()["exit_code"], 2)

    def test_manual_corruption_no_source_leak(self):
        path = self.root / "profiles/script.js"
        path.write_text(path.read_text().replace('"schema": 1', '"schema": "' + SECRET + '"'))
        self.assertEqual(self.cli()["exit_code"], 2)

    def test_requested_runtime_unavailable_no_silent_fallback(self):
        for code in ("runtime_auth", "socket_unavailable", "runtime_shape"):
            with patch("runtime.read_runtime", side_effect=AuditError(code, "模拟已脱敏错误。")):
                result = self.cli("--runtime")
            self.assertEqual(result["exit_code"], 2)
            self.assertFalse(result["runtime_verified"])
            self.assertFalse(result["ok"])
            self.assertTrue(result["runtime_requested"])

    def test_issues_have_priority_over_unverified(self):
        self.config["rules"].remove("GEOSITE,cn,DIRECT")
        write_yaml(self.root / "clash-verge.yaml", self.config)
        with patch("runtime.read_runtime", side_effect=AuditError("runtime_auth", "模拟认证失败。")):
            result = self.cli("--runtime")
        self.assertEqual(result["exit_code"], 1)
        self.assertFalse(result["runtime_verified"])

    def test_runtime_rule_order_and_dns_drift(self):
        for change_dns in (True, False):
            state = snapshot(self.config)
            if change_dns:
                state["/configs"]["dns"]["nameserver-policy"]["geosite:cn"] = ["https://dns.alidns.com/dns-query"]
            else:
                rows = state["/rules"]["rules"]
                index = next(i for i, row in enumerate(rows) if row["payload"] == "ai.azure.com")
                rows[index], rows[index + 1] = rows[index + 1], rows[index]
                for i, row in enumerate(rows):
                    row["index"] = i
            with patch("runtime.read_runtime", return_value=state):
                result = self.cli("--runtime")
            self.assertEqual(result["exit_code"], 1)

    def test_runtime_unexpected_proxy_egress(self):
        state = snapshot(self.config)
        self.config["proxies"].append({"name": "different", "type": "vmess"})
        write_yaml(self.root / "clash-verge.yaml", self.config)
        proxies = state["/proxies"]["proxies"]
        proxies["different"] = {"type": "VMess"}
        proxies[PRIMARY]["all"].append("different")
        proxies[PRIMARY]["now"] = "different"
        with patch("runtime.read_runtime", return_value=state):
            result = self.cli("--runtime")
        self.assertEqual(result["exit_code"], 1)
        self.assertFalse(result["runtime_verified"])

    def test_unexpected_exception_is_sanitized(self):
        with patch("clash_audit.load_sources", side_effect=RuntimeError(SECRET)):
            self.assertEqual(self.cli()["exit_code"], 2)

    def test_review_preserved_exceptions_behind_match(self):
        before = [x for x in self.config["rules"] if x.split(",")[-1] not in PROTECTED]
        moved = [x for x in self.config["rules"] if x.split(",")[-1] in PROTECTED]
        self.config["rules"] = before + moved
        write_yaml(self.root / "clash-verge.yaml", self.config)
        with patch("runtime.read_runtime", return_value=snapshot(self.config)):
            result = self.cli("--runtime")
        self.assertEqual(result["exit_code"], 1)
        self.assertFalse(result["runtime_verified"])
        self.assertTrue(any(x["code"] == "exception_priority" and x["status"] == "issue" for x in result["checks"]))

    def test_review_generated_dialer_omitted_by_runtime(self):
        # 生成节点新增来源没有的 dialer 已是确定漂移，不能再只返回必要未知。
        for target, expected in (("missing-upstream", 1), ("DIRECT", 1)):
            self.config["proxies"][0]["dialer-proxy"] = target
            write_yaml(self.root / "clash-verge.yaml", self.config)
            with patch("runtime.read_runtime", return_value=snapshot(self.config)):
                result = self.cli("--runtime")
            self.assertEqual(result["exit_code"], expected)
            self.assertFalse(result["runtime_verified"])

    def test_review_dns_disabled_both_sources(self):
        self.config["dns"]["enable"] = False
        write_yaml(self.root / "clash-verge.yaml", self.config)
        with patch("runtime.read_runtime", return_value=snapshot(self.config)):
            result = self.cli("--runtime")
        self.assertEqual(result["exit_code"], 1)
        self.assertFalse(result["runtime_verified"])

    def test_concise_human_output(self):
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            code = main(["--app-dir", str(self.root)])
        self.assertEqual(code, 0)
        self.assertIn("通过", output.getvalue())
        self.assertIn("未验证", output.getvalue())
        self.assertLess(len(output.getvalue().splitlines()), 10)
        for sensitive in SENSITIVE:
            self.assertNotIn(sensitive, output.getvalue())

    def human(self, result):
        original = copy.deepcopy(result)
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            emit(result, False)
        self.assertEqual(result, original)
        for sensitive in (*SENSITIVE, yaml.safe_dump(self.config), json.dumps(self.config)):
            self.assertNotIn(sensitive, output.getvalue())
        encoded = io.StringIO()
        with contextlib.redirect_stdout(encoded):
            emit(result, True)
        self.assertEqual(json.loads(encoded.getvalue()), original)
        self.assertEqual(result["schema_version"], 1)
        return output.getvalue()

    def test_human_offline_and_unavailable(self):
        result = self.cli()
        self.assertEqual(result["exit_code"], 0)
        self.assertFalse(result["runtime_observed"])
        self.assertFalse(result["runtime_verified"])
        self.assertIn("离线检查通过，尚未观察运行态", self.human(result))
        with patch("runtime.read_runtime", side_effect=AuditError("runtime_unavailable", "本机控制接口不可用或超时。")):
            result = self.cli("--runtime")
        output = self.human(result)
        self.assertEqual(result["exit_code"], 2)
        self.assertFalse(result["runtime_observed"])
        self.assertFalse(result["runtime_verified"])
        self.assertIn("尚未完成运行态观察", output)
        self.assertIn("[必要未验证] runtime.runtime_unavailable", output)
        self.assertNotIn("关键分流运行态检查通过", output)

    def test_human_dns_only_issue_and_other_unknown(self):
        for case, exit_code in (("dns", 2), ("issue", 1), ("unknown", 2)):
            with self.subTest(case=case):
                state = snapshot(self.config)
                del state["/configs"]["dns"]
                rows = state["/rules"]["rules"]
                if case == "issue":
                    next(row for row in rows if row["payload"] == "cn")["proxy"] = PRIMARY
                if case == "unknown":
                    for row in rows:
                        row.pop("extra")
                with patch("runtime.read_runtime", return_value=state):
                    result = self.cli("--runtime")
                self.assertEqual(result["exit_code"], exit_code)
                self.assertTrue(result["runtime_observed"])
                self.assertFalse(result["runtime_verified"])
                output = self.human(result)
                self.assertIn("[必要未验证] runtime.dns_unexposed", output)
                if case == "dns":
                    self.assertIn("关键分流运行态检查通过；内核 DNS 加载状态未验证", output)
                    self.assertIn("完整运行态验收未完成", output)
                else:
                    self.assertNotIn("关键分流运行态检查通过", output)
                    self.assertIn("已确认存在问题" if case == "issue" else "[必要未验证] runtime.disabled_unknown", output)


if __name__ == "__main__":
    unittest.main()
