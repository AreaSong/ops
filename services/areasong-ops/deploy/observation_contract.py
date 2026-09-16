"""历史观察计划维护的只读授权与行绑定，不迁移 schema、不写审计。"""

from __future__ import annotations

import datetime as dt
import hashlib
import json
import re
import sqlite3

from release_snapshot import ACTIVE_STATES


UTC = dt.timezone.utc
PURPOSE = "areasong-ops.observation-escalation"
AUDIT_EVENT = "plan.observation_escalated"
REASON = "观察期已结束，当前运行身份与原目标不一致；转人工关注，未完成原计划验收"


def version(value: str) -> str:
    match = re.fullmatch(r"v?((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)", value)
    if not match:
        raise ValueError("版本身份格式无效")
    return match[1]


def digest(value: object) -> str:
    encoded = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False, allow_nan=False)
    return "sha256:" + hashlib.sha256(encoded.encode()).hexdigest()


def timestamp(value: str) -> dt.datetime:
    result = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    if result.tzinfo is None:
        raise ValueError("时间必须带时区")
    return result.astimezone(UTC)


def subject(value: str) -> str:
    value = value.strip().lower()
    return hashlib.sha256(value.encode()).hexdigest() if "@" in value else value


def usable(item: dict, now: dt.datetime) -> bool:
    return (item.get("status", "active") in {"", "active"}
            and (not item.get("expiresAt") or now < timestamp(item["expiresAt"])))


def binding_matches(binding: dict, actor: str, tenant: str, object_id: str, now: dt.datetime) -> bool:
    objects = binding.get("objectIds") or []
    return (subject(binding.get("subject", "")) == actor
            and binding.get("tenantId") in {tenant, "*"} and usable(binding, now)
            and (not objects or object_id in objects or "*" in objects))


def authorization(connection: sqlite3.Connection, actor: str, service: dict, now: dt.datetime) -> dict:
    if not re.fullmatch(r"[a-f0-9]{64}", actor):
        raise ValueError("操作者必须是当前认证主体的 SHA-256，不接受邮箱或任意标识")
    row = connection.execute("SELECT version,digest,policy_json FROM access_policy_snapshots ORDER BY version DESC LIMIT 1").fetchone()
    if row is None:
        raise ValueError("缺少数据库权威权限快照")
    policy = json.loads(row["policy_json"])
    principal = policy.get("principals", {}).get(actor)
    if policy.get("enforced") is not True or not principal or not usable(principal, now):
        raise ValueError("操作者未登记、已停用、过期或权限未启用")
    tenant = principal.get("tenantId") or policy.get("defaultTenant", "default")
    if tenant != service["tenantId"]:
        raise ValueError("拒绝跨租户维护")
    tenants = policy.get("tenants") or {}
    if (tenants and (tenant not in tenants or not usable(tenants[tenant], now))
            or not tenants and tenant != policy.get("defaultTenant", "default")):
        raise ValueError("租户未启用")
    bindings = policy.get("bindings") or []
    if principal.get("jit") and not any(
            b.get("jit") and subject(b.get("subject", "")) == actor
            and b.get("tenantId") in {tenant, "*"} and usable(b, now) for b in bindings):
        raise ValueError("操作者缺少有效 JIT 授权")
    roles = policy.get("roles") or {}
    permits = lambda role: bool({"ops.deploy", "*"} & set(role.get("permissions") or []))
    allowed = any(permits(roles.get(role, {})) for role in principal.get("roles") or [])
    allowed |= any(binding_matches(b, actor, tenant, service["objectId"], now)
                   and permits(roles.get(b.get("roleId"), {})) for b in bindings)
    dynamic = [dict(r) for r in connection.execute(
        "SELECT rb.role_id,rb.tenant_id,rb.object_ids_json,rb.expires_at,"
        "COALESCE(r.permissions_json,'[]') AS permissions_json FROM role_bindings rb "
        "LEFT JOIN roles r ON r.id=rb.role_id WHERE rb.subject=? "
        "AND (rb.tenant_id=? OR rb.tenant_id='*') ORDER BY rb.id", (actor, tenant))]
    for binding in dynamic:
        objects = json.loads(binding["object_ids_json"])
        valid = not binding["expires_at"] or now < timestamp(binding["expires_at"])
        allowed |= (valid and (not objects or service["objectId"] in objects or "*" in objects)
                    and bool({"ops.deploy", "*"} & set(json.loads(binding["permissions_json"]))))
    if not allowed:
        raise ValueError("当前权限没有 ops.deploy")
    return {"version": row["version"], "digest": row["digest"],
            "policyHash": digest(policy), "dynamicHash": digest(dynamic)}


def plans(connection: sqlite3.Connection, ids: list[str], actor: str, catalog: dict, now: dt.datetime) -> list[dict]:
    result = []
    for plan_id in ids:
        row = connection.execute("SELECT * FROM release_plans WHERE id=?", (plan_id,)).fetchone()
        if row is None:
            raise ValueError("计划不存在")
        plan = dict(row)
        task_row = connection.execute("SELECT * FROM tasks WHERE id=?", (plan["task_id"],)).fetchone()
        if task_row is None:
            raise ValueError("关联任务不存在")
        task = dict(task_row)
        validate_plan(plan, task, actor, now)
        service = catalog.get("services", {}).get(plan["service"])
        if not service or service.get("objectId") != "service:" + plan["service"]:
            raise ValueError("受管服务身份不一致")
        if plan["tenant_id"] != service.get("tenantId") or plan["server_id"] != service.get("serverId"):
            raise ValueError("历史计划租户/服务器与目录不一致，不能自动补写")
        result.append({"id": plan_id, "service": plan["service"], "target": plan["target"],
                       "taskId": task["id"], "planDigest": plan["digest"],
                       "planHash": digest(plan), "taskHash": digest(task),
                       "silenceId": plan["maintenance_silence_id"],
                       "authorization": authorization(connection, actor, service, now)})
    return result


def validate_plan(plan: dict, task: dict, actor: str, now: dt.datetime) -> None:
    if plan["actor_hash"] != actor:
        raise ValueError("只有原创建者可以申请本维护操作")
    if plan["state"] != "observing" or plan["action"] != "update" or task["state"] != "succeeded":
        raise ValueError("只处理已成功执行的更新观察计划")
    if not plan["observation_ends_at"] or now <= timestamp(plan["observation_ends_at"]):
        raise ValueError("观察期尚未结束")
    if not task["finished_at"] or timestamp(task["finished_at"]) > now:
        raise ValueError("任务完成时间无效")
    if (task["plan_id"] != plan["id"] or task["plan_digest"] != plan["digest"]
            or any(task[k] != plan[k] for k in ("service", "action", "target"))):
        raise ValueError("计划与任务双向绑定不一致")
    if plan["closed_at"] or plan["closure_idempotency_key"]:
        raise ValueError("计划已有不一致的成功收口记录")


def require_idle(connection: sqlite3.Connection, selected: list[str]) -> None:
    for table, states in ACTIVE_STATES.items():
        parameters = list(states)
        sql = f"SELECT COUNT(*) FROM {table} WHERE state IN ({','.join('?' for _ in states)})"
        if table == "release_plans":
            sql += f" AND id NOT IN ({','.join('?' for _ in selected)})"
            parameters += selected
        if connection.execute(sql, parameters).fetchone()[0]:
            raise ValueError("控制面仍有其他活动状态: " + table)
    if connection.execute("SELECT COUNT(*) FROM kubernetes_operations WHERE state='pending' OR finished_at IS NULL").fetchone()[0]:
        raise ValueError("Kubernetes 写入状态不明")
    if connection.execute("SELECT COUNT(*) FROM tasks WHERE state='recovery_uncertain'").fetchone()[0]:
        raise ValueError("存在恢复不确定任务")
