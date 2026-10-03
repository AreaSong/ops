"""三态、候选脚本与故障矩阵；所有认证值为合成数据。"""
import copy
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from support import (fixture, snapshot, write_yaml, managed_script, SENSITIVE,
                     LA, SG, ALIAS, FOLLOWERS, LA_IDENTITY, SG_IDENTITY,
                     PRIMARY, FOREIGN, GFW)
from candidate import execute_script, append_backup, build_script
from clash_audit import parser, run
from source import read_yaml, AuditError


class DualTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.config = fixture(self.root)

    def candidate(self, dual=False):
        script = managed_script(["DOMAIN,manual.example,DIRECT"], True)
        (self.root / "profiles/script.js").write_text(script)
        if dual:
            backup = {**copy.deepcopy(SG_IDENTITY), "name": "🇸🇬 Singapore 01", "password": SENSITIVE[2]}
            ext = read_yaml(self.root / "profiles/proxies.yaml")
            ext = append_backup(ext, [backup])
            write_yaml(self.root / "profiles/proxies.yaml", ext)
            write_yaml(self.root / "profiles/eix.yaml", {"proxies": [backup]})
            index = read_yaml(self.root / "profiles.yaml")
            index["items"].append({"uid": "fixture-eix", "type": "remote", "name": "E-IX", "file": "eix.yaml"})
            write_yaml(self.root / "profiles.yaml", index)
            self.config["proxies"].extend(copy.deepcopy(ext["append"]))
        self.config = execute_script(script, self.config)
        return script

    def audit(self, state=None):
        write_yaml(self.root / "clash-verge.yaml", self.config)
        args = parser().parse_args(["--app-dir", str(self.root)] + (["--runtime"] if state else []))
        with patch("runtime.read_runtime", return_value=state):
            result = run(args)
        self.assertEqual(result["schema_version"], 1)
        for secret in SENSITIVE:
            self.assertNotIn(secret, json.dumps(result))
        return result

    def issue(self, result, code):
        self.assertEqual(result["exit_code"], 1)
        self.assertTrue(any(c["code"] == code and c["status"] == "issue" for c in result["checks"]), code)

    def test_all_three_states(self):
        self.assertEqual(self.audit()["exit_code"], 0)
        script = self.candidate()
        self.assertEqual(self.audit()["exit_code"], 0)
        self.assertEqual(self.config, execute_script(script, copy.deepcopy(self.config)))
        self.candidate(True)
        self.assertEqual(self.audit()["exit_code"], 0)
        self.assertEqual(self.config, execute_script(script, copy.deepcopy(self.config)))

    def test_both_choices_and_only_dns_gap(self):
        self.candidate(True)
        for choice in (LA, SG):
            state = snapshot(self.config)
            state["/proxies"]["proxies"][PRIMARY]["now"] = choice
            self.assertEqual(self.audit(state)["exit_code"], 0)
            del state["/configs"]["dns"]
            result = self.audit(state)
            self.assertEqual(result["exit_code"], 2)
            self.assertTrue(result["runtime_observed"])
            self.assertFalse(result["runtime_verified"])
            self.assertEqual([(c["scope"], c["code"]) for c in result["checks"]
                              if c["required"] and c["status"] == "unverified"], [("runtime", "dns_unexposed")])

    def test_followers_other_defaults_and_rules(self):
        rules = copy.deepcopy(self.config["rules"])
        self.candidate(True)
        groups = {g["name"]: g["proxies"] for g in self.config["proxy-groups"]}
        self.assertEqual(groups[PRIMARY], [LA, SG])
        for name in FOLLOWERS:
            self.assertEqual(groups[name], [PRIMARY])
        for name in ("Ⓜ️ 微软服务", "🍎 苹果服务", "🎮 游戏平台", "🌏 国内媒体", "🎯 全球直连"):
            self.assertEqual(groups[name], ["DIRECT", PRIMARY])
        self.assertEqual(groups["🛑 广告拦截"], ["REJECT", "DIRECT"])
        self.assertEqual(groups["📺 哔哩哔哩"], ["🎯 全球直连", "DIRECT"])
        self.assertEqual(rules, self.config["rules"])

    def test_dns_extra_parameters_and_independence(self):
        before = copy.deepcopy(self.config["dns"])
        url = 'https://1.1.1.1/dns-query?x=1#' + LA + '&h3=true&ecs=1.2.3.4/24'
        self.config["dns"]["nameserver-policy"][FOREIGN] = [url]
        self.config["dns"]["nameserver-policy"][GFW] = [url]
        self.config["dns"]["fallback"] = [url]
        self.candidate(True)
        expected = url.split('#')[0] + '#%F0%9F%9A%80%20%E8%8A%82%E7%82%B9%E9%80%89%E6%8B%A9&h3=true&ecs=1.2.3.4/24'
        self.assertEqual(self.config["dns"]["fallback"], [expected])
        self.assertEqual(self.config["dns"]["proxy-server-nameserver"], before["proxy-server-nameserver"])
        for key, value in before["nameserver-policy"].items():
            if key not in (FOREIGN, GFW):
                self.assertEqual(self.config["dns"]["nameserver-policy"][key], value)

    def test_all_group_bypasses_rejected(self):
        self.candidate(True)
        original = copy.deepcopy(self.config)
        for name in FOLLOWERS:
            for bypass in ("DIRECT", LA, SG):
                self.config = copy.deepcopy(original)
                next(g for g in self.config["proxy-groups"] if g["name"] == name)["proxies"].append(bypass)
                self.issue(self.audit(), "follower_groups")
        self.config = copy.deepcopy(original)
        next(g for g in self.config["proxy-groups"] if g["name"] == "🍎 苹果服务")["proxies"] = ["DIRECT", SG]
        self.issue(self.audit(), "other_groups")

    def test_primary_range_and_default(self):
        self.candidate(True)
        for choices in ([SG, LA], ["DIRECT", LA, SG], [LA, SG, "unknown"], [LA]):
            self.config["proxy-groups"][0]["proxies"] = choices
            self.issue(self.audit(), "primary_candidates")

    def test_source_node_fields_and_runtime_protocol(self):
        self.candidate(True)
        original = copy.deepcopy(self.config)
        for field, value in (("server", "changed.invalid"), ("port", 443), ("password", "changed"),
                             ("sni", "wrong.invalid"), ("type", "vmess"), ("skip-cert-verify", True)):
            self.config = copy.deepcopy(original)
            self.config["proxies"][1][field] = value
            self.issue(self.audit(), "node_identity")
        self.config = original
        for name in (LA, SG):
            state = snapshot(self.config)
            state["/proxies"]["proxies"][name]["type"] = "Trojan"
            self.issue(self.audit(state), "outbound_catalog")

    def test_missing_extra_nodes_and_legacy_bypass(self):
        self.candidate(True)
        original = copy.deepcopy(self.config)
        for nodes in (original["proxies"][1:], [], original["proxies"] + [{"name": "unknown", "type": "vmess"}]):
            self.config = copy.deepcopy(original)
            self.config["proxies"] = nodes
            self.assertEqual(self.audit()["exit_code"], 1)
        self.config = original
        (self.root / "profiles/script.js").write_text(managed_script(["DOMAIN,manual.example,DIRECT"]))
        self.issue(self.audit(), "topology_state")

    def test_graph_unselected_cycle_dangling_dns_and_rule(self):
        self.candidate(True)
        original = copy.deepcopy(self.config)
        for target in ("🛑 广告拦截", "🍎 苹果服务"):
            self.config = copy.deepcopy(original)
            next(g for g in self.config["proxy-groups"] if g["name"] == target)["proxies"].append(target)
            self.issue(self.audit(), "reference_cycles")
        self.config = original
        self.config["dns"]["nameserver"] = ["https://1.1.1.1/dns-query#unknown"]
        self.config["rules"].insert(0, "DOMAIN,fixture.invalid,unknown")
        result = self.audit()
        self.issue(result, "dns_references")
        self.issue(result, "rule_references")

    def test_fallback_and_resolver_drift(self):
        self.candidate(True)
        original = copy.deepcopy(self.config)
        for field, value, code in (("fallback", ['https://1.1.1.1/dns-query#' + LA], "dns_egress"),
                                   ("proxy-server-nameserver", ["https://resolver.invalid#" + PRIMARY], "node_resolver"),
                                   ("respect-rules", True, "dns_respect_rules")):
            self.config = copy.deepcopy(original)
            self.config["dns"][field] = value
            self.issue(self.audit(), code)

    def test_script_rejects_invalid_input_without_dropping_nodes(self):
        script = self.candidate(True)
        original = copy.deepcopy(self.config)
        for case in ("duplicate", "missing", "extra", "transport", "alias", "unknown"):
            config = copy.deepcopy(original)
            if case == "duplicate":
                config["proxies"].append(copy.deepcopy(config["proxies"][0]))
            if case == "missing":
                config["proxies"] = config["proxies"][1:]
            if case == "extra":
                config["proxies"].append({"name": "extra", "type": "vmess"})
            if case == "transport":
                config["proxies"][0]["ws-opts"]["path"] = "/different"
            if case == "alias":
                config["proxy-groups"].append({"name": ALIAS, "type": "select", "proxies": [LA, SG]})
            if case == "unknown":
                config["proxy-groups"][0]["proxies"].append("unknown")
            result = execute_script(script, config)
            self.assertEqual(result["proxies"], config["proxies"])
            self.config = result
            self.assertNotEqual(self.audit()["exit_code"], 0)

    def test_managed_contract_and_candidate_tamper(self):
        script = self.candidate(True)
        from clash_direct_lib.script import ScriptDocument
        document = ScriptDocument.parse(script, "random-sub")
        self.assertTrue(document.installed)
        self.assertEqual(document.render(document.rules), script)
        (self.root / "profiles/script.js").write_text(script.replace('node.port === 443', 'node.port === 444'))
        self.issue(self.audit(), "candidate_script")
        with self.assertRaises(AuditError):
            build_script(script)

    def test_backup_exact_selection_and_no_mutation(self):
        extension = {"prepend": [], "append": [], "delete": [], "custom": {"keep": True}}
        node = {**SG_IDENTITY, "name": "🇸🇬 Singapore 01", "password": SENSITIVE[2]}
        result = append_backup(extension, [node])
        self.assertEqual(result["custom"], extension["custom"])
        self.assertEqual(result["append"][0], {**node, "name": SG})
        self.assertEqual(extension["append"], [])
        for nodes in ([], [node, node], [{**node, "skip-cert-verify": True}]):
            with self.assertRaises(AuditError):
                append_backup(extension, nodes)

    def test_node_vm_escape_cannot_write_files(self):
        path = self.root / "must-not-exist"
        script = 'function main(c) { try { const p = this.constructor.constructor("return process")();' \
                 'p.getBuiltinModule("fs").writeFileSync(' + json.dumps(str(path)) + ', "blocked");' \
                 'return {blocked: false}; } catch (error) { return {blocked: ["EPERM", "EACCES"].includes(error.code)}; } }'
        self.assertEqual(execute_script(script, {}), {"blocked": True})
        self.assertFalse(path.exists())

    def test_node_execution_requires_os_sandbox(self):
        with patch("candidate.subprocess.run", side_effect=FileNotFoundError()):
            with self.assertRaises(AuditError):
                execute_script("function main(c) { return c; }", {})

    def test_legacy_source_cannot_redefine_defaults_or_reference_global(self):
        source_path = self.root / "profiles/subscription.yaml"
        original = read_yaml(source_path)
        base = copy.deepcopy(self.config)
        for case in ("primary", "global"):
            source = copy.deepcopy(original)
            self.config = copy.deepcopy(base)
            for value in (source, self.config):
                if case == "primary":
                    value["proxy-groups"][0]["proxies"] = ["DIRECT"]
                else:
                    next(g for g in value["proxy-groups"] if g["name"] == "🍎 苹果服务")["proxies"].append("GLOBAL")
            write_yaml(source_path, source)
            self.issue(self.audit(), "group_baseline")
            if case == "global":
                self.issue(self.audit(), "global_reference")

    def test_boolean_integer_confusion_is_rejected(self):
        self.candidate(True)
        original = copy.deepcopy(self.config)
        for node, field, value in ((0, "tls", 1), (0, "skip-cert-verify", 0), (1, "udp", 1), (1, "port", 43121.0)):
            self.config = copy.deepcopy(original)
            self.config["proxies"][node][field] = value
            self.issue(self.audit(), "node_identity")
        self.config = original
        path = self.root / "profiles/proxies.yaml"
        extension = read_yaml(path)
        extension["append"][0]["udp"] = 1
        write_yaml(path, extension)
        self.issue(self.audit(), "backup_identity")

    def test_candidate_entry_must_call_topology(self):
        script = self.candidate(True)
        for replacement in ("", "/* simplifyLosAngelesNode(config); */", "return config; simplifyLosAngelesNode(config);"):
            (self.root / "profiles/script.js").write_text(script.replace("simplifyLosAngelesNode(config);", replacement))
            self.issue(self.audit(), "candidate_script")

    def test_other_business_proxy_selections_follow_primary(self):
        self.candidate(True)
        state = snapshot(self.config)
        proxies = state["/proxies"]["proxies"]
        proxies[PRIMARY]["now"] = SG
        for group in self.config["proxy-groups"]:
            if PRIMARY in group["proxies"]:
                proxies[group["name"]]["now"] = PRIMARY
        self.assertEqual(self.audit(state)["exit_code"], 0)
        proxies["🍎 苹果服务"]["all"].append(SG)
        proxies["🍎 苹果服务"]["now"] = SG
        self.issue(self.audit(state), "outbound_catalog")

    def test_duplicate_names_are_confirmed_issues(self):
        self.candidate(True)
        self.config["proxies"].append(copy.deepcopy(self.config["proxies"][0]))
        self.issue(self.audit(), "outbound_duplicate")

    def test_package_concurrent_edit_and_permission_change_rejected(self):
        from verify_candidate import freeze_package, unchanged_package
        package = self.root / "private"
        package.mkdir(mode=0o700)
        path = package / "candidate.js"
        path.write_text("before")
        path.chmod(0o600)
        before = freeze_package(package)
        unchanged_package(package, before)
        path.write_text("after")
        with self.assertRaises(ValueError):
            unchanged_package(package, before)
        path.write_text("before")
        path.chmod(0o644)
        with self.assertRaises(ValueError):
            unchanged_package(package, before)


if __name__ == "__main__":
    unittest.main()
