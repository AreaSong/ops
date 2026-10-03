from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import Optional

from .errors import ToolError
from .manager import Manager
from .menu import RELOAD_NOTE, display_plan, run_menu
from .profile import DEFAULT_APP_DIR
from .rules import resolve
from .runtime import Tools


TOOL_DIR = Path(__file__).resolve().parents[1]
class Parser(argparse.ArgumentParser):
    def error(self, message: str) -> None:
        # argparse 的原始错误可能回显用户粘贴的带令牌网址。
        raise ToolError("arguments", "命令参数无效；请用 --help 查看用法。")


def common_arguments() -> argparse.ArgumentParser:
    common = argparse.ArgumentParser(add_help=False, argument_default=argparse.SUPPRESS)
    common.add_argument("--json", action="store_true", help="只向 stdout 输出结构化 JSON")
    common.add_argument("--app-dir", type=Path, help="Clash Verge 数据目录；默认读取当前用户目录")
    common.add_argument("--state-dir", type=Path, help="私有备份目录；默认工具目录中的 .state")
    common.add_argument("--node", help="Node 可执行文件路径，不会自动安装")
    common.add_argument("--core", help="Mihomo 可执行文件路径，不会自动安装")
    return common


def parser() -> Parser:
    common = common_arguments()
    root = Parser(prog="clash-direct", parents=[common], description="离线管理 sub 的自定义直连；仅保存文件，由你手动重载。")
    sub = root.add_subparsers(dest="command", parser_class=Parser)
    descriptions = {
        "menu": "打开交互菜单", "doctor": "只读检查依赖、目标和脚本兼容性",
        "target": "查看唯一目标 sub 的脚本绑定", "resolve": "把网址解析为域名/IP 规则，不保存",
        "list": "只列本工具管理的条目", "export": "只读导出原始 Clash 规则",
        "add": "预览并添加直连例外", "remove": "预览并移除自定义例外", "undo": "撤销上一次本工具修改",
    }
    for command, description in descriptions.items():
        child = sub.add_parser(command, parents=[common], help=description, description=description)
        if command in {"resolve", "add", "remove"}:
            child.add_argument("value", help="HTTP(S) 网址、域名或单个 IP")
            child.add_argument("--scope", choices=["exact", "suffix"], default="exact", help="默认 exact；suffix 包含子域名")
        if command in {"add", "remove", "undo"}:
            child.add_argument("--dry-run", action="store_true", help="预览和校验，不修改 Clash 或持久备份")
            child.add_argument("--yes", action="store_true", help="明确确认本次保存；默认需要交互确认")
    return root


def emit(command: Optional[str], data: dict, json_mode: bool) -> None:
    if json_mode:
        print(json.dumps({"ok": True, "command": command, "data": data}, ensure_ascii=False))
    else:
        print(json.dumps(data, ensure_ascii=False, indent=2))


def output_error(command: Optional[str], error: ToolError, json_mode: bool) -> int:
    payload = {"ok": False, "command": command, "error": error.payload()}
    if json_mode:
        print(json.dumps(payload, ensure_ascii=False))
    else:
        print(f"未继续：{error}", file=sys.stderr)
        if error.details.get("backup"):
            print(f"备份：{error.details['backup']}", file=sys.stderr)
    return error.exit_code


def write_command(manager: Manager, args, json_mode: bool) -> int:
    warnings = []
    if args.command == "undo":
        plan = manager.undo()
    else:
        rule, warnings = resolve(args.value, args.scope, manager.tools)
        plan = manager.add(rule) if args.command == "add" else manager.remove(rule)
    preview = {**plan.describe(), "warnings": warnings}
    if args.dry_run or not plan.changed:
        emit(args.command, {"preview": preview, "saved": False, "reload_triggered": False, "note": RELOAD_NOTE}, json_mode)
        return 0
    if not args.yes:
        if json_mode or not sys.stdin.isatty():
            raise ToolError("confirmation_required", "保存需要明确确认；先用 --dry-run 预览，再用 --yes。", {"preview": preview})
        for warning in warnings:
            print(warning)
        display_plan(plan)
        if input("确认备份并保存？[y/N] ").strip().lower() not in {"y", "yes", "是", "确认"}:
            emit(args.command, {"cancelled": True, "saved": False}, json_mode)
            return 0
    result = manager.commit(plan)
    emit(args.command, {**result, "warnings": warnings, "note": RELOAD_NOTE}, json_mode)
    return 0


def dispatch(args, json_mode: bool) -> int:
    tools = Tools.discover(getattr(args, "node", None), getattr(args, "core", None))
    if args.command == "resolve":
        rule, warnings = resolve(args.value, args.scope, tools)
        emit("resolve", {**rule.describe(), "warnings": warnings, "saved": False}, json_mode)
        return 0
    manager = Manager(getattr(args, "app_dir", DEFAULT_APP_DIR),
                      getattr(args, "state_dir", TOOL_DIR / ".state"), tools)
    if args.command == "menu":
        if json_mode:
            raise ToolError("arguments", "交互菜单不支持 --json；请使用具体子命令。")
        return run_menu(manager)
    if args.command == "doctor":
        result = manager.doctor()
        emit("doctor", result, json_mode)
        return 0 if result["ready"] else 2
    if args.command in {"add", "remove", "undo"}:
        return write_command(manager, args, json_mode)
    snapshot = manager.snapshot()
    data = {"target": snapshot.target.describe(), "source_layer_present": snapshot.document.installed,
            "rules": [rule.describe() for rule in snapshot.document.rules], "runtime_verified": False}
    if args.command == "export" and not json_mode:
        print("\n".join(rule.line for rule in snapshot.document.rules))
    else:
        if args.command == "export":
            data["rules"] = [rule.line for rule in snapshot.document.rules]
        emit(args.command, data, json_mode)
    return 0


def main(argv: Optional[list[str]] = None) -> int:
    argv = list(sys.argv[1:] if argv is None else argv)
    json_mode = "--json" in argv
    command = None
    try:
        root = parser()
        args = root.parse_args(argv)
        command = args.command
        if command is None:
            if not sys.stdin.isatty() or json_mode:
                root.print_help(sys.stderr)
                raise ToolError("arguments", "请指定子命令；交互使用 menu。")
            args.command = command = "menu"
        return dispatch(args, json_mode)
    except ToolError as error:
        return output_error(command, error, json_mode)
    except (EOFError, KeyboardInterrupt) as error:
        emit(command, {"cancelled": True, "saved": None, "reload_triggered": False,
                       "message": "操作中断；若已确认保存，请先检查清单，不要直接重复写入。"}, json_mode)
        return 130 if isinstance(error, KeyboardInterrupt) else 0
    except OSError:
        return output_error(command, ToolError("io_error", "本地文件操作失败，请检查权限与备份；不会自动重试。"), json_mode)
