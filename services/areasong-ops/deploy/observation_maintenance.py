#!/usr/bin/env python3
"""显式升级历史观察异常，不执行业务动作，也不把异常计划写成 completed。

仅在已验证的 Linux/Python 3.12+ 维护窗口使用 python3 -I -B 运行。本工具不停止或启动服务。
preview/status 只读，但同样要求停机且不存在 WAL/SHM，避免只读 SQLite 更新共享索引。
发布锁文件必须已在批准的准备步骤创建，读取全程持有共享锁，不自动创建或修复锁。
apply 需要原创建者当前权限、已批准请求的 root-only 回执、
同一主机/启动/数据库/配置/计划摘要、控制面停稳及完整 SQLite 备份。
审批回执只是外部人工批准的记录，不能代替仓库要求的变更单批准。
失败保留备份和现场；提交结果不明时先 status，禁止直接重发写操作或覆盖旧快照。
"""

from __future__ import annotations

import sys
from pathlib import Path

sys.dont_write_bytecode = True
sys.path.insert(0, str(Path(__file__).resolve().parent))

import argparse
import datetime as dt
import json
import os
import re
import sqlite3
import uuid

from observation_contract import AUDIT_EVENT, PURPOSE, REASON, UTC, authorization, digest, plans, require_idle, timestamp, version
from observation_runtime import MaintenanceRuntime, private_file, read_json


def identifiers(ids: list[str]) -> list[str]:
    if not 1 <= len(ids) <= 2 or len(set(ids)) != len(ids):
        raise ValueError("必须明确选择一至两个不重复的计划")
    if any(str(uuid.UUID(value)) != value for value in ids):
        raise ValueError("计划 ID 必须为规范 UUID")
    return sorted(ids)


def now() -> dt.datetime:
    return dt.datetime.now(UTC)


def collect(runtime: MaintenanceRuntime, connection, actor: str, ids: list[str], clock: dt.datetime) -> tuple[dict, list]:
    catalog, binding = runtime.base()
    rows = plans(connection, ids, actor, catalog, clock)
    require_idle(connection, ids)
    for row in rows:
        service = catalog["services"][row["service"]]
        if service["serverId"].lower() != binding["host"].lower():
            raise ValueError("服务目录不是本机目标，拒绝跨服务器维护")
        row["observed"] = runtime.observe(service, row["silenceId"])
        if version(row["observed"]["version"]) == version(row["target"]):
            raise ValueError("当前版本仍符合原计划，应使用正常收口而不是异常维护")
    if runtime.base()[1] != binding:
        raise ValueError("取证期间主机、数据库或配置发生变化")
    return binding, rows


def preview(runtime: MaintenanceRuntime, actor: str, ids: list[str], approval_ref: str) -> dict:
    ids = identifiers(ids)
    if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]{2,99}", approval_ref):
        raise ValueError("需要明确的外部审批引用，不接受自由文本或空引用")
    created = now()
    with runtime.connect() as connection:
        connection.execute("BEGIN")
        binding, rows = collect(runtime, connection, actor, ids, created)
    result = {"schemaVersion": 1, "purpose": PURPOSE, "operationId": str(uuid.uuid4()),
              "actorHash": actor, "approvalRef": approval_ref, "createdAt": created.isoformat(),
              "expiresAt": (created + dt.timedelta(minutes=15)).isoformat(),
              "targetState": "needs_attention", "binding": binding, "plans": rows}
    result["requestDigest"] = digest(result)
    return result


def validate_request(request: dict, *, check_time=True) -> list[str]:
    expected = {"schemaVersion", "purpose", "operationId", "actorHash", "approvalRef", "createdAt",
                "expiresAt", "targetState", "binding", "plans", "requestDigest"}
    if (set(request) != expected or type(request["schemaVersion"]) is not int
            or request["schemaVersion"] != 1 or request["purpose"] != PURPOSE
            or request["targetState"] != "needs_attention"):
        raise ValueError("维护请求用途或结构无效")
    if str(uuid.UUID(request["operationId"])) != request["operationId"]:
        raise ValueError("操作 ID 无效")
    if (not re.fullmatch(r"[a-f0-9]{64}", request["actorHash"])
            or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._/-]{2,99}", request["approvalRef"])):
        raise ValueError("维护主体或审批引用无效")
    unsigned = {key: value for key, value in request.items() if key != "requestDigest"}
    if digest(unsigned) != request["requestDigest"]:
        raise ValueError("维护请求摘要不匹配")
    created, expires = timestamp(request["createdAt"]), timestamp(request["expiresAt"])
    if expires - created != dt.timedelta(minutes=15) or check_time and not created <= now() < expires:
        raise ValueError("维护请求已过期或时间窗口无效")
    return identifiers([row["id"] for row in request["plans"]])


def validate_approval(request: dict, approval: dict) -> None:
    expected = {"purpose", "requestDigest", "operationId", "actorHash", "approvalRef", "approvedAt", "expiresAt"}
    if set(approval) != expected or approval["purpose"] != PURPOSE + ".approval":
        raise ValueError("审批回执结构或用途无效")
    for key in ("requestDigest", "operationId", "actorHash", "approvalRef"):
        if approval[key] != request[key]:
            raise ValueError("审批回执与固定请求不匹配")
    approved, expires = timestamp(approval["approvedAt"]), timestamp(approval["expiresAt"])
    if not timestamp(request["createdAt"]) <= approved <= now() < expires <= timestamp(request["expiresAt"]):
        raise ValueError("审批不在本次预览的有效窗口")


def receipt(runtime: MaintenanceRuntime, connection, request: dict) -> dict | None:
    records = []
    catalog, _ = runtime.base()
    for item in request["plans"]:
        matched = 0
        authorization(connection, request["actorHash"], catalog["services"][item["service"]], now())
        rows = connection.execute("SELECT detail_json FROM audit_entries WHERE event=? AND resource=? AND actor_hash=?",
                                  (AUDIT_EVENT, item["id"], request["actorHash"]))
        for row in rows:
            detail = json.loads(row[0])
            if detail.get("operationId") == request["operationId"]:
                matched += 1
                if matched != 1:
                    raise ValueError("同一计划出现重复维护回执")
                if detail.get("requestDigest") != request["requestDigest"]:
                    raise ValueError("操作 ID 已绑定其他请求")
                actual = connection.execute("SELECT * FROM release_plans WHERE id=?", (item["id"],)).fetchone()
                if actual is None or digest(dict(actual)) != detail.get("afterHash"):
                    raise ValueError("已有回执但当前计划漂移，须人工核对")
                records.append(detail)
    if not records:
        return None
    if len(records) != len(request["plans"]):
        raise ValueError("维护审计集合不完整，不得重放写入")
    return {"status": "already_escalated", "operationId": request["operationId"],
            "plans": [row["id"] for row in request["plans"]], "backup": records[0]["backup"]}


def status(runtime: MaintenanceRuntime, request: dict) -> dict:
    validate_request(request, check_time=False)
    with runtime.connect() as connection:
        connection.execute("BEGIN")
        current = runtime.base()[1]
        if any(current[key] != request["binding"][key] for key in ("database", "host")):
            raise ValueError("主机/数据库身份已改变，不能判定原操作是否提交")
        return receipt(runtime, connection, request) or {"status": "not_applied", "operationId": request["operationId"]}


def update_rows(connection, request: dict, backup: dict) -> None:
    updated = now().isoformat()
    for item in request["plans"]:
        before = dict(connection.execute("SELECT * FROM release_plans WHERE id=?", (item["id"],)).fetchone())
        if digest(before) != item["planHash"]:
            raise ValueError("事务内计划漂移")
        result = connection.execute(
            "UPDATE release_plans SET state='needs_attention',closure_reason=?,updated_at=? "
            "WHERE id=? AND state='observing' AND digest=? AND task_id=?",
            (REASON, updated, item["id"], item["planDigest"], item["taskId"]))
        if result.rowcount != 1:
            raise ValueError("条件更新没有精确命中一条计划")
        after = dict(connection.execute("SELECT * FROM release_plans WHERE id=?", (item["id"],)).fetchone())
        expected = {**before, "state": "needs_attention", "closure_reason": REASON, "updated_at": updated}
        if after != expected:
            raise ValueError("更新影响了三列之外的历史字段")
        detail = {"operationId": request["operationId"], "requestDigest": request["requestDigest"],
                  "approvalRef": request["approvalRef"], "from": "observing", "to": "needs_attention",
                  "previousClosureReason": before["closure_reason"], "plan": item,
                  "afterHash": digest(after), "backup": backup, "reason": REASON}
        connection.execute(
            "INSERT INTO audit_entries(occurred_at,actor_hash,event,resource,outcome,detail_json) VALUES(?,?,?,?,?,?)",
            (updated, request["actorHash"], AUDIT_EVENT, item["id"], "needs_attention",
             json.dumps(detail, sort_keys=True, ensure_ascii=False)))


def revalidate_live(runtime: MaintenanceRuntime, connection, request: dict) -> None:
    catalog, binding = runtime.base()
    if binding != request["binding"]:
        raise ValueError("提交前主机/数据库/配置漂移")
    for item in request["plans"]:
        service = catalog["services"][item["service"]]
        if authorization(connection, request["actorHash"], service, now()) != item["authorization"]:
            raise ValueError("提交前当前授权已变化")
        if runtime.observe(service, item["silenceId"]) != item["observed"]:
            raise ValueError("提交前服务或静默身份已变化")
        task = connection.execute("SELECT * FROM tasks WHERE id=?", (item["taskId"],)).fetchone()
        if task is None or digest(dict(task)) != item["taskHash"]:
            raise ValueError("提交前任务历史漂移")


def apply(runtime: MaintenanceRuntime, request: dict, approval: dict) -> dict:
    ids = validate_request(request, check_time=False)
    prior = status(runtime, request)
    if prior["status"] == "already_escalated":
        return prior  # 只读确认，不重做备份、更新或审计。
    validate_request(request)
    validate_approval(request, approval)
    with runtime.locked():
        runtime.stopped()
        with runtime.connect(write=True) as connection:
            try:
                connection.execute("BEGIN IMMEDIATE")
                binding, rows = collect(runtime, connection, request["actorHash"], ids, now())
                if binding != request["binding"] or rows != request["plans"]:
                    raise ValueError("预览后权限、计划、任务或运行身份发生变化")
                runtime.stopped()
                backup = runtime.backup(request["operationId"])
                runtime.stopped()
                if collect(runtime, connection, request["actorHash"], ids, now()) != (binding, rows):
                    raise ValueError("备份后权限、计划或运行态发生变化")
                update_rows(connection, request, backup)
                runtime.stopped()
                validate_request(request)
                validate_approval(request, approval)
                revalidate_live(runtime, connection, request)
                receipt(runtime, connection, request)
                catalog, final_binding = runtime.base()
                if final_binding != request["binding"]:
                    raise ValueError("最终提交前运行绑定已变化")
                runtime.stopped()
                for item in request["plans"]:
                    if authorization(connection, request["actorHash"], catalog["services"][item["service"]], now()) != item["authorization"]:
                        raise ValueError("最终提交前授权已变化")
                # 最后的网络/运行态读取之后才检查纯时间门禁。
                validate_request(request)
                validate_approval(request, approval)
                connection.commit()
            except BaseException:
                if connection.in_transaction:
                    connection.rollback()
                raise
    return {"status": "escalated", "operationId": request["operationId"], "plans": ids, "backup": backup}


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    plan = commands.add_parser("preview", help="零写入输出十五分钟有效的固定请求")
    plan.add_argument("--actor-hash", required=True)
    plan.add_argument("--plan-id", action="append", required=True)
    plan.add_argument("--approval-ref", required=True)
    execute = commands.add_parser("apply", help="只把固定请求中的计划转人工关注；不会停止/启动服务")
    execute.add_argument("--request", type=Path, required=True)
    execute.add_argument("--approval", type=Path, required=True)
    read = commands.add_parser("status", help="只读检查事务审计，不重放写入")
    read.add_argument("--request", type=Path, required=True)
    args = parser.parse_args()
    if sys.platform != "linux" or sys.version_info < (3, 12):
        raise ValueError("维护入口仅支持已验收的 Linux/Python 3.12+，其他平台不执行")
    if os.geteuid() != 0 or not sys.flags.isolated or not sys.flags.dont_write_bytecode:
        raise ValueError("必须由 root 使用 python3 -I -B 运行；不得以环境变量绕过")
    for name in ("observation_maintenance.py", "observation_contract.py", "observation_runtime.py"):
        private_file(Path(__file__).parent / name, secret=False)
    runtime = MaintenanceRuntime()
    if args.command == "preview":
        result = preview(runtime, args.actor_hash, args.plan_id, args.approval_ref)
    else:
        request = read_json(args.request)
        result = (apply(runtime, request, read_json(args.approval)) if args.command == "apply" else status(runtime, request))
    print(json.dumps(result, sort_keys=True, ensure_ascii=False))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, sqlite3.Error, RuntimeError) as error:
        print("维护已停止；若可能已提交，先 status 核对审计：" + str(error), file=sys.stderr)
        raise SystemExit(1)
