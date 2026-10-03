#!/usr/bin/env python3
"""显式候选复验：只读包和当前来源，在私有临时目录禁网校验；从不应用。"""
import copy
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

sys.dont_write_bytecode = True

from candidate import append_backup, build_script, compose_input, execute_script
from checks import audit_offline
from report import Report
from source import DEFAULT_DIR, load_sources, read_yaml, read_text, select_sources, profile_file
from topology import LA, SG, PRIMARY, FOLLOWERS, replace_fragment


CORE = Path("/Applications/Clash Verge.app/Contents/MacOS/verge-mihomo")


class VerificationError(ValueError):
    """只接受本模块的常量诊断，禁止携带原配置。"""


def require(condition, label):
    if not condition:
        raise VerificationError(label)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def source_guard(package, manifest):
    root = DEFAULT_DIR
    sub, active, links = select_sources(root)
    require(active and sub.get("type") == "local" and sub["option"].get("allow_auto_update") is False,
            "活动订阅身份或刷新状态变化")
    paths = [root / "profiles.yaml", root / "clash-verge.yaml", profile_file(root, sub),
             *links.values(), root / "profiles/Merge.yaml", root / "profiles/Script.js",
             Path(manifest["eix_source"])]
    for path in paths:
        require(digest(path) == manifest["active_before"].get(str(path)), "活动来源指纹变化")
        snapshot = package / "original" / path.relative_to(root)
        if path != Path(manifest["eix_source"]):
            require(digest(snapshot) == digest(path), "原来源快照指纹不符")
    return paths


def private_files(package):
    require(package.is_dir() and not package.is_symlink(), "候选目录无效")
    for path in [package, *package.rglob("*")]:
        require(not path.is_symlink(), "候选包含链接")
        expected = 0o700 if path.is_dir() else 0o600
        require(path.stat().st_mode & 0o777 == expected, "候选文件权限不符")


def freeze_package(package):
    return {p.relative_to(package): digest(p) for p in package.rglob("*") if p.is_file()}


def unchanged_package(package, before):
    private_files(package)
    require(freeze_package(package) == before, "验证期间候选包变化")


def run_check(argv, content=None):
    env = {k: v for k, v in os.environ.items() if k not in {"NODE_OPTIONS", "NODE_PATH"}}
    result = subprocess.run(argv, input=content, capture_output=True, text=True, timeout=30, env=env)
    require(result.returncode == 0, "离线语法/内核检查失败（原输出已隐藏）")
    return result.stdout


def mihomo_check(config, directory):
    config = copy.deepcopy(config)
    require(not config.get("proxy-providers") and not config.get("rule-providers"), "禁止动态 provider")
    config["geo-auto-update"] = False
    path = directory / "check.json"
    path.write_text(json.dumps(config, ensure_ascii=False))
    path.chmod(0o600)
    # deny network 防止缺资源时下载；写权限只给本次隔离目录，不启动内核。
    sandbox = '(version 1)(allow default)(deny network*)(deny file-write*)' \
              '(allow file-write* (subpath ' + json.dumps(str(directory.resolve())) + '))'
    run_check(["/usr/bin/sandbox-exec", "-p", sandbox, str(CORE), "-t", "-d", str(directory), "-f", str(path)])


def business_baseline(old, active):
    require(all(old[k] == active[k] for k in ("rules", "proxies", "proxy-groups")), "离线旧脚本未重现活动业务配置")
    dns = copy.deepcopy(active["dns"])
    # GUI 生成层补充此字段，不能把生成文件当作订阅输入。
    require(dns.pop("enhanced-mode", None) == "fake-ip" and dns == old["dns"], "旧 DNS 重现不一致")
    require(old.get("mode") == "Rule" and active.get("mode") == "rule", "GUI 模式归一化基线变化")


def assert_delta(old, new, dual):
    expected_dns = copy.deepcopy(old["dns"])
    for key in ("geosite:geolocation-!cn", "geosite:gfw"):
        expected_dns["nameserver-policy"][key] = [replace_fragment(v, PRIMARY) for v in expected_dns["nameserver-policy"][key]]
    expected_dns["fallback"] = [replace_fragment(v, PRIMARY) for v in expected_dns["fallback"]]
    require(new["dns"] == expected_dns, "候选 DNS 超出三处引用变更")
    require(new["rules"] == old["rules"], "规则内容或顺序改变")
    untouched = set(old) - {"dns", "proxies", "proxy-groups"}
    require(set(new) == set(old) and all(new[k] == old[k] for k in untouched), "候选改变无关顶层字段")
    require(len(new["proxies"]) == (2 if dual else 1), "候选节点数错误")


def verify_dns_parameters(script, generated):
    inputs = copy.deepcopy(generated)
    policy = inputs["dns"]["nameserver-policy"]
    for key in ("geosite:geolocation-!cn", "geosite:gfw"):
        policy[key] = [replace_fragment(v, LA) + "&h3=true&ecs=1.2.3.4/24" for v in policy[key]]
    inputs["dns"]["fallback"] = [replace_fragment(v, LA) + "&h3=true&ecs=1.2.3.4/24" for v in inputs["dns"]["fallback"]]
    expected = copy.deepcopy(inputs["dns"])
    for key in ("geosite:geolocation-!cn", "geosite:gfw"):
        expected["nameserver-policy"][key] = [replace_fragment(v, PRIMARY) for v in expected["nameserver-policy"][key]]
    expected["fallback"] = [replace_fragment(v, PRIMARY) for v in expected["fallback"]]
    result = execute_script(script, inputs)
    require(result["dns"] == expected and result["rules"] == generated["rules"], "完整脚本丢失 DNS 附加参数或改变规则")


def mock_runtime(data, offline, choice):
    from runtime import audit_runtime
    from unittest.mock import patch
    rows = [{"index": i, "type": row[0], "payload": row[1], "proxy": row[2], "extra": {"disabled": False}}
            for i, row in enumerate(offline[0])]
    proxies = {n["name"]: {"type": n["type"]} for n in data["generated"]["proxies"]}
    for group in data["generated"]["proxy-groups"]:
        proxies[group["name"]] = {"type": "Selector", "all": group["proxies"], "now": group["proxies"][0]}
    proxies[PRIMARY]["now"] = choice
    for name in FOLLOWERS:
        proxies[name]["now"] = PRIMARY
    state = {"/version": {"version": "offline-mock"}, "/configs": {"mode": "rule", "dns": data["generated"]["dns"]},
             "/rules": {"rules": rows}, "/proxies": {"proxies": proxies}}
    report = Report(True)
    report.active = True
    with patch("runtime.read_runtime", return_value=state):
        audit_runtime(report, data, offline)
    require(report.result()["exit_code"] == 0, "合成选择链未通过")


def verify(package):
    private_files(package)
    package_before = freeze_package(package)
    manifest = json.loads(read_text(package / "manifest.json"))
    require(all(digest(Path(p)) == h for p, h in manifest.get("repository_files", {}).items()), "检查器实现指纹变化")
    guarded = source_guard(package, manifest)
    data = load_sources(package / "original")
    script = read_text(package / "s3xXQkAEORtm.js")
    require(script == build_script(data["script"]), "候选脚本与确定性构建不符")
    eix = read_yaml(Path(manifest["eix_source"]))
    extension = read_yaml(package / "poUzKF2xQBER.yaml")
    require(extension == append_backup(data["extensions"]["proxies"], eix["proxies"]), "候选节点扩展与来源不符")
    # 仅调用纯解析器，禁止导入管理器或调用其保存入口。
    sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "clash_direct"))
    from clash_direct_lib.script import ScriptDocument
    document = ScriptDocument.parse(script, data["uid"])
    require(document.installed and document.render(document.rules) == script, "clash-direct 管理契约不符")
    global_merge = read_yaml(package / "original/profiles/Merge.yaml")
    original_input = compose_input(data["source"], data["extensions"], global_merge)
    old = execute_script(data["script"], original_input)
    business_baseline(old, data["generated"])
    run_check(["node", "--check", "--input-type=commonjs"], script)
    summary = []
    with tempfile.TemporaryDirectory(prefix="clash-6d-offline-") as temporary:
        directory = Path(temporary)
        for filename in ("geoip.dat", "geosite.dat", "Country.mmdb", "ASN.mmdb"):
            shutil.copyfile(DEFAULT_DIR / filename, directory / filename)
            (directory / filename).chmod(0o600)
        for state in ("legacy", "transition", "dual"):
            current = copy.deepcopy(data)
            if state == "dual":
                current["extensions"]["proxies"] = extension
                current["backup_source"] = [n for n in eix["proxies"] if n.get("name") == "🇸🇬 Singapore 01"]
            if state != "legacy":
                current["script"] = script
            inputs = compose_input(current["source"], current["extensions"], global_merge)
            generated = execute_script(current["script"], inputs)
            require(generated == execute_script(current["script"], generated), "完整脚本不幂等")
            if state != "legacy":
                assert_delta(old, generated, state == "dual")
                verify_dns_parameters(current["script"], generated)
            # 检查器读取 GUI 的生成形式；原 sub 的 Rule 在生成层归一化为 rule。
            current["generated"] = {**generated, "mode": generated["mode"].lower()}
            report = Report()
            report.active = True
            offline = audit_offline(report, current)
            require(report.result()["exit_code"] == 0, "候选结构验收未通过")
            for choice in ((LA, SG) if state == "dual" else (LA,)):
                mock_runtime(current, offline, choice)
            mihomo_check(generated, directory)
            summary.append({"state": state, "offline_exit": 0, "mihomo_test": True,
                            "idempotent": True, "rules": len(generated["rules"]),
                            "mock_choices": 2 if state == "dual" else 1})
    require(all(digest(p) == manifest["active_before"][str(p)] for p in guarded), "验证期间来源变化")
    unchanged_package(package, package_before)
    return {"status": "passed", "applied": False, "states": summary, "node_syntax": True,
            "clash_direct_compatible": True, "network_denied": True,
            "script_sha256": package_before[Path("s3xXQkAEORtm.js")],
            "proxies_sha256": package_before[Path("poUzKF2xQBER.yaml")]}


def main():
    try:
        require(len(sys.argv) == 2, "仅接受一个私有候选目录参数")
        print(json.dumps(verify(Path(sys.argv[1]).resolve()), ensure_ascii=False))
        return 0
    except Exception as error:
        # 子进程输出、YAML 异常和配置对象均可能含凭据，不输出异常内容。
        message = str(error) if isinstance(error, VerificationError) else "候选复验失败；已隐藏原始异常，不能进入应用阶段。"
        print(json.dumps({"status": "rejected", "applied": False, "message": message}, ensure_ascii=False))
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
