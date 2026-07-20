#!/usr/bin/env python3
"""Agent 治理一致性校验。

守护 Agent 导航体系的四条结构不变量，防止"遗忘型"漂移：

1. 路由表同步：四个薄壳入口的 Quick Routing 表必须与 AGENTS.md 逐字一致；
2. 路由路径存在：路由表引用的文件/目录必须真实存在；
3. runbooks 分层：runbooks/ 根目录只允许索引与模板，其余必须归入 playbooks/ 或 records/；
4. 全库死链：所有 markdown 相对链接必须可解析（gotchas 锚点、standards 互引等）。
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

ROUTING_SOURCE = "AGENTS.md"
SHELL_FILES = (
    "CLAUDE.md",
    "CODEX.md",
    "GEMINI.md",
    ".cursor/rules/ops-routing.mdc",
)
RUNBOOKS_ROOT_ALLOWED = {"README.md", "postmortem-template.md", "gotchas.md"}
LINK_RE = re.compile(r"\]\(([^)#\s]+)(?:#[^)]*)?\)")


def extract_routing_table(path: Path) -> list[str]:
    """提取以 '| 任务 ' 开头、至空行结束的路由表块。"""
    table: list[str] = []
    in_table = False
    for line in path.read_text(encoding="utf-8").split("\n"):
        if line.startswith("| 任务 "):
            in_table = True
        if in_table:
            if not line.strip():
                break
            table.append(line)
    return table


def check_routing_sync(errors: list[str]) -> list[str]:
    source = extract_routing_table(ROOT / ROUTING_SOURCE)
    if not source:
        errors.append(f"{ROUTING_SOURCE}: 未找到路由表（应有以 '| 任务 ' 开头的表格）")
        return []
    for shell in SHELL_FILES:
        shell_path = ROOT / shell
        if not shell_path.exists():
            errors.append(f"薄壳入口缺失: {shell}")
            continue
        if extract_routing_table(shell_path) != source:
            errors.append(f"{shell}: 路由表与 {ROUTING_SOURCE} 不一致，请从 {ROUTING_SOURCE} 同步")
    return source


def check_routing_paths(routing_table: list[str], errors: list[str]) -> None:
    for line in routing_table:
        for ref in re.findall(r"`([^`]+)`", line):
            # 只校验形如路径的引用，跳过“按对应域 standards 执行”等说明文字
            if "/" not in ref and not ref.endswith((".md", ".yml", ".yaml")):
                continue
            if not (ROOT / ref).exists():
                errors.append(f"{ROUTING_SOURCE} 路由表引用不存在: {ref}")


def check_runbooks_layout(errors: list[str]) -> None:
    for md in sorted((ROOT / "runbooks").glob("*.md")):
        if md.name not in RUNBOOKS_ROOT_ALLOWED:
            errors.append(
                f"runbooks/{md.name} 未归类：可复用流程进 playbooks/，一次性记录进 records/"
                "（见 runbooks/README.md 目录约定）"
            )


def check_markdown_links(errors: list[str]) -> None:
    for md in sorted(ROOT.rglob("*.md")):
        if ".git" in md.parts or "node_modules" in md.parts:
            continue
        for lineno, line in enumerate(md.read_text(encoding="utf-8").split("\n"), 1):
            for match in LINK_RE.finditer(line):
                target = match.group(1)
                if target.startswith(("http://", "https://", "mailto:")):
                    continue
                if not (md.parent / target).resolve().exists():
                    errors.append(f"{md.relative_to(ROOT)}:{lineno}: markdown 死链 -> {target}")


def main() -> int:
    errors: list[str] = []
    routing_table = check_routing_sync(errors)
    if routing_table:
        check_routing_paths(routing_table, errors)
    check_runbooks_layout(errors)
    check_markdown_links(errors)
    if errors:
        print("Agent governance validation failed:", file=sys.stderr)
        for err in errors:
            print(f"  - {err}", file=sys.stderr)
        return 1
    print("Agent governance validation passed.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
