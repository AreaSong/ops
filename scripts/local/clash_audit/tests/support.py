"""仅生成合成配置，不复制活动配置、订阅或凭据。"""
import copy
import json
import sys
from pathlib import Path

import yaml

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from checks import AI, PRIMARY, PROTECTED, TENCENT, DOMESTIC, FOREIGN, GFW, parse_rules
from topology import LA, SG, ALIAS, FOLLOWERS, PLACEHOLDERS, LA_IDENTITY, SG_IDENTITY
from urllib.parse import quote


SECRET = "fixture-secret-never-print"
SENSITIVE = (SECRET, "fixture-uuid-never-print", "fixture-password-never-print", "https://subscription.invalid/?token=private")


def fixture(root):
    profiles = root / "profiles"
    profiles.mkdir()
    index = {"current": "random-sub", "items": [{"uid": "random-sub", "name": "sub", "type": "remote",
             "file": "subscription.yaml", "url": SENSITIVE[3],
             "option": {x: "new-" + x for x in ("script", "merge", "rules", "proxies", "groups")}}]}
    for role in ("script", "merge", "rules", "proxies", "groups"):
        index["items"].append({"uid": "new-" + role, "type": role, "file": role + (".js" if role == "script" else ".yaml")})
    manual = ["DOMAIN,manual.example,DIRECT"]
    school = "DOMAIN-SUFFIX,guat.edu.cn,DIRECT"
    protected = ["DOMAIN,protected" + str(i) + ".example," + target for i, target in enumerate(sorted(PROTECTED))]
    original = protected + ["DOMAIN-SUFFIX,azure.com,Ⓜ️ 微软服务", "DOMAIN-SUFFIX,azureedge.net,Ⓜ️ 微软服务",
                            "GEOIP,CN,DIRECT", "MATCH," + PRIMARY]
    generated = manual + [school] + protected + ["DOMAIN,ai.azure.com," + AI,
                "DOMAIN-SUFFIX,azure.com,Ⓜ️ 微软服务", "DOMAIN,openaiapi-site.azureedge.net," + AI,
                "DOMAIN-SUFFIX,azureedge.net,Ⓜ️ 微软服务", "GEOSITE,cn,DIRECT",
                "GEOSITE,geolocation-!cn," + PRIMARY, "GEOIP,CN,DIRECT", "MATCH," + PRIMARY]
    domestic = ["https://dns.alidns.com/dns-query#DIRECT", "https://doh.pub/dns-query#DIRECT"]
    foreign = ["https://1.1.1.1/dns-query#" + quote(LA, safe="~()*!.'-")]
    directed = {"+.apple.com": ["https://dns.alidns.com/dns-query"], "+.icloud.com": ["https://doh.pub/dns-query"]}
    dns = dict(directed)
    dns.update({"+." + host: domestic for host in TENCENT})
    dns.update({DOMESTIC: domestic, FOREIGN: foreign, GFW: foreign})
    groups, source_groups = synthetic_groups()
    node = {**copy.deepcopy(LA_IDENTITY), "name": LA, "uuid": SENSITIVE[1], "password": SENSITIVE[2]}
    config = {"secret": SECRET, "mode": "rule", "rules": generated,
              "dns": {"enable": True, "nameserver-policy": dns, "fallback": foreign,
                      "proxy-server-nameserver": ["119.29.29.29", "223.5.5.5"]},
              "proxy-groups": groups, "proxies": [node]}
    write_yaml(root / "profiles.yaml", index)
    write_yaml(root / "clash-verge.yaml", config)
    write_yaml(profiles / "subscription.yaml", {"rules": original, "proxy-groups": source_groups,
               "proxies": [{**node, "name": "synthetic-la"}],
               "dns": {"proxy-server-nameserver": ["119.29.29.29", "223.5.5.5"]}})
    write_yaml(profiles / "merge.yaml", {"dns": {"nameserver-policy": {**directed, FOREIGN: foreign}}})
    for role in ("rules", "proxies", "groups"):
        write_yaml(profiles / (role + ".yaml"), {"prepend": [school] if role == "rules" else [], "append": [], "delete": []})
    script = managed_script(manual)
    (profiles / "script.js").write_text(script)
    return config


def write_yaml(path, data):
    path.write_text(yaml.safe_dump(data, allow_unicode=True, sort_keys=False))


def snapshot(config):
    rows = [{"index": i, "type": row[0], "payload": row[1], "proxy": row[2], "extra": {"disabled": False}}
            for i, row in enumerate(parse_rules(config))]
    proxies = {node["name"]: {"type": node["type"]} for node in config["proxies"]}
    proxies["DIRECT"] = {"type": "Direct"}
    for group in config["proxy-groups"]:
        proxies[group["name"]] = {"type": "Selector", "all": list(group["proxies"]), "now": group["proxies"][0]}
    if "DIRECT" in proxies.get("📺 巴哈姆特", {}).get("all", []):
        proxies["📺 巴哈姆特"]["now"] = PRIMARY
    return {"/version": {"version": "fixture"}, "/configs": copy.deepcopy({"mode": "rule", "dns": config["dns"]}),
            "/rules": {"rules": rows}, "/proxies": {"proxies": proxies}}


def synthetic_groups():
    values = {PRIMARY: [LA, "DIRECT"]}
    values.update({n: [PRIMARY, "DIRECT", LA] for n in FOLLOWERS})
    values["📺 巴哈姆特"] = ["DIRECT", PRIMARY, LA]
    values.update({n: ["DIRECT", PRIMARY, LA] for n in
                   ("Ⓜ️ 微软Bing", "Ⓜ️ 微软云盘", "Ⓜ️ 微软服务", "🍎 苹果服务", "🎮 游戏平台")})
    values.update({"📺 哔哩哔哩": ["🎯 全球直连", "DIRECT"], "🌏 国内媒体": ["DIRECT", LA],
                   "🎶 网易音乐": ["DIRECT", PRIMARY], "🎯 全球直连": ["DIRECT", PRIMARY],
                   "🛑 广告拦截": ["REJECT", "DIRECT"], "🍃 应用净化": ["REJECT", "DIRECT"]})
    groups = [{"name": n, "type": "select", "proxies": p} for n, p in values.items()]
    source = [{**g, "proxies": [ALIAS if n == LA else n for n in g["proxies"]]} for g in groups]
    source += [{"name": ALIAS, "type": "select", "proxies": ["synthetic-la"]}]
    source += [{"name": n, "type": "select", "proxies": ["DIRECT"]} for n in sorted(PLACEHOLDERS)]
    return groups, source


def managed_script(manual, candidate=False):
    # 只调用纯脚本格式模块，不执行管理器、不触碰状态。
    sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "clash_direct"))
    from clash_direct_lib.script import render_block
    from clash_direct_lib.rules import validate_rules
    helper = (Path(__file__).resolve().parents[1] / "candidate_topology.js").read_text() if candidate else ""
    entry = 'function __clash_direct_original_main__(config, profileName) { return config; }\n'
    stubs = ""
    if candidate:
        calls = ["prioritizeTencentDns", "prioritizeAppleDns", "removeDirectOnlyPlaceholders",
                 "routePersonalOneDrive", "routeForeignWebFixes", "routeExperienceFirstServices",
                 "normalizeFallbackDns", "routeDomesticAndForeignDefaults"]
        entry = 'function __clash_direct_original_main__(config, profileName) {\n'
        entry += ''.join('  ' + name + '(config);\n' for name in calls + ["simplifyLosAngelesNode"])
        entry += '  return config;\n}\n'
        stubs = ''.join('function ' + name + '(config) {}\n' for name in calls)
    return entry + stubs + helper + '\n' + render_block("random-sub", validate_rules(manual))
