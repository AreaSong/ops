"""阶段 6D 的有界三态契约；节点身份不由显示名称推断。"""
import hashlib
import re
from pathlib import Path
from urllib.parse import quote, unquote

from source import strings

PRIMARY = "🚀 节点选择"
LA, SG = "🇺🇸 洛杉矶｜VMess · WS · TLS", "🇸🇬 新加坡｜E-IX 01"
ALIAS = "🚀 手动切换"
FOLLOWERS = ("💬 Ai平台", "📲 电报消息", "📹 油管视频", "🎥 奈飞视频",
             "📺 巴哈姆特", "🌍 国外媒体", "📢 谷歌FCM", "🐟 漏网之鱼")
PLACEHOLDERS = {"🇭🇰 香港节点", "🇯🇵 日本节点", "🇺🇲 美国节点", "🇸🇬 狮城节点",
                "🇨🇳 台湾节点", "🇰🇷 韩国节点", "🎥 奈飞节点"}
OTHERS = {"📺 哔哩哔哩", "🌏 国内媒体", "Ⓜ️ 微软Bing", "Ⓜ️ 微软云盘", "Ⓜ️ 微软服务",
          "🍎 苹果服务", "🎮 游戏平台", "🎶 网易音乐", "🎯 全球直连", "🛑 广告拦截", "🍃 应用净化"}
LA_IDENTITY = {"server": "23.185.200.12", "port": 443, "type": "vmess", "network": "ws",
               "tls": True, "servername": "log.areasong.top", "skip-cert-verify": False,
               "ws-opts": {"path": "/as", "headers": {"Host": "log.areasong.top"}}}
SG_IDENTITY = {"server": "8e3f7b290d64.eixcloud.com", "port": 43121, "type": "anytls",
               "sni": "rds.emsfi.com", "client-fingerprint": "chrome", "udp": True,
               "skip-cert-verify": False, "alpn": ["h2", "http/1.1"]}
BEGIN, END = "// stage6d:topology:begin v1", "// stage6d:topology:end v1"
ENTRY_SHA256 = "8eca3652ad7a60748901cf75bf8d196bc7022779c37b8576b6cc1528882e2c0f"
WRAPPER_SHA256 = "1a087e8d7cdb1d5f3342c5f1752b80c1e4643e6a437c3d2b8640b44b3ca7c6e0"


def strict_equal(left, right):
    if type(left) is not type(right):
        return False
    if isinstance(left, dict):
        return left.keys() == right.keys() and all(strict_equal(left[k], right[k]) for k in left)
    if isinstance(left, list):
        return len(left) == len(right) and all(strict_equal(a, b) for a, b in zip(left, right))
    return left == right


def identity(node, expected):
    return isinstance(node, dict) and all(strict_equal(node.get(k), v) for k, v in expected.items())


def known_legacy_choices():
    choices = {PRIMARY: [LA, "DIRECT"]}
    choices.update({n: [PRIMARY, "DIRECT", LA] for n in FOLLOWERS})
    choices["📺 巴哈姆特"] = ["DIRECT", PRIMARY, LA]
    choices.update({n: ["DIRECT", PRIMARY, LA] for n in
                    ("Ⓜ️ 微软Bing", "Ⓜ️ 微软云盘", "Ⓜ️ 微软服务", "🍎 苹果服务", "🎮 游戏平台")})
    choices.update({"📺 哔哩哔哩": ["🎯 全球直连", "DIRECT"], "🌏 国内媒体": ["DIRECT", LA],
                    "🎶 网易音乐": ["DIRECT", PRIMARY], "🎯 全球直连": ["DIRECT", PRIMARY],
                    "🛑 广告拦截": ["REJECT", "DIRECT"], "🍃 应用净化": ["REJECT", "DIRECT"]})
    return choices


def without_name(node):
    return {k: v for k, v in node.items() if k != "name"}


def script_contract(report, script):
    candidate = BEGIN in script or END in script
    if candidate:
        block = Path(__file__).with_name("candidate_topology.js").read_text().strip()
        entry = re.search(r"function __clash_direct_original_main__\([^)]*\)\s*\{([^}]+)\}", script)
        report.test("source", "candidate_script", script.count(BEGIN) == script.count(END) == 1
                    and block in script and script.count("function simplifyLosAngelesNode(") == 1
                    and entry is not None and hashlib.sha256(entry[0].encode()).hexdigest() == ENTRY_SHA256,
                    "候选脚本的拓扑实现与本地已回归版本一致；不以脚本异常充当应用门禁。")
    start = script.find("function main(", script.find("/* clash-direct:managed:begin"))
    end = script.find("/* clash-direct:managed:end")
    wrapper = script[start:end] if 0 <= start < end else ""
    report.test("source", "managed_wrapper", hashlib.sha256(wrapper.encode()).hexdigest() == WRAPPER_SHA256
                and script.count("function __clash_direct_original_main__(") == 1
                and script.count("function main(") == 1,
                "直连管理包装入口保持 v1 代码契约；UID 与清单另行核验。")
    return candidate


def node_contract(report, data):
    original = data["source"].get("proxies", [])
    nodes = data["generated"]["proxies"]
    source_ok = (isinstance(original, list) and len(original) == 1
                 and identity(original[0], LA_IDENTITY))
    report.test("source", "primary_identity", source_ok,
                "原 sub 唯一主节点匹配已核实的服务器、端口、VMess/WS/TLS 与传输身份。")
    expected = [{**original[0], "name": LA}] if source_ok else []
    ext = data["extensions"]["proxies"]
    added = ext["append"]
    backup_ok = not added
    if added:
        eix = data.get("backup_source", [])
        backup_ok = (len(added) == len(eix) == 1 and identity(added[0], SG_IDENTITY)
                     and added[0].get("name") == SG and strict_equal(without_name(added[0]), without_name(eix[0])))
        if backup_ok:
            expected += added
    report.test("source", "backup_identity", backup_ok and not ext["prepend"] and not ext["delete"],
                "proxies 扩展只能追加唯一指定新加坡节点，完整字段与当前 E-IX 来源一致（仅显示名不同）。")
    report.test("offline", "node_identity", source_ok and backup_ok and len(nodes) == len(expected)
                and {x["name"] for x in nodes} == {x["name"] for x in expected}
                and all(any(strict_equal(x, e) for e in expected) for x in nodes),
                "生成节点集合仅含已核实主节点和可选备用，凭据及所有传输字段与来源保持一致。")
    return original[0].get("name") if source_ok else None


def legacy_groups(source, original_name):
    groups = source.get("proxy-groups")
    if not isinstance(groups, list) or not all(isinstance(g, dict) for g in groups):
        return None
    names = [g.get("name") for g in groups]
    required = {PRIMARY, ALIAS, *FOLLOWERS, *OTHERS, *PLACEHOLDERS}
    if set(names) != required or len(names) != len(required):
        return None
    expected, removed = {}, set()
    for group in groups:
        name = group["name"]
        if set(group) != {"name", "type", "proxies"} or group["type"] != "select" or not strings(group["proxies"]):
            return None
        if name in PLACEHOLDERS or name == ALIAS:
            if group["proxies"] != ([original_name] if name == ALIAS else ["DIRECT"]):
                return None
            removed.add(name)
    for group in groups:
        name = group["name"]
        if name in removed:
            continue
        values = ["DIRECT" if x in PLACEHOLDERS else LA if x in {ALIAS, original_name} else x
                  for x in group["proxies"]]
        values = list(dict.fromkeys(values))
        preferred = LA if name == PRIMARY else PRIMARY if name in {"🎥 奈飞视频", "📢 谷歌FCM"} else None
        if preferred in values:
            values = [preferred] + [x for x in values if x != preferred]
        expected[name] = {**group, "proxies": values}
    # 历史兼容不能跟随来源漂移，把 DIRECT 默认或 GLOBAL 间接循环接受为新基线。
    if {n: g["proxies"] for n, g in expected.items()} != known_legacy_choices():
        return None
    return expected


def topology_contract(report, data, catalog):
    original_name = node_contract(report, data)
    candidate = script_contract(report, data["script"])
    expected = legacy_groups(data["source"], original_name)
    groups_ext = data["extensions"]["groups"]
    report.test("source", "group_baseline", expected is not None and not any(groups_ext[k] for k in ("prepend", "append", "delete")),
                "原订阅的组、严格单节点别名和空地域占位符合已核实边界，无未知组扩展。")
    nodes = data["generated"]["proxies"]
    names = {n["name"] for n in nodes}
    state = "dual" if names == {LA, SG} and len(nodes) == 2 else "transition" if candidate else "legacy"
    valid = names == ({LA, SG} if state == "dual" else {LA}) and len(nodes) in (1, 2)
    valid &= candidate if state != "legacy" else len(nodes) == 1
    report.test("offline", "topology_state", valid, "配置边界：" + state + "；只接受原单节点、过渡单节点或目标双节点。")
    if expected is None:
        return state
    if state != "legacy":
        for name, group in expected.items():
            values = [PRIMARY if x == LA else x for x in group["proxies"]]
            group["proxies"] = list(dict.fromkeys(values))
            if name in FOLLOWERS:
                group["proxies"] = [PRIMARY]
        expected[PRIMARY]["proxies"] = [LA, SG] if state == "dual" else [LA]
    actual = {g["name"]: g for g in data["generated"]["proxy-groups"]}
    report.test("offline", "group_scope", actual.keys() == expected.keys(), "策略组集合保持不变，无新增、缺失或旧别名残留。")
    report.test("offline", "primary_candidates", actual.get(PRIMARY) == expected[PRIMARY],
                "主组候选范围、类型及洛杉矶首位默认值正确；新拓扑不含 DIRECT。")
    report.test("offline", "follower_groups", all(actual.get(n) == expected[n] for n in FOLLOWERS),
                "八组满足对应状态的完整候选契约；新拓扑只跟随主组。")
    report.test("offline", "other_groups", all(actual.get(n) == expected[n] for n in OTHERS),
                "其他业务组保留直连、拒绝与默认顺序，代理选项在新拓扑统一跟随主组。")
    return state


def replace_fragment(value, target):
    base, marker, fragment = value.partition("#")
    if not marker:
        return value
    parts = fragment.split("&")
    return base + "#" + "&".join([quote(target, safe="~()*!.'-"), *parts[1:]])


def dns_contract(report, data, state):
    config, source = data["generated"], data["source"]
    dns = config["dns"]
    foreign = data["extensions"]["merge"]["dns"]["nameserver-policy"].get("geosite:geolocation-!cn", [])
    target = LA if state == "legacy" else PRIMARY
    expected = [replace_fragment(v, target) for v in foreign] if strings(foreign) else []
    policy = dns["nameserver-policy"]
    values = [policy.get("geosite:geolocation-!cn"), policy.get("geosite:gfw"), dns.get("fallback")]
    report.test("offline", "dns_egress", bool(expected) and all(v == expected for v in values),
                "境外、gfw、fallback 三处保留原 DoH 和附加参数，出口符合当前拓扑。")
    original = source.get("dns", {})
    resolvers = dns.get("proxy-server-nameserver")
    report.test("offline", "node_resolver", resolvers == original.get("proxy-server-nameserver")
                and resolvers == ["119.29.29.29", "223.5.5.5"]
                and not dns.get("proxy-server-nameserver-policy"),
                "节点域名仍由既有国内 IP 独立解析，不经主组或额外节点解析策略。")
    report.test("offline", "dns_respect_rules", dns.get("respect-rules") == original.get("respect-rules"),
                "respect-rules 保持原值与缺省语义。")


def graph_contract(report, config, catalog):
    edges = {name: item.get("proxies", []) + ([item["dialer-proxy"]] if item.get("dialer-proxy") else [])
             for name, item in catalog.items()}
    visiting, finished = set(), set()

    def visit(name):
        if name in visiting:
            return False
        if name in finished or name not in edges:
            return True
        visiting.add(name)
        if not all(isinstance(n, str) and visit(n) for n in edges[name]):
            return False
        visiting.remove(name)
        finished.add(name)
        return True

    report.test("offline", "reference_cycles", all(visit(name) for name in edges),
                "全部组候选和 dialer-proxy 构成的引用图无循环，包括未选中的分支。")
    report.test("offline", "global_reference", not any("GLOBAL" in refs for refs in edges.values()),
                "有界拓扑的业务组或节点不引用内核动态 GLOBAL，避免隐藏的回边。")
    report.test("offline", "dynamic_outbounds", not config.get("proxy-providers")
                and not any(any(g.get(k) for k in ("use", "include-all", "include-all-proxies", "include-all-providers"))
                            for g in config["proxy-groups"]),
                "三态边界不引入动态 provider 或隐式候选。")
    refs = []

    def dns_refs(value):
        if isinstance(value, dict):
            for item in value.values():
                dns_refs(item)
        elif isinstance(value, list):
            for item in value:
                dns_refs(item)
        elif isinstance(value, str) and "#" in value:
            ref = unquote(value.split("#", 1)[1].split("&", 1)[0])
            refs.append(ref)

    dns_refs(config.get("dns"))
    report.test("offline", "dns_references", all(r in catalog or r == "DIRECT" for r in refs),
                "所有 DNS 字段的显式出口均有效，无悬空引用。")
