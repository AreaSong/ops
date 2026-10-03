from __future__ import annotations

import json
import re
from dataclasses import dataclass

from .errors import ToolError
from .rules import Rule, validate_rules


BEGIN = "/* clash-direct:managed:begin v1 */"
END = "/* clash-direct:managed:end v1 */"
BASE_MAIN = "__clash_direct_original_main__"
DATA_DECLARATION = "const __clash_direct_data__ = "
DATA_END = "\n/* clash-direct:data:end */"
MAIN = re.compile(r"^function\s+(main)\s*\(\s*config\s*(?:,\s*profileName\s*)?\)\s*\{", re.MULTILINE)

WRAPPER = """function main(config, profileName) {
  const directRules = __clash_direct_data__.rules;
  const managed = new Set(directRules);
  // 先移除本层规则，再运行原脚本，避免原脚本强化外网出口时反复叠加规则。
  if (Array.isArray(config.rules) && managed.size > 0) {
    config = {...config, rules: config.rules.filter(rule => !managed.has(rule))};
  }
  const result = __clash_direct_original_main__(config, profileName);
  if (!result || !Array.isArray(result.rules)) {
    throw new Error("clash-direct: 原脚本必须返回包含 rules 数组的配置");
  }
  // 用户明确添加的 DIRECT 例外最后置顶；DNS、节点、分组等其他字段原样保留。
  if (directRules.length === 0) return result;
  return {...result, rules: [...directRules, ...result.rules.filter(rule => !managed.has(rule))]};
}
"""


def render_block(profile_id: str, rules: tuple[Rule, ...]) -> str:
    data = {"schema": 1, "profile_id": profile_id, "rules": [rule.line for rule in rules]}
    return (BEGIN + "\n// 本区段由 clash-direct 管理，请通过工具增删，不要手工编辑。\n"
            + DATA_DECLARATION + json.dumps(data, ensure_ascii=True, indent=2) + ";"
            + DATA_END + "\n" + WRAPPER + END + "\n")


@dataclass(frozen=True)
class ScriptDocument:
    profile_id: str
    prefix: str
    suffix: str
    rules: tuple[Rule, ...]
    installed: bool

    @classmethod
    def parse(cls, source: str, profile_id: str) -> ScriptDocument:
        if BEGIN not in source and END not in source:
            return cls.bootstrap(source, profile_id)
        if source.count(BEGIN) != 1 or source.count(END) != 1:
            raise ToolError("managed_block_invalid", "管理区标记不完整或重复，拒绝覆盖。")
        start = source.index(BEGIN)
        end = source.index(END) + len(END)
        if start == 0 or source[start - 1] != "\n" or source[end:end + 1] != "\n":
            raise ToolError("managed_block_invalid", "管理区边界被修改，拒绝覆盖。")
        block = source[start:end + 1]
        data_start = block.find(DATA_DECLARATION)
        data_end = block.find(DATA_END)
        if data_start < 0 or data_end < data_start:
            raise ToolError("managed_block_invalid", "管理区数据缺失。")
        encoded = block[data_start + len(DATA_DECLARATION):data_end]
        if not encoded.endswith(";"):
            raise ToolError("managed_block_invalid", "管理区数据格式错误。")
        rules = cls.parse_data(encoded[:-1], profile_id)
        if block != render_block(profile_id, rules):
            raise ToolError("managed_block_modified", "管理区代码或格式被手工修改，拒绝覆盖。")
        prefix, suffix = source[:start - 1], source[end + 1:]
        cls.check_base(prefix + suffix)
        return cls(profile_id, prefix, suffix, rules, True)

    @staticmethod
    def parse_data(encoded: str, profile_id: str) -> tuple[Rule, ...]:
        try:
            data = json.loads(encoded)
        except ValueError as error:
            raise ToolError("managed_block_invalid", "管理区不是有效 JSON。") from error
        if not isinstance(data, dict) or set(data) != {"schema", "profile_id", "rules"}:
            raise ToolError("managed_block_invalid", "管理区结构不受支持。")
        if type(data["schema"]) is not int or data["schema"] != 1 or data["profile_id"] != profile_id:
            raise ToolError("managed_block_invalid", "管理区版本或所属订阅不匹配。")
        return validate_rules(data["rules"])

    @classmethod
    def bootstrap(cls, source: str, profile_id: str) -> ScriptDocument:
        matches = list(MAIN.finditer(source))
        if len(matches) != 1 or len(re.findall(r"\bmain\s*\(", source)) != 1:
            raise ToolError("unsupported_script", "仅支持唯一的 function main(config, profileName) 入口，未修改。")
        if "__clash_direct_" in source or "clash-direct:managed:" in source:
            raise ToolError("namespace_conflict", "脚本已有同名管理标识，拒绝接入。")
        start, end = matches[0].span(1)
        prefix = source[:start] + BASE_MAIN + source[end:]
        return cls(profile_id, prefix, "", (), False)

    @staticmethod
    def check_base(source: str) -> None:
        declarations = re.findall(r"^function\s+" + re.escape(BASE_MAIN) + r"\s*\(", source, re.MULTILINE)
        if len(declarations) != 1 or MAIN.search(source):
            raise ToolError("unsupported_script", "原脚本入口发生变化，拒绝覆盖。")

    def render(self, rules: tuple[Rule, ...]) -> str:
        validate_rules([rule.line for rule in rules])
        return self.prefix + "\n" + render_block(self.profile_id, rules) + self.suffix
