from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Optional

from .errors import ToolError
from .profile import Target, digest, load_target, read_regular
from .rules import MAX_RULES, Rule
from .runtime import Tools
from .script import ScriptDocument
from .storage import StateStore, atomic_write


@dataclass(frozen=True)
class Snapshot:
    target: Target
    document: ScriptDocument


@dataclass(frozen=True)
class Plan:
    snapshot: Snapshot
    desired: tuple[Rule, ...]
    operation: str
    source: str
    checks: dict
    expected_head: Optional[str]
    undo_parent: Optional[str] = None

    @property
    def changed(self) -> bool:
        return self.snapshot.document.rules != self.desired

    def describe(self) -> dict:
        before = self.snapshot.document.rules
        return {
            "target": self.snapshot.target.describe(), "operation": self.operation,
            "changed": self.changed, "install_layer": self.changed and not self.snapshot.document.installed,
            "before_count": len(before), "after_count": len(self.desired),
            "add": [rule.describe() for rule in self.desired if rule not in before],
            "remove": [rule.describe() for rule in before if rule not in self.desired],
            "checks": self.checks, "reload_triggered": False, "runtime_verified": False,
        }


class Manager:
    def __init__(self, app_dir: Path, state_dir: Path, tools: Tools) -> None:
        self.app_dir = app_dir
        self.tools = tools
        self.store = StateStore(state_dir)
        if self.store.directory.resolve().is_relative_to(app_dir.expanduser().resolve()):
            raise ToolError("unsafe_state_dir", "备份目录不能放进 Clash 的配置目录。")

    def snapshot(self) -> Snapshot:
        target = load_target(self.app_dir)
        return Snapshot(target, ScriptDocument.parse(target.source, target.profile_id))

    def prepare(self, snapshot: Snapshot, desired: tuple[Rule, ...], operation: str) -> Plan:
        if not snapshot.target.active:
            raise ToolError("inactive_profile", "请先在 Clash Verge 启用 sub；工具不会切换订阅。")
        if len(desired) > MAX_RULES or len(set(desired)) != len(desired):
            raise ToolError("invalid_rule", f"直连清单必须唯一且不超过 {MAX_RULES} 条。")
        head = self.store.head(snapshot.target.profile_id)
        if head is not None:
            record = self.store.load_record(snapshot.target, head)
            if record["after_rules"] != [rule.line for rule in snapshot.document.rules]:
                raise ToolError("history_conflict", "备份历史与当前清单不一致，请先检查，不继续保存。", exit_code=3)
        if desired == snapshot.document.rules:
            return Plan(snapshot, desired, operation, snapshot.target.source, {}, head)
        source = snapshot.document.render(desired)
        checks = self.tools.validate(source, [rule.line for rule in desired])
        return Plan(snapshot, desired, operation, source, checks, head)

    def add(self, rule: Rule) -> Plan:
        snapshot = self.snapshot()
        rules = snapshot.document.rules
        return self.prepare(snapshot, rules if rule in rules else rules + (rule,), "add")

    def remove(self, rule: Rule) -> Plan:
        snapshot = self.snapshot()
        return self.prepare(snapshot, tuple(item for item in snapshot.document.rules if item != rule), "remove")

    def undo(self) -> Plan:
        snapshot = self.snapshot()
        head = self.store.head(snapshot.target.profile_id)
        if head is None:
            raise ToolError("no_history", "没有可撤销的本工具变更。")
        record = self.store.load_record(snapshot.target, head)
        if record["after_rules"] != [rule.line for rule in snapshot.document.rules]:
            raise ToolError("history_conflict", "当前清单与撤销记录不一致，不自动恢复。", exit_code=3)
        desired = tuple(Rule.parse(line) for line in record["before_rules"])
        plan = self.prepare(snapshot, desired, "undo")
        if plan.expected_head != head:
            raise ToolError("conflict", "撤销历史已变化，请重新查看。", exit_code=3)
        return Plan(plan.snapshot, plan.desired, plan.operation, plan.source,
                    plan.checks, plan.expected_head, record["parent"])

    def verify_current(self, plan: Plan) -> None:
        current = self.snapshot()
        if not current.target.active or current.target.identity() != plan.snapshot.target.identity():
            raise ToolError("conflict", "预览后 sub 或脚本已变化；未覆盖，请重新查看。", exit_code=3)
        if self.store.head(current.target.profile_id) != plan.expected_head:
            raise ToolError("conflict", "预览后撤销历史已变化，请重新查看。", exit_code=3)

    def write_source(self, plan: Plan) -> None:
        # 临提交再次检查，保护人工编辑和不使用本工具锁的其他程序。
        current = load_target(self.app_dir)
        if not current.active or current.identity() != plan.snapshot.target.identity():
            raise ToolError("conflict", "提交前脚本发生变化，未覆盖。", exit_code=3)
        atomic_write(current.path, plan.source.encode("utf-8"), current.mode)

    def interrupted_write(self, plan: Plan, record: Optional[dict], cause: BaseException) -> ToolError:
        try:
            raw, _ = read_regular(plan.snapshot.target.path)
            actual = digest(raw)
        except ToolError:
            actual = None
        expected = digest(plan.source.encode("utf-8"))
        details = {"backup": record.get("backup") if record else None, "reload_triggered": False}
        if actual == expected:
            return ToolError("saved_but_unfinished", "规则已保存，但备份索引收尾失败；请勿重复保存，先检查。",
                             {**details, "saved": True}, 5)
        if actual == plan.snapshot.target.sha256:
            code = "cancelled" if isinstance(cause, KeyboardInterrupt) else "write_failed"
            return ToolError(code, "本次保存未完成，Clash 脚本仍是原内容。", {**details, "saved": False}, 5)
        return ToolError("write_state_unknown", "写入状态无法确认，请勿自动重试或覆盖，先检查目标及备份。",
                         {**details, "saved": None}, 5)

    def commit(self, plan: Plan) -> dict:
        if not plan.changed:
            return {"changed": False, "saved": False, "reload_triggered": False,
                    "message": "清单无需修改；这不代表当前内核已重载。"}
        record = None
        with self.store.lock(plan.snapshot.target.profile_id):
            self.verify_current(plan)
            try:
                record = self.store.prepare_record(plan)
                self.write_source(plan)
                raw, _ = read_regular(plan.snapshot.target.path)
                if digest(raw) != digest(plan.source.encode("utf-8")):
                    raise ToolError("write_state_unknown", "保存后内容不一致。", exit_code=5)
                head = plan.undo_parent if plan.operation == "undo" else record["id"]
                self.store.finish(plan.snapshot.target.profile_id, record, head)
            except BaseException as error:
                raise self.interrupted_write(plan, record, error) from error
        return {"changed": True, "saved": True, "managed_count": len(plan.desired),
                "backup": record["backup"], "reload_required": True, "reload_triggered": False,
                "runtime_verified": False,
                "message": "已保存。请在 Clash Verge 的 sub 订阅上点击“重新激活订阅”。"}

    def doctor(self) -> dict:
        result = {"tools": self.tools.describe(), "ready": False, "issues": []}
        try:
            snapshot = self.snapshot()
            result.update({"target": snapshot.target.describe(), "installed": snapshot.document.installed,
                           "managed_count": len(snapshot.document.rules)})
            result["checks"] = self.tools.validate(snapshot.document.render(snapshot.document.rules),
                                                   [rule.line for rule in snapshot.document.rules])
            head = self.store.head(snapshot.target.profile_id)
            if head is not None:
                record = self.store.load_record(snapshot.target, head)
                if record["after_rules"] != [rule.line for rule in snapshot.document.rules]:
                    raise ToolError("history_conflict", "备份历史与当前清单不一致，请先检查。", exit_code=3)
            result["undo_available"] = head is not None
            if not snapshot.target.active:
                result["issues"].append({"code": "inactive_profile", "message": "sub 当前未启用；只读可用，保存会拒绝。"})
            result["ready"] = snapshot.target.active
        except ToolError as error:
            result["issues"].append(error.payload())
        return result
