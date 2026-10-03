"""有限结构检查：不展开 geosite/rule-set，也不模拟 Mihomo 匹配器。"""
from collections import Counter
from urllib.parse import unquote, urlsplit

from source import AuditError, manual_rules, strings
from topology import topology_contract, dns_contract, graph_contract


PRIMARY, AI = "🚀 节点选择", "💬 Ai平台"
DOMESTIC, FOREIGN, GFW = "geosite:cn", "geosite:geolocation-!cn", "geosite:gfw"
HOSTS = ("ai.azure.com", "openaiapi-site.azureedge.net")
PROTECTED = {"🛑 广告拦截", "🍃 应用净化", "🍎 苹果服务", "Ⓜ️ 微软服务", "🎮 游戏平台"}
BUILTINS = {"DIRECT", "REJECT", "REJECT-DROP", "PASS", "PASS-RULE", "COMPATIBLE", "GLOBAL"}
TENCENT = ("qq.com", "qpic.cn", "qlogo.cn", "gtimg.cn", "gtimg.com", "wechat.com", "weixin.com", "weixinbridge.com")
SIMPLE_TYPES = {"DOMAIN", "DOMAINSUFFIX", "DOMAINKEYWORD", "GEOIP", "GEOSITE", "MATCH",
                "IPCIDR", "IPCIDR6", "SRCIPCIDR", "SRCPORT", "DSTPORT", "PROCESSNAME",
                "PROCESSPATH", "RULESET", "NETWORK", "INNAME", "INPORT", "INUSER"}


def rule(line):
    parts = [x.strip() for x in line.split(",")]
    kind = parts[0].replace("-", "").upper()
    if kind not in SIMPLE_TYPES:
        return None
    size = 2 if kind == "MATCH" else 3
    if len(parts) not in (size, size + 1) or any(not x for x in parts):
        raise AuditError("rule_shape", "已知规则类型的字段结构无效。")
    if len(parts) > size and parts[-1] != "no-resolve":
        raise AuditError("rule_shape", "规则包含不支持的尾部参数。")
    payload = "" if kind == "MATCH" else parts[1]
    return kind, normalize_payload(kind, payload), parts[size - 1]


def normalize_payload(kind, value):
    return value.lower() if kind in {"DOMAIN", "DOMAINSUFFIX", "DOMAINKEYWORD", "GEOIP", "GEOSITE"} else value


def parse_rules(config):
    lines = config.get("rules")
    if not strings(lines) or not lines:
        raise AuditError("rules_shape", "rules 缺失、为空或含非字符串条目。")
    return [rule(x) for x in lines]


def obvious_shadow(row, host):
    if row is None:
        return False
    kind, payload, _ = row
    return (kind == "MATCH" or kind == "DOMAIN" and payload == host
            or kind == "DOMAINSUFFIX" and (payload == host or host.endswith("." + payload))
            or kind == "DOMAINKEYWORD" and payload in host)


def key_rules(report, rows, scope):
    wanted = [("GEOSITE", "cn", "DIRECT"), ("GEOSITE", "geolocation-!cn", PRIMARY)]
    wanted += [("DOMAIN", host, AI) for host in HOSTS]
    positions = []
    for number, expected in enumerate(wanted):
        matches = [i for i, row in enumerate(rows) if row and row[:2] == expected[:2]]
        report.test(scope, "key_rule_" + str(number),
                    len(matches) == 1 and rows[matches[0]] == expected,
                    ("国内分类", "境外分类", "ai.azure.com 精确规则", "openaiapi-site.azureedge.net 精确规则")[number]
                    + "：应唯一存在且目标正确。")
        positions.append(matches[0] if len(matches) == 1 else None)
    cn, foreign = positions[:2]
    fallbacks = [i for i, row in enumerate(rows) if row and
                 (row[0] == "MATCH" or row[:2] == ("GEOIP", "cn"))]
    report.test(scope, "classification_order", cn is not None and foreign is not None
                and cn < foreign and bool(fallbacks) and foreign < min(fallbacks)
                and any(row and row[0] == "MATCH" for row in rows)
                and any(row and row[:2] == ("GEOIP", "cn") for row in rows),
                "国内、境外分类依次位于 GEOIP,CN 与 MATCH 兜底之前。")
    for host, position in zip(HOSTS, positions[2:]):
        report.test(scope, "ai_priority_" + str(HOSTS.index(host)), position is not None
                    and not any(obvious_shadow(row, host) and row[2] != AI for row in rows[:position]),
                    host + "：精确 AI 例外之前无可静态确定的其他目标遮挡。")
    return positions


def outbound_catalog(config):
    result = {}
    for field in ("proxies", "proxy-groups"):
        values = config.get(field)
        if not isinstance(values, list):
            raise AuditError("outbound_shape", "节点或策略组列表缺失。")
        for item in values:
            if (not isinstance(item, dict) or not isinstance(item.get("name"), str)
                    or not item["name"] or not isinstance(item.get("type"), str)):
                raise AuditError("outbound_shape", "节点或策略组条目结构无效。")
            if item["name"] in result or item["name"] in BUILTINS:
                raise AuditError("outbound_duplicate", "节点、组或内置出口名称冲突。")
            result[item["name"]] = item
    return result


def check_references(report, config, rows, catalog):
    providers = config.get("proxy-providers", {})
    rulesets = config.get("rule-providers", {})
    if not isinstance(providers, dict) or not isinstance(rulesets, dict):
        raise AuditError("provider_shape", "provider 索引结构无效。")
    missing = [x for x in rows if x and x[2] not in BUILTINS and x[2] not in catalog]
    report.test("offline", "rule_references", not missing, "可识别规则的目标引用均存在。")
    report.test("offline", "ruleset_references", not any(x and x[0] == "RULESET" and x[1] not in rulesets for x in rows),
                "可识别 RULE-SET 的来源引用均存在。")
    valid = True
    for group in config["proxy-groups"]:
        members, uses = group.get("proxies", []), group.get("use", [])
        if not strings(members) or not strings(uses):
            raise AuditError("group_shape", "策略组候选或 provider 引用结构无效。")
        valid &= all(x in catalog or x in BUILTINS for x in members)
        valid &= all(x in providers for x in uses)
        valid &= bool(members or uses or group.get("include-all") or group.get("include-all-proxies")
                      or group.get("include-all-providers"))
    report.test("offline", "group_references", valid, "策略组候选与 provider 无明显悬空引用。")
    dialers = [item.get("dialer-proxy") for item in catalog.values() if item.get("dialer-proxy")]
    report.test("offline", "dialer_references", all(isinstance(x, str) and (x in catalog or x in BUILTINS) for x in dialers),
                "生成配置中的 dialer-proxy 引用应存在。")
    report.test("offline", "required_groups", all(x in catalog and "proxies" in catalog[x] for x in (AI, PRIMARY)),
                "AI 与节点选择策略组存在。")
    if any(x is None for x in rows):
        report.add("offline", "unsupported_rules", "unverified", "存在未解析的规则类型；其引用及匹配语义未验证。", False)


def preserve_exceptions(report, data, rows):
    source_rows = parse_rules(data["source"])
    old = [x for x in source_rows if x and x[2] in PROTECTED]
    new = [x for x in rows if x and x[2] in PROTECTED]
    report.test("offline", "protected_rules", bool(old) and old == new and PROTECTED <= {x[2] for x in old},
                "拦截、Apple、微软服务和游戏规则段相对当前订阅来源保持内容与顺序。")
    extension = data["extensions"]["rules"]
    expected = extension["prepend"] + extension["append"]
    if not strings(expected) or not strings(extension["delete"]):
        raise AuditError("school_shape", "规则扩展条目结构无效。")
    school = "DOMAIN-SUFFIX,guat.edu.cn,DIRECT"
    report.test("offline", "school_source", school in expected and school not in extension["delete"],
                "学校直连例外在当前关联规则扩展中可识别。")
    manual = manual_rules(data["script"], data["uid"])
    counts = Counter(data["generated"]["rules"])
    report.test("offline", "extension_rules", all(counts[x] == 1 for x in expected),
                "关联规则扩展的条目在生成配置中唯一保留。")
    report.test("offline", "manual_rules", all(counts[x] == 1 for x in manual)
                and data["generated"]["rules"][:len(manual)] == manual,
                "手动直连数据可识别且在生成规则首部唯一保留（空清单有效）。")
    report.test("offline", "school_priority", school in data["generated"]["rules"]
                and not any(obvious_shadow(x, "guat.edu.cn") and x[2] != "DIRECT"
                            for x in rows[:data["generated"]["rules"].index(school)]),
                "学校域名之前无可静态确定的非 DIRECT 遮挡。")
    preferred = {rule(x) for x in manual + extension["prepend"]}
    important = [i for i, row in enumerate(rows) if row and (row in preferred or row[2] in PROTECTED)]
    categories = [i for i, row in enumerate(rows) if row and row[:2] in {
        ("GEOSITE", "cn"), ("GEOSITE", "geolocation-!cn")}]
    report.test("offline", "exception_priority", bool(important) and bool(categories) and max(important) < min(categories),
                "学校、手动直连、拦截及保留业务例外应先于国内/境外通用分类。")
    return [rule(x) for x in manual + expected]


def policy(config):
    dns = config.get("dns")
    value = dns.get("nameserver-policy") if isinstance(dns, dict) else None
    if not isinstance(value, dict) or not value:
        raise AuditError("dns_shape", "DNS nameserver-policy 缺失或结构无效。")
    for key, servers in value.items():
        if not isinstance(key, str) or not strings(as_list(servers)) or not as_list(servers):
            raise AuditError("dns_shape", "DNS 策略键或解析服务器结构无效。")
    return value


def as_list(value):
    return value if isinstance(value, list) else [value]


def outbound(value):
    try:
        return unquote(urlsplit(value).fragment.split("&")[0])
    except ValueError:
        raise AuditError("dns_url", "DNS 地址无法解析；未回显地址。") from None


def domestic_servers(value):
    if not value:
        return False
    try:
        return all(urlsplit(x).scheme == "https" and urlsplit(x).hostname in {"dns.alidns.com", "doh.pub"}
                   and urlsplit(x).path == "/dns-query" and outbound(x) == "DIRECT" for x in as_list(value))
    except ValueError:
        raise AuditError("dns_url", "DNS 地址无法解析；未回显地址。") from None


def check_dns(report, data, catalog):
    generated = policy(data["generated"])
    source = policy(data["extensions"]["merge"])
    keys = list(generated)
    report.test("offline", "dns_enabled", data["generated"]["dns"].get("enable") is True,
                "生成配置应显式启用 DNS，以应用本阶段解析策略。")
    report.test("offline", "dns_domestic", domestic_servers(generated.get(DOMESTIC)),
                "国内 geosite:cn 使用显式 DIRECT 的阿里/腾讯国内 DoH。")
    report.test("offline", "dns_order", DOMESTIC in keys and FOREIGN in keys
                and all(keys.index(DOMESTIC) < keys.index(x) for x in (FOREIGN, GFW) if x in keys),
                "国内 DNS 分类先于通用境外与 gfw 分类。")
    directed = {k: v for k, v in source.items() if not k.startswith("geosite:")}
    report.test("offline", "dns_directed", DOMESTIC in keys and bool(directed)
                and {"+.apple.com", "+.icloud.com"} <= directed.keys()
                and all(generated.get(k) == v for k, v in directed.items())
                and all(k in keys and keys.index(k) < keys.index(DOMESTIC) for k in directed),
                "当前 DNS 扩展的定向例外保留且先于通用分类。")
    report.test("offline", "dns_tencent", DOMESTIC in keys and all(
        domestic_servers(generated.get("+." + host)) and keys.index("+." + host) < keys.index(DOMESTIC)
        for host in TENCENT), "既有腾讯定向国内 DoH 例外保留并优先。")
    refs = set()
    for key in (FOREIGN, GFW):
        values = as_list(generated.get(key, []))
        selected = [outbound(x) for x in values]
        report.test("offline", "dns_foreign_" + ("general" if key == FOREIGN else "gfw"),
                    bool(selected) and all(x in catalog for x in selected),
                    "境外 DNS 分类具有显式且可解析的代理出口引用。")
        refs.update(selected)
    fallback = data["generated"]["dns"].get("fallback", [])
    if not strings(as_list(fallback)):
        raise AuditError("dns_shape", "fallback DNS 列表结构无效。")
    selected = [outbound(x) for x in as_list(fallback)]
    report.test("offline", "dns_fallback", bool(selected) and all(x in catalog for x in selected),
                "fallback DNS 具有显式且可解析的代理出口引用。")
    refs.update(selected)
    report.add("offline", "dns_reachability", "unverified", "仅检查 DNS 结构；未发送 DNS 或网站可达性探测。", False)
    return refs


def audit_offline(report, data):
    config = data["generated"]
    rows = parse_rules(config)
    catalog = outbound_catalog(config)
    report.test("offline", "mode", config.get("mode") == "rule", "生成配置使用 rule 模式。")
    key_rules(report, rows, "offline")
    check_references(report, config, rows, catalog)
    graph_contract(report, config, catalog)
    state = topology_contract(report, data, catalog)
    exceptions = preserve_exceptions(report, data, rows)
    refs = check_dns(report, data, catalog)
    dns_contract(report, data, state)
    report.add("offline", "static_limits", "unverified",
               "未执行扩展脚本或展开 geosite/rule-set/逻辑规则；不证明完整首命中语义或源脚本下次生成结果。", False)
    report.add("offline", "history", "warning", "仅比较当前来源与生成结果；源文件和生成文件同步改变时，无历史基线可证明所有旧例外仍在。", False)
    return rows, catalog, exceptions, refs
