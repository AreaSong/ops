import json
import unittest

from support import BASE_CONFIG, BASE_SCRIPT, Tools, evaluate
from clash_direct_lib.errors import ToolError
from clash_direct_lib.rules import Rule, resolve, validate_rules
from clash_direct_lib.script import BASE_MAIN, BEGIN, END, ScriptDocument


class RuleTests(unittest.TestCase):
    def test_native_formats(self):
        examples = [
            (Rule("example.com"), "DOMAIN,example.com,DIRECT"),
            (Rule("example.com", "suffix"), "DOMAIN-SUFFIX,example.com,DIRECT"),
            (Rule("127.0.0.1"), "IP-CIDR,127.0.0.1/32,DIRECT,no-resolve"),
            (Rule("2001:db8::1"), "IP-CIDR6,2001:db8::1/128,DIRECT,no-resolve"),
        ]
        for rule, line in examples:
            with self.subTest(line=line):
                self.assertEqual(rule.line, line)
                self.assertEqual(Rule.parse(line), rule)

    def test_invalid_hosts_and_broad_scopes(self):
        cases = [("*.example.com", "exact"), ("bad,host", "exact"), ("-bad.example", "exact"),
                 ("bad_.example", "exact"), ("example..com", "exact"), ("EXAMPLE.COM", "exact"),
                 ("com", "suffix"), ("co.uk", "suffix"), ("com.cn", "suffix"), ("127.0.0.1", "suffix")]
        for host, scope in cases:
            with self.subTest(host=host, scope=scope), self.assertRaises(ToolError):
                Rule(host, scope)

    def test_only_supported_canonical_direct_rules(self):
        for line in ["MATCH,DIRECT", "DOMAIN,example.com,PROXY", "IP-CIDR,10.0.0.0/8,DIRECT,no-resolve",
                     "IP-CIDR,1.1.1.1/32,DIRECT", "DOMAIN,127.0.0.1,DIRECT", "DOMAIN,example.com,DIRECT,extra"]:
            with self.subTest(line=line), self.assertRaises(ToolError):
                Rule.parse(line)
        with self.assertRaises(ToolError):
            validate_rules(["DOMAIN,example.com,DIRECT"] * 2)


@unittest.skipUnless(Tools.discover().node, "需要已安装的 Node，测试不会安装")
class ResolveTests(unittest.TestCase):
    def setUp(self):
        self.tools = Tools.discover()

    def test_urls_ports_paths_and_unicode(self):
        cases = [
            ("https://DOCS.Example.com:8443/路径?secret=abc#x", "docs.example.com"),
            ("example.com/path", "example.com"), ("https://example.com./", "example.com"),
            ("https://例子.测试/路径", "xn--fsqu00a.xn--0zwm56d"),
            ("https://faß.de/", "xn--fa-hia.de"), ("http://127.0.0.1:8080/a", "127.0.0.1"),
            ("2001:db8::1", "2001:db8::1"), ("https://[2001:db8::1]:443/a", "2001:db8::1"),
            ("localhost:8080", "localhost"),
        ]
        for value, host in cases:
            with self.subTest(value=value):
                rule, warnings = resolve(value, "exact", self.tools)
                self.assertEqual(rule.host, host)
                self.assertNotIn("secret=abc", json.dumps(rule.describe()) + " ".join(warnings))

    def test_rejects_unsafe_input_without_echoing_it(self):
        values = ["", "https://user:DO_NOT_LEAK@host.example/path", "ftp://example.com/x", "https://example.com\\@evil.test",
                  "example.com\nMATCH,DIRECT", "*.example.com", "10.0.0.0/8", "[2001:db8::]/64", "https://[fe80::1%en0]/"]
        for value in values:
            with self.subTest(value=value), self.assertRaises(ToolError) as raised:
                resolve(value, "exact", self.tools)
            self.assertNotIn("DO_NOT_LEAK", str(raised.exception))

    def test_suffix_does_not_promote_parent_domain(self):
        rule, _ = resolve("https://www.example.com/path", "suffix", self.tools)
        self.assertEqual(rule.line, "DOMAIN-SUFFIX,www.example.com,DIRECT")


class DocumentTests(unittest.TestCase):
    def test_roundtrip_preserves_base_and_unrelated_trailing_code(self):
        document = ScriptDocument.parse(BASE_SCRIPT, "SUB")
        output = document.render((Rule("example.com"),))
        parsed = ScriptDocument.parse(output, "SUB")
        self.assertTrue(parsed.installed)
        self.assertEqual(parsed.prefix.replace(BASE_MAIN, "main", 1), BASE_SCRIPT)
        extra = "\nfunction moreUserCode() { return 7; }\n"
        updated = ScriptDocument.parse(output + extra, "SUB").render((Rule("other.example"),))
        self.assertTrue(updated.endswith(extra))
        self.assertEqual(ScriptDocument.parse(updated, "SUB").rules, (Rule("other.example"),))

    def test_rejects_modified_or_foreign_blocks(self):
        rendered = ScriptDocument.parse(BASE_SCRIPT, "SUB").render((Rule("example.com"),))
        variants = [rendered.replace(END, ""), rendered + BEGIN,
                    rendered.replace("managed.size > 0", "managed.size >= 0"),
                    rendered.replace('"profile_id": "SUB"', '"profile_id": "OTHER"'),
                    rendered.replace('"schema": 1', '"schema": true')]
        for source in variants:
            with self.subTest(source=source[-80:]), self.assertRaises(ToolError):
                ScriptDocument.parse(source, "SUB")

    def test_rejects_ambiguous_or_recursive_main(self):
        for source in ["const main = c => c;", BASE_SCRIPT + BASE_SCRIPT,
                       "function main(config) { return main(config); }", BASE_SCRIPT + "const __clash_direct_other = 1;"]:
            with self.subTest(source=source[:40]), self.assertRaises(ToolError):
                ScriptDocument.parse(source, "SUB")


@unittest.skipUnless(Tools.discover().node, "需要 Node")
class WrapperRuntimeTests(unittest.TestCase):
    def test_empty_layer_is_equivalent(self):
        wrapped = ScriptDocument.parse(BASE_SCRIPT, "SUB").render(())
        self.assertEqual(evaluate(wrapped, BASE_CONFIG), evaluate(BASE_SCRIPT, BASE_CONFIG))

    def test_direct_overrides_forced_proxy_and_is_idempotent(self):
        wrapped = ScriptDocument.parse(BASE_SCRIPT, "SUB").render((Rule("tools.google.com", "suffix"),))
        first = evaluate(wrapped, BASE_CONFIG)
        self.assertEqual(first["rules"][0], "DOMAIN-SUFFIX,tools.google.com,DIRECT")
        self.assertEqual(evaluate(wrapped, first), first)
        for key in ["dns", "proxies", "proxy-groups"]:
            self.assertEqual(first[key], BASE_CONFIG[key])

    def test_removal_on_fresh_generation_restores_original_routing(self):
        document = ScriptDocument.parse(BASE_SCRIPT, "SUB")
        installed = document.render((Rule("tools.google.com", "suffix"),))
        cleared = ScriptDocument.parse(installed, "SUB").render(())
        self.assertEqual(evaluate(cleared, BASE_CONFIG), evaluate(BASE_SCRIPT, BASE_CONFIG))


if __name__ == "__main__":
    unittest.main()
