#!/usr/bin/env python3
"""sub 分流只读检查入口。"""
import argparse
import json
import sys
from pathlib import Path

# 运行入口不在仓库或活动配置目录产生 Python 缓存。
sys.dont_write_bytecode = True

from checks import audit_offline
from report import Report
from source import AuditError, DEFAULT_DIR, load_sources


class Parser(argparse.ArgumentParser):
    def error(self, message):
        raise AuditError("arguments", "命令参数无效；请用 --help 查看用法（不回显输入）。")


def parser():
    result = Parser(description="本地只读检查 sub；默认离线，不执行扩展脚本或自动修复。")
    result.add_argument("--app-dir", type=Path, default=DEFAULT_DIR, help="Clash 数据目录，可用于隔离夹具")
    result.add_argument("--runtime", action="store_true", help="显式读取本机内核四个 GET 接口")
    result.add_argument("--socket", help="本机绝对 Unix socket 路径，必须与 --runtime 一起使用")
    result.add_argument("--json", action="store_true", help="输出 schema_version=1 JSON")
    return result


def run(args):
    report = Report(args.runtime)
    if args.socket and not args.runtime:
        report.add("input", "arguments", "unverified", "--socket 必须与 --runtime 一起使用。")
        return report.result()
    try:
        data = load_sources(args.app_dir.expanduser())
        report.active = data["active"]
        report.add("source", "target", "pass", "已唯一识别 sub、关联脚本及所需来源文件。")
        if not report.active:
            report.add("source", "active", "unverified", "sub 存在但当前未启用；不把其他订阅的生成配置或内核归属为 sub。")
            return report.result()
        report.add("source", "active", "pass", "当前索引启用 sub。")
        offline = audit_offline(report, data)
    except AuditError as error:
        report.add("offline", error.code, "issue" if error.code == "outbound_duplicate" else "unverified", error.message)
        return report.result()
    if args.runtime:
        from runtime import audit_runtime
        try:
            audit_runtime(report, data, offline, args.socket)
        except AuditError as error:
            report.add("runtime", error.code, "unverified", error.message)
    else:
        report.add("runtime", "not_requested", "unverified", "未指定 --runtime，未发现进程或调用控制接口。", False)
    return report.result()


def human_summary(result):
    checks = result["checks"]
    if any(item["status"] == "issue" for item in checks):
        return "已确认存在问题；下列问题项所述条件不满足，分流或配置一致性可能受影响。请按检查项核对来源与运行态，按已授权范围修正或恢复后复验；工具不自动修复或回滚。"
    unknown = [item for item in checks if item["required"] and item["status"] == "unverified"]
    if unknown:
        dns_only = {(item["scope"], item["code"]) for item in unknown} == {("runtime", "dns_unexposed")}
        runtime = [item for item in checks if item["scope"] == "runtime"
                   and item["code"] != "dns_unexposed"]
        if (dns_only and result["active"] is True and result["runtime_observed"]
                and runtime and all(item["status"] == "pass" for item in runtime)):
            return "关键分流运行态检查通过；内核 DNS 加载状态未验证。完整运行态验收未完成；字段未暴露不代表 DNS 已加载或故障。"
        observation = "已观察运行态；" if result["runtime_observed"] else "尚未完成运行态观察；"
        return observation + "仍有必要项目未验证，请逐项处理下列必要未验证项后复验，不能统一归因于 DNS 接口限制。"
    if not result["runtime_requested"] and result["active"] is True:
        return "离线检查通过，尚未观察运行态；不代表应用验收通过。下一步运行 --runtime 并阅读限制项。"
    return "请求范围内必要检查通过；仍须阅读限制项并完成受影响业务抽测。"


def emit(result, json_mode):
    if json_mode:
        print(json.dumps(result, ensure_ascii=False))
        return
    labels = {"pass": "通过", "issue": "问题", "warning": "提醒", "unverified": "未验证"}
    print("sub 只读检查：" + human_summary(result))
    counts = {status: sum(item["status"] == status for item in result["checks"]) for status in labels}
    print("；".join(labels[status] + " " + str(counts[status]) for status in labels))
    for item in result["checks"]:
        if item["status"] != "pass":
            label = "必要未验证" if item["required"] and item["status"] == "unverified" else labels[item["status"]]
            print("[" + label + "] " + item["scope"] + "." + item["code"] + " — " + item["message"])
    print("exit_code=" + str(result["exit_code"]) + " runtime_observed=" + str(result["runtime_observed"]).lower()
          + " runtime_verified=" + str(result["runtime_verified"]).lower())


def main(argv=None):
    argv = sys.argv[1:] if argv is None else argv
    try:
        args = parser().parse_args(argv)
        result = run(args)
    except AuditError as error:
        report = Report("--runtime" in argv)
        report.add("input", error.code, "unverified", error.message)
        result = report.result()
    except Exception:
        # 配置/响应也可能触发未预料的异常；禁止 traceback 泄漏原文。
        report = Report("--runtime" in argv)
        report.add("internal", "internal_error", "unverified", "检查器遇到未预料错误；未回显原始数据或异常栈。")
        result = report.result()
    emit(result, "--json" in argv)
    return result["exit_code"]


if __name__ == "__main__":
    raise SystemExit(main())
