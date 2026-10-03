from __future__ import annotations

from .errors import ToolError
from .manager import Manager, Plan
from .rules import Rule, resolve


RELOAD_NOTE = "本工具不触发重载。保存后在 Clash Verge 的 sub 订阅上点击“重新激活订阅”；自动更新订阅也可能应用扩展。"


def display_rules(rules: tuple[Rule, ...], write=print) -> None:
    if not rules:
        write("本工具清单为空；学校等原有直连规则不在此列表中。")
    for index, rule in enumerate(rules, 1):
        scope = "含子域名" if rule.scope == "suffix" else "仅此主机"
        write(f"{index:>3}. [{scope}] {rule.host}")


def display_plan(plan: Plan, write=print) -> None:
    data = plan.describe()
    write(f"\n目标：sub / {data['target']['profile_id']}")
    write(f"文件：{data['target']['script']}")
    if data["install_layer"]:
        write("首次保存将接入独立管理层；保留原脚本主体，仅包装其入口。")
    for key, label in (("add", "新增"), ("remove", "移除")):
        for item in data[key]:
            write(f"{label}：{item['rule']}")
    write(f"自定义条目：{data['before_count']} → {data['after_count']}")
    write("直连例外对主机的所有路径/端口生效，优先于原分流及拦截规则；不改 DNS、TUN 或节点。")
    write("删除只恢复原分流；若原规则或上级域名仍直连，删除后仍可能直连。")
    write(RELOAD_NOTE)


def confirm_and_save(manager: Manager, plan: Plan, read=input, write=print) -> None:
    if not plan.changed:
        write("清单没有变化，未写入文件。若此前还未重载，仍需手动应用。")
        return
    display_plan(plan, write)
    if read("确认备份并保存？[y/N] ").strip().lower() not in {"y", "yes", "是", "确认"}:
        write("已取消，未修改 Clash 文件。")
        return
    result = manager.commit(plan)
    write(result["message"])
    write(f"备份：{result['backup']}")


def add_interactive(manager: Manager, read=input, write=print) -> None:
    value = read("网址、域名或单个 IP（留空取消）：").strip()
    if not value:
        return
    rule, warnings = resolve(value, "exact", manager.tools)
    if rule.describe()["kind"] == "domain":
        scope = read("匹配方式：1 仅此域名；2 包含子域名 [默认 1]：").strip()
        if scope not in {"", "1", "2"}:
            raise ToolError("invalid_input", "请输入 1 或 2。")
        if scope == "2":
            rule = Rule(rule.host, "suffix")
            warnings.append(f"将覆盖 {rule.host} 及其全部子域名；不会自动扩大到上级域名。")
    for warning in warnings:
        write(warning)
    confirm_and_save(manager, manager.add(rule), read, write)


def indices(value: str, count: int) -> set[int]:
    result = set()
    for token in value.replace("，", ",").split(","):
        token = token.strip()
        if not token.isdecimal():
            raise ToolError("invalid_input", "请输入列表序号，多个序号用逗号分隔。")
        index = int(token)
        if not 1 <= index <= count:
            raise ToolError("invalid_input", "序号超出当前清单范围。")
        result.add(index - 1)
    return result


def remove_interactive(manager: Manager, read=input, write=print) -> None:
    snapshot = manager.snapshot()
    display_rules(snapshot.document.rules, write)
    if not snapshot.document.rules:
        return
    value = read("删除哪些序号？例如 1,3（留空取消）：").strip()
    if not value:
        return
    chosen = indices(value, len(snapshot.document.rules))
    desired = tuple(rule for index, rule in enumerate(snapshot.document.rules) if index not in chosen)
    confirm_and_save(manager, manager.prepare(snapshot, desired, "remove"), read, write)


def handle_choice(choice: str, manager: Manager, read=input, write=print) -> None:
    if choice == "1":
        add_interactive(manager, read, write)
    elif choice == "2":
        display_rules(manager.snapshot().document.rules, write)
    elif choice == "3":
        remove_interactive(manager, read, write)
    elif choice == "4":
        confirm_and_save(manager, manager.undo(), read, write)
    elif choice == "5":
        result = manager.doctor()
        write("本地校验通过；不代表内核已重载。" if result["ready"] else "检查未通过。")
        for issue in result["issues"]:
            write(issue["message"])
    elif choice == "6":
        for rule in manager.snapshot().document.rules:
            write(rule.line)
    else:
        write("请输入菜单中的序号。")


def run_menu(manager: Manager, read=input, write=print) -> int:
    write("Clash 直连规则管理器（仅 sub，本地离线）")
    write("“临时”表示手动添加、用完删除，没有自动到期。")
    while True:
        try:
            snapshot = manager.snapshot()
        except ToolError as error:
            write(f"无法进入菜单：{error}")
            return error.exit_code
        try:
            status = "当前启用" if snapshot.target.active else "当前未启用：只读可用，保存会拒绝"
            write(f"\n目标：sub（{status}）｜本工具条目：{len(snapshot.document.rules)}")
            write("1 添加直连  2 查看清单  3 删除条目  4 撤销上次修改  5 检查  6 导出规则  0 退出")
            choice = read("选择：").strip()
            if choice == "0":
                return 0
            handle_choice(choice, manager, read, write)
        except ToolError as error:
            write(f"未继续：{error}")
            if error.details.get("backup"):
                write(f"备份：{error.details['backup']}")
            if error.exit_code == 5:
                return 5
        except (EOFError, KeyboardInterrupt):
            write("\n已退出；未自动重载 Clash。")
            return 0
