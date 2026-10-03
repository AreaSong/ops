import contextlib
import copy
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from support import fixture, write_yaml, snapshot, SENSITIVE, DOMESTIC, FOREIGN, AI, PRIMARY
from clash_audit import main, parser, run


class AuditTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = fixture(self.root)

    def execute(self, runtime=False, state=None):
        write_yaml(self.root / "clash-verge.yaml", self.config)
        args = parser().parse_args(["--app-dir", str(self.root)] + (["--runtime"] if runtime else []))
        if runtime:
            with patch("runtime.read_runtime", return_value=state if state is not None else snapshot(self.config)):
                return run(args)
        return run(args)

    def check(self, result, code, status):
        matches = [x for x in result["checks"] if x["code"] == code]
        self.assertTrue(matches, code)
        self.assertIn(status, [x["status"] for x in matches])

    def test_normal_offline_no_network_process_or_writes(self):
        before = {p: p.read_bytes() for p in self.root.rglob("*") if p.is_file()}
        with patch("socket.socket", side_effect=AssertionError("network")), patch("subprocess.run", side_effect=AssertionError("process")):
            result = run(parser().parse_args(["--app-dir", str(self.root)]))
        self.assertEqual(result["exit_code"], 0)
        self.assertFalse(result["runtime_verified"])
        self.assertFalse(result["all_checks_passed"])
        self.assertEqual(before, {p: p.read_bytes() for p in self.root.rglob("*") if p.is_file()})

    def test_classification_missing_order_target_duplicate(self):
        original = copy.deepcopy(self.config)
        variants = [lambda r: r.remove("GEOSITE,cn,DIRECT"),
                    lambda r: r.insert(0, r.pop(r.index("GEOSITE,geolocation-!cn," + PRIMARY))),
                    lambda r: r.__setitem__(r.index("GEOSITE,cn,DIRECT"), "GEOSITE,cn," + PRIMARY),
                    lambda r: r.append("GEOSITE,cn,DIRECT"),
                    lambda r: r.append(r.pop(r.index("GEOSITE,cn,DIRECT")))]
        for change in variants:
            with self.subTest(change=variants.index(change)):
                self.config = copy.deepcopy(original)
                change(self.config["rules"])
                self.assertEqual(self.execute()["exit_code"], 1)

    def test_ai_shadow_and_missing_exact(self):
        for host, broad in (("ai.azure.com", "azure.com"), ("openaiapi-site.azureedge.net", "azureedge.net")):
            with self.subTest(host=host):
                original = list(self.config["rules"])
                exact = "DOMAIN," + host + "," + AI
                self.config["rules"].remove(exact)
                self.assertEqual(self.execute()["exit_code"], 1)
                self.config["rules"].append(exact)
                self.check(self.execute(), "ai_priority_" + str(0 if host.startswith("ai.") else 1), "issue")
                self.config["rules"] = original

    def test_domestic_dns_direct_and_order(self):
        policies = self.config["dns"]["nameserver-policy"]
        policies[DOMESTIC] = ["https://dns.alidns.com/dns-query"]
        self.check(self.execute(), "dns_domestic", "issue")
        policies[DOMESTIC] = ["https://foreign.invalid/dns-query#DIRECT"]
        self.check(self.execute(), "dns_domestic", "issue")
        policies[DOMESTIC] = policies.pop(DOMESTIC)
        self.check(self.execute(), "dns_order", "issue")

    def test_directed_dns_and_foreign_reference(self):
        policy = self.config["dns"]["nameserver-policy"]
        del policy["+.apple.com"]
        policy[FOREIGN] = ["https://1.1.1.1/dns-query#missing"]
        result = self.execute()
        self.check(result, "dns_directed", "issue")
        self.check(result, "dns_foreign_general", "issue")

    def test_school_manual_and_protected_removed(self):
        self.config["rules"] = [x for x in self.config["rules"] if "guat.edu.cn" not in x and "manual.example" not in x and "protected0" not in x]
        result = self.execute()
        for code in ("protected_rules", "extension_rules", "manual_rules", "school_priority"):
            self.check(result, code, "issue")

    def test_manual_empty_allowed(self):
        path = self.root / "profiles/script.js"
        path.write_text(path.read_text().replace('["DOMAIN,manual.example,DIRECT"]', '[]'))
        self.assertEqual(self.execute()["exit_code"], 0)

    def test_missing_file(self):
        (self.root / "profiles/merge.yaml").unlink()
        self.assertEqual(self.execute()["exit_code"], 2)

    def test_invalid_yaml_sanitized(self):
        (self.root / "profiles.yaml").write_text("secret: [" + SENSITIVE[0])
        result = self.execute()
        self.assertEqual(result["exit_code"], 2)
        self.assertNotIn(SENSITIVE[0], json.dumps(result))

    def test_duplicate_yaml_keys_rejected(self):
        (self.root / "profiles.yaml").write_text("items: []\nitems: []\n")
        self.check(self.execute(), "yaml_keys", "unverified")

    def test_target_duplicate_and_inactive(self):
        import yaml
        path = self.root / "profiles.yaml"
        index = yaml.safe_load(path.read_text())
        extra = copy.deepcopy(index["items"][0])
        extra["uid"] = "other"
        index["items"].append(extra)
        write_yaml(path, index)
        self.check(self.execute(), "target_unique", "unverified")
        extra["name"] = "other"
        index["current"] = "other"
        write_yaml(path, index)
        with patch("runtime.read_runtime", side_effect=AssertionError("must not request")):
            result = run(parser().parse_args(["--app-dir", str(self.root), "--runtime"]))
        self.assertFalse(result["active"])
        self.assertEqual(result["exit_code"], 2)

    def test_dangling_rule_and_group_reference(self):
        self.config["rules"].insert(0, "DOMAIN,example.test,missing")
        self.config["proxy-groups"][0]["proxies"] = ["missing"]
        result = self.execute()
        self.check(result, "rule_references", "issue")
        self.check(result, "group_references", "issue")

    def test_corrupt_shapes(self):
        for field, bad in (("rules", None), ("rules", [{}]), ("dns", []), ("proxy-groups", [{}]), ("proxies", None)):
            with self.subTest(field=field, bad=bad):
                previous = self.config[field]
                self.config[field] = bad
                self.assertEqual(self.execute()["exit_code"], 2)
                self.config[field] = previous

    def test_runtime_success_is_scoped(self):
        result = self.execute(True)
        self.assertEqual(result["exit_code"], 0)
        self.assertTrue(result["runtime_observed"])
        self.assertTrue(result["runtime_verified"])
        self.assertFalse(result["all_checks_passed"])

    def test_runtime_dns_not_exposed(self):
        state = snapshot(self.config)
        state["/configs"].pop("dns")
        result = self.execute(True, state)
        self.assertEqual(result["exit_code"], 2)
        self.assertFalse(result["runtime_verified"])
        self.assertTrue(result["runtime_observed"])

    def test_runtime_missing_rule_and_disabled(self):
        for missing in (True, False):
            state = snapshot(self.config)
            rows = state["/rules"]["rules"]
            i = next(i for i, x in enumerate(rows) if x["payload"] == "cn" and x["type"] == "GEOSITE")
            if missing:
                rows.pop(i)
                for number, row in enumerate(rows):
                    row["index"] = number
            else:
                rows[i]["extra"]["disabled"] = True
            result = self.execute(True, state)
            self.assertEqual(result["exit_code"], 1)
            self.assertFalse(result["runtime_verified"])

    def test_runtime_disabled_absent(self):
        state = snapshot(self.config)
        for row in state["/rules"]["rules"]:
            row.pop("extra")
        self.assertEqual(self.execute(True, state)["exit_code"], 2)

    def test_runtime_mode_drift(self):
        state = snapshot(self.config)
        state["/configs"]["mode"] = "global"
        self.check(self.execute(True, state), "mode", "issue")

    def test_runtime_direct_missing_cycle(self):
        for selected, expected in (("DIRECT", "selection_direct"), ("missing", "selection_missing"), (AI, "selection_cycle")):
            state = snapshot(self.config)
            group = state["/proxies"]["proxies"][PRIMARY]
            group["now"] = selected
            if selected == AI:
                group["all"].append(AI)
            result = self.execute(True, state)
            self.check(result, expected, "issue")
            self.assertEqual(result["exit_code"], 1)

    def test_runtime_shape_failures(self):
        for endpoint, bad in (("/version", {}), ("/configs", {"mode": []}), ("/rules", {"rules": [{}]}), ("/proxies", {"proxies": []})):
            state = snapshot(self.config)
            state[endpoint] = bad
            # run() 返回可处理错误；API 结构异常不能仅靠 main() 兜底。
            self.assertEqual(self.execute(True, state)["exit_code"], 2)

    def test_json_exit_code_and_redaction(self):
        self.config["rules"][0] = "DOMAIN,secret.example," + SENSITIVE[0]
        write_yaml(self.root / "clash-verge.yaml", self.config)
        for extra in ([], ["--bad=" + SENSITIVE[3]]):
            stdout = io.StringIO()
            with contextlib.redirect_stdout(stdout):
                code = main(["--app-dir", str(self.root), "--json"] + extra)
            result = json.loads(stdout.getvalue())
            self.assertEqual(code, result["exit_code"])
            for token in SENSITIVE:
                self.assertNotIn(token, stdout.getvalue())

    def test_socket_requires_runtime(self):
        args = parser().parse_args(["--app-dir", str(self.root), "--socket", "/tmp/fake.sock"])
        self.assertEqual(run(args)["exit_code"], 2)


if __name__ == "__main__":
    unittest.main()
