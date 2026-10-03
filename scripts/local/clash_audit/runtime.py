"""显式运行态检查：固定四个 GET，只通过本机 Unix socket。"""
import http.client
import json
import re
import socket
import stat
import subprocess
from pathlib import Path

from checks import AI, PRIMARY, PROTECTED, HOSTS, SIMPLE_TYPES, key_rules, obvious_shadow, policy, normalize_payload
from source import AuditError, strings
from topology import FOLLOWERS


ENDPOINTS = ("/version", "/configs", "/rules", "/proxies")


def process_socket():
    try:
        listing = subprocess.run(["/bin/ps", "-axo", "pid=,comm="], capture_output=True,
                                 text=True, check=True, timeout=3).stdout
        pids = []
        for line in listing.splitlines():
            parts = line.strip().split(None, 1)
            if len(parts) == 2 and Path(parts[1]).name in {"mihomo", "verge-mihomo", "verge-mihomo-alpha"}:
                pids.append(parts[0])
        if len(pids) != 1 or not pids[0].isdigit():
            raise AuditError("socket_discovery", "无法唯一识别内核进程；请显式传入本机 --socket。")
        command = subprocess.run(["/bin/ps", "-p", pids[0], "-o", "command="], capture_output=True,
                                 text=True, check=True, timeout=3).stdout.strip()
    except (OSError, subprocess.SubprocessError, UnicodeError):
        raise AuditError("socket_discovery", "无法读取内核进程参数；请显式传入本机 --socket。") from None
    values = re.findall(r"(?<!\S)--?ext-ctl-unix(?:=|\s+)(.*?)(?=\s+--?[A-Za-z]|$)", command)
    if len(values) != 1:
        raise AuditError("socket_discovery", "内核参数中无唯一 Unix socket；不会尝试 GUI 或历史地址。")
    value = values[0].strip()
    if value[:1] in {"'", '"'} and value[-1:] == value[:1]:
        value = value[1:-1]
    return value


class UnixConnection(http.client.HTTPConnection):
    def __init__(self, path):
        super().__init__("localhost", timeout=3)
        self.path = path

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(self.timeout)
        self.sock.connect(self.path)


def validate_socket(path):
    if not isinstance(path, str) or not path.startswith("/") or any(ord(x) < 32 for x in path):
        raise AuditError("socket_path", "socket 必须是本机绝对路径，不接受 TCP/HTTP 控制地址。")
    try:
        if not stat.S_ISSOCK(Path(path).lstat().st_mode):
            raise AuditError("socket_path", "指定位置不是普通 Unix socket（不接受符号链接）。")
    except OSError:
        raise AuditError("socket_unavailable", "本机控制 socket 不存在或不可访问。") from None


def get_json(path, endpoint, secret):
    if endpoint not in ENDPOINTS:
        raise AuditError("endpoint", "拒绝未列入只读白名单的接口。")
    connection = UnixConnection(path)
    try:
        headers = {"Authorization": "Bearer " + secret} if secret else {}
        connection.request("GET", endpoint, headers=headers)
        response = connection.getresponse()
        if response.status in (401, 403):
            raise AuditError("runtime_auth", "本机控制接口认证失败；未重试或回显凭据。")
        if response.status != 200:
            raise AuditError("runtime_http", "本机控制接口未返回 HTTP 200；未跟随跳转或回显响应。")
        raw = response.read(16 * 1024 * 1024 + 1)
        if len(raw) > 16 * 1024 * 1024:
            raise AuditError("runtime_size", "控制响应超过 16 MiB 上限。")
        data = json.loads(raw)
        if not isinstance(data, dict):
            raise AuditError("runtime_shape", "控制接口响应顶层结构无效。")
        return data
    except (OSError, http.client.HTTPException):
        raise AuditError("runtime_unavailable", "本机控制接口不可用或超时。") from None
    except (ValueError, UnicodeError, RecursionError):
        raise AuditError("runtime_json", "控制接口响应解析失败；已隐藏响应原文。") from None
    finally:
        connection.close()


def read_runtime(config, explicit_socket=None):
    path = explicit_socket if explicit_socket is not None else process_socket()
    validate_socket(path)
    secret = config.get("secret", "")
    if not isinstance(secret, str) or any(ord(x) < 32 or ord(x) > 126 for x in secret):
        raise AuditError("runtime_auth_config", "本地认证字段格式不受支持；未发送请求。")
    return {endpoint: get_json(path, endpoint, secret) for endpoint in ENDPOINTS}


def runtime_rules(data):
    values = data.get("rules")
    if not isinstance(values, list) or not values:
        raise AuditError("runtime_shape", "运行态规则列表缺失、为空或结构无效。")
    rows, disabled = [], []
    for index, item in enumerate(values):
        if not isinstance(item, dict) or not all(isinstance(item.get(k), str) for k in ("type", "payload", "proxy")):
            raise AuditError("runtime_shape", "运行态规则条目结构无效。")
        if "index" in item and (type(item["index"]) is not int or item["index"] != index):
            raise AuditError("runtime_shape", "运行态规则索引与返回顺序不一致。")
        kind = item["type"].replace("-", "").upper()
        rows.append((kind, normalize_payload(kind, item["payload"]), item["proxy"]) if kind in SIMPLE_TYPES else None)
        extra = item.get("extra", {})
        value = extra.get("disabled") if isinstance(extra, dict) else None
        disabled.append(value if type(value) is bool else None)
    return rows, disabled


def relevant(row, exceptions):
    return bool(row and (row in exceptions or row[2] in PROTECTED
                        or row[:2] in {("GEOSITE", "cn"), ("GEOSITE", "geolocation-!cn"), ("GEOIP", "cn")}
                        or row[0] == "MATCH" or any(obvious_shadow(row, host) for host in HOSTS)))


def trace(proxies, start):
    seen = set()
    name = start
    for _ in range(100):
        if name in seen:
            raise AuditError("selection_cycle", "相关出口的当前选择存在引用循环。")
        seen.add(name)
        if name in {"DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE"}:
            raise AuditError("selection_direct", "相关代理路径意外到达 DIRECT/拒绝/旁路出口。")
        item = proxies.get(name)
        if not isinstance(item, dict) or not isinstance(item.get("type"), str):
            raise AuditError("selection_missing", "相关出口引用不存在或结构无效。")
        kind = item["type"].lower()
        if kind in {"selector", "urltest", "fallback"}:
            choices, selected = item.get("all"), item.get("now")
            if not strings(choices) or not isinstance(selected, str) or selected not in choices:
                raise AuditError("selection_missing", "相关策略组当前选择不在有效候选列表中。")
            name = selected
            continue
        if kind in {"direct", "reject", "pass", "compatible"}:
            raise AuditError("selection_direct", "相关路径到达直连、拒绝或旁路类型。")
        if item.get("dialer-proxy") or kind not in {
                "vmess", "vless", "trojan", "ss", "shadowsocks", "ssr", "shadowsocksr", "socks5",
                "http", "snell", "hysteria", "hysteria2", "tuic", "wireguard", "ssh", "anytls"}:
            raise AuditError("selection_unverified", "相关路径涉及非唯一选择、链式代理或未知类型，出口未验证。")
        return name
    raise AuditError("selection_unverified", "相关选择链超过 100 层，出口未验证。")


def check_selections(report, snapshot, catalog, refs):
    proxies = snapshot["/proxies"].get("proxies")
    if not isinstance(proxies, dict) or not proxies:
        raise AuditError("runtime_shape", "运行态出口映射无效。")
    check_runtime_catalog(report, proxies, catalog)
    leaves = []
    starts = list(dict.fromkeys([AI, PRIMARY, *FOLLOWERS, *sorted(refs)]))
    for index, start in enumerate(starts):
        try:
            leaf = trace(proxies, start)
            leaves.append(leaf)
            report.test("runtime", "selection_" + str(index), leaf in catalog
                        and "proxies" not in catalog[leaf]
                        and protocol(proxies[leaf]["type"]) == protocol(catalog[leaf]["type"]),
                        "相关选择链到达当前生成配置的具体代理节点，且同名节点协议类型一致。")
            if leaf in catalog and catalog[leaf].get("dialer-proxy"):
                report.add("runtime", "generated_dialer", "unverified",
                           "生成节点含 dialer-proxy；即使内核响应省略此字段，也无法确认完整链式出口。")
        except AuditError as error:
            report.add("runtime", error.code, "unverified" if error.code == "selection_unverified" else "issue", error.message)
    if len(leaves) != len(starts):
        report.add("runtime", "egress_agreement", "unverified", "部分相关选择链未解析，无法比较最终出口。")
    else:
        report.test("runtime", "egress_agreement", len(set(leaves)) == 1,
                    "八个跟随组、主组与境外/fallback DNS 引用应到达同一配置代理出口。")


def protocol(value):
    kind = value.lower()
    return {"shadowsocks": "ss", "shadowsocksr": "ssr", "selector": "select", "urltest": "url-test"}.get(kind, kind)


def check_runtime_catalog(report, proxies, catalog):
    valid = True
    for name, expected in catalog.items():
        actual = proxies.get(name)
        same_type = (isinstance(actual, dict) and isinstance(actual.get("type"), str)
                     and protocol(actual["type"]) == protocol(expected["type"]))
        valid &= same_type
        if "proxies" not in expected or not same_type:
            continue
        choices, selected = actual.get("all"), actual.get("now")
        valid &= choices == expected["proxies"] and isinstance(selected, str) and selected in expected["proxies"]
    # 内核自动增加的内置出口和 GLOBAL 不属于订阅声明节点。
    allowed = set(catalog) | {"DIRECT", "REJECT", "REJECT-DROP", "PASS", "PASS-RULE", "COMPATIBLE", "GLOBAL"}
    report.test("runtime", "outbound_catalog", valid and set(proxies) <= allowed,
                "全部运行态节点协议、业务组候选/当前选择与生成配置一致，无未知额外节点。")


def audit_runtime(report, data, offline, explicit_socket=None):
    snapshot = read_runtime(data["generated"], explicit_socket)
    if not isinstance(snapshot["/version"].get("version"), str) or not snapshot["/version"]["version"]:
        raise AuditError("runtime_shape", "内核版本响应结构无效。")
    mode = snapshot["/configs"].get("mode")
    if not isinstance(mode, str) or mode not in {"rule", "global", "direct"}:
        raise AuditError("runtime_shape", "内核模式响应结构无效。")
    rows, disabled = runtime_rules(snapshot["/rules"])
    generated_rows, catalog, exceptions, refs = offline
    report.test("runtime", "mode", mode == "rule" and mode == data["generated"].get("mode"),
                "内核当前模式：" + mode + "；应为 rule 且与生成配置一致。")
    key_rules(report, rows, "runtime")
    report.test("runtime", "rules_agreement",
                [x for x in rows if relevant(x, exceptions)] == [x for x in generated_rows if relevant(x, exceptions)],
                "关键规则、相关已知遮挡项和保留例外的内容及相对顺序与生成配置一致。")
    enabled = [state for row, state in zip(rows, disabled) if relevant(row, exceptions)]
    report.test("runtime", "rules_enabled", not any(x is True for x in enabled), "已加载关键规则与保留例外未被禁用。")
    if any(x is None for x in enabled):
        report.add("runtime", "disabled_unknown", "unverified", "此内核未完整提供关键规则 disabled 标记，启用状态无法确认。")
    check_selections(report, snapshot, catalog, refs)
    report.runtime_observed = True
    if "dns" in snapshot["/configs"]:
        actual, expected = policy(snapshot["/configs"]), policy(data["generated"])
        actual_dns, expected_dns = snapshot["/configs"]["dns"], data["generated"]["dns"]
        report.test("runtime", "dns_agreement", list(actual.items()) == list(expected.items())
                    and actual_dns.get("enable") is True
                    and all(actual_dns.get(k) == expected_dns.get(k) for k in
                            ("fallback", "proxy-server-nameserver", "proxy-server-nameserver-policy", "respect-rules")),
                    "内核暴露的 DNS 策略、fallback、节点解析与 respect-rules 内容和顺序一致。")
    else:
        report.add("runtime", "dns_unexposed", "unverified", "本机 /configs 未暴露 DNS；无法确认内核 DNS 与生成配置一致。")
