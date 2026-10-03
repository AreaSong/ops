"""显式离线组装辅助函数；默认检查入口不导入或执行本模块。无保存/应用接口。"""
import copy
import hashlib
import json
import os
import shutil
import subprocess
from pathlib import Path

from source import AuditError
from topology import BEGIN, END, SG, SG_IDENTITY, identity


LEGACY_HELPER_SHA256 = "7ccb7e5308e6f0affaad58d49c4a424d75f15b441f5ae0a18c10a34f029aabf2"
RUNNER = r"""
const fs = require('node:fs');
const vm = require('node:vm');
try {
  const input = JSON.parse(fs.readFileSync(0, 'utf8'));
  const context = vm.createContext({});
  vm.runInContext(input.script, context, {timeout: 2000});
  // vm 仅隔离变量；真正的网络/文件写入限制由外层 macOS 沙箱执行。
  const code = 'main(' + JSON.stringify(input.config) + ', "sub")';
  const result = vm.runInContext(code, context, {timeout: 2000});
  process.stdout.write(JSON.stringify(result));
} catch {
  process.stderr.write('offline script evaluation failed');
  process.exitCode = 1;
}
"""


def build_script(original):
    start, end = "function simplifyLosAngelesNode(", "function routeForeignWebFixes("
    if original.count(start) != 1 or original.count(end) != 1 or BEGIN in original or END in original:
        raise AuditError("candidate_source", "原脚本边界变化，停止候选组装。")
    left, right = original.index(start), original.index(end)
    digest = hashlib.sha256(original[left:right].encode()).hexdigest()
    if digest != LEGACY_HELPER_SHA256:
        raise AuditError("candidate_source", "原单节点辅助函数指纹变化，须重新核对后再组装。")
    replacement = Path(__file__).with_name("candidate_topology.js").read_text()
    return original[:left] + replacement + "\n" + original[right:]


def append_backup(extension, saved_nodes):
    matches = [n for n in saved_nodes if isinstance(n, dict) and n.get("name") == "🇸🇬 Singapore 01"]
    if len(matches) != 1 or not identity(matches[0], SG_IDENTITY):
        raise AuditError("candidate_source", "指定 E-IX 节点缺失、重名或传输身份变化。")
    if any(n.get("name") == SG for k in ("prepend", "append") for n in extension[k]):
        raise AuditError("candidate_duplicate", "proxies 扩展已存在同名候选，拒绝重复追加。")
    result = copy.deepcopy(extension)
    result["append"].append({**copy.deepcopy(matches[0]), "name": SG})
    return result


def merge_mapping(target, extra):
    for key, value in extra.items():
        if isinstance(value, dict) and isinstance(target.get(key), dict):
            merge_mapping(target[key], value)
        else:
            target[key] = copy.deepcopy(value)
    return target


def compose_input(source, extensions, global_merge):
    """本阶段无 provider、空组/节点删除列表；顺序另以旧脚本重现活动结果核验。"""
    result = copy.deepcopy(source)
    merge_mapping(result, global_merge)
    merge_mapping(result, extensions["merge"])
    for role, field in (("rules", "rules"), ("proxies", "proxies"), ("groups", "proxy-groups")):
        ext = extensions[role]
        if ext["delete"]:
            raise AuditError("candidate_extension", "离线组装不支持非空 delete；停止并核对扩展语义。")
        result[field] = copy.deepcopy(ext["prepend"] + result[field] + ext["append"])
    return result


def execute_script(script, config, node="node"):
    env = {k: v for k, v in os.environ.items() if k not in {"NODE_OPTIONS", "NODE_PATH"}}
    try:
        executable = shutil.which(node)
        if not executable:
            raise ValueError()
        sandbox = '(version 1)(allow default)(deny network*)(deny file-write*)'
        result = subprocess.run(["/usr/bin/sandbox-exec", "-p", sandbox, executable, "-e", RUNNER],
                                input=json.dumps({"script": script, "config": config}),
                                text=True, capture_output=True, timeout=10, env=env)
        if result.returncode:
            raise ValueError()
        value = json.loads(result.stdout)
        if not isinstance(value, dict):
            raise ValueError()
        return value
    except (OSError, subprocess.SubprocessError, ValueError):
        raise AuditError("candidate_execution", "离线脚本执行失败；源码、输入与原始输出均不回显。") from None
