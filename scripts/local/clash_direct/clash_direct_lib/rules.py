from __future__ import annotations

import ipaddress
import re
from dataclasses import dataclass

from .errors import ToolError
from .runtime import Tools


MAX_RULES = 200
LABEL = re.compile(r"[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\Z", re.ASCII)
COMMON_PUBLIC_SUFFIXES = frozenset({
    "com.cn", "net.cn", "org.cn", "gov.cn", "edu.cn", "ac.cn",
    "com.hk", "com.tw", "com.au", "net.au", "org.au",
    "co.uk", "org.uk", "ac.uk", "gov.uk", "co.jp", "co.kr", "co.nz",
    "com.sg", "com.br", "com.mx", "co.in", "co.za",
})


def address_of(host: str):
    try:
        return ipaddress.ip_address(host)
    except ValueError:
        return None


@dataclass(frozen=True)
class Rule:
    host: str
    scope: str = "exact"

    def __post_init__(self) -> None:
        if self.scope not in {"exact", "suffix"}:
            raise ToolError("invalid_rule", "匹配方式只能是 exact 或 suffix。")
        address = address_of(self.host)
        if address:
            if self.scope != "exact" or self.host != address.compressed:
                raise ToolError("invalid_rule", "IP 只支持规范化后的单个地址。")
            return
        labels = self.host.split(".")
        if len(self.host) > 253 or not all(LABEL.fullmatch(label) for label in labels):
            raise ToolError("invalid_rule", "域名格式无效，不能含通配符、空格或规则分隔符。")
        if self.scope == "suffix" and (len(labels) < 2 or self.host in COMMON_PUBLIC_SUFFIXES):
            raise ToolError("broad_scope", "不能把顶级域名或常见公共后缀整体设为直连。")

    @property
    def line(self) -> str:
        address = address_of(self.host)
        if address:
            kind, prefix = ("IP-CIDR", 32) if address.version == 4 else ("IP-CIDR6", 128)
            return f"{kind},{self.host}/{prefix},DIRECT,no-resolve"
        kind = "DOMAIN" if self.scope == "exact" else "DOMAIN-SUFFIX"
        return f"{kind},{self.host},DIRECT"

    def describe(self) -> dict:
        address = address_of(self.host)
        return {"host": self.host, "scope": self.scope,
                "kind": "ip" if address else "domain", "rule": self.line}

    @classmethod
    def parse(cls, line: str) -> Rule:
        if not isinstance(line, str):
            raise ToolError("invalid_rule", "管理区包含非文本规则。")
        parts = line.split(",")
        if len(parts) == 3 and parts[2] == "DIRECT" and parts[0] in {"DOMAIN", "DOMAIN-SUFFIX"}:
            result = cls(parts[1], "exact" if parts[0] == "DOMAIN" else "suffix")
        elif len(parts) == 4 and parts[2:] == ["DIRECT", "no-resolve"]:
            try:
                network = ipaddress.ip_network(parts[1], strict=True)
            except ValueError as error:
                raise ToolError("invalid_rule", "管理区包含无效的 IP 规则。") from error
            expected = "IP-CIDR" if network.version == 4 else "IP-CIDR6"
            if parts[0] != expected or network.prefixlen != network.max_prefixlen:
                raise ToolError("invalid_rule", "管理区不接受 CIDR 网段，只支持单个 IP。")
            result = cls(network.network_address.compressed)
        else:
            raise ToolError("invalid_rule", "管理区包含本工具不支持的规则。")
        if result.line != line:
            raise ToolError("invalid_rule", "管理区包含未规范化的规则。")
        return result


def validate_rules(lines: list[str]) -> tuple[Rule, ...]:
    if not isinstance(lines, list) or len(lines) > MAX_RULES:
        raise ToolError("invalid_rule", f"自定义规则必须是列表，且不能超过 {MAX_RULES} 条。")
    rules = tuple(Rule.parse(line) for line in lines)
    if len(set(rules)) != len(rules):
        raise ToolError("invalid_rule", "管理区包含重复条目。")
    return rules


def resolve(value: str, scope: str, tools: Tools) -> tuple[Rule, list[str]]:
    value = value.strip()
    if not value or len(value) > 8192:
        raise ToolError("invalid_input", "网址不能为空或过长。")
    if re.search(r"[\x00-\x1f\x7f\u202a-\u202e\u2066-\u2069]", value) or "\\" in value:
        raise ToolError("invalid_input", "网址不能含控制字符或反斜线。")
    if "://" not in value and value.count("/") == 1:
        host, prefix = value.split("/", 1)
        if prefix.isdigit() and address_of(host.strip("[]")):
            raise ToolError("invalid_input", "不接受 CIDR 网段，请输入单个 IP 或完整网址。")
    parsed = tools.normalize(value)
    host = parsed["host"].lower().removesuffix(".")
    if host.startswith("[") and host.endswith("]"):
        host = host[1:-1]
    address = address_of(host)
    if isinstance(address, ipaddress.IPv6Address) and address.ipv4_mapped:
        address = address.ipv4_mapped
    rule = Rule(address.compressed if address else host, scope)
    warnings = []
    if parsed["path_ignored"]:
        warnings.append("仅保存主机名；路径、查询参数和片段已丢弃，不会写入规则或备份。")
    if parsed["port_ignored"]:
        warnings.append("输入的端口不单独保存：规则对该主机的所有端口生效。")
    if scope == "suffix":
        warnings.append(f"将覆盖 {host} 及其全部子域名；不会自动提升到更上级域名。")
    return rule, warnings
