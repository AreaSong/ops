import json
import os
import subprocess
import sys
from pathlib import Path

import yaml


TOOL_DIR = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(TOOL_DIR))

from clash_direct_lib.runtime import Tools


BASE_SCRIPT = """function main(config, profileName) {
  const wanted = "DOMAIN-SUFFIX,tools.google.com,🚀 节点选择";
  const first = config.rules.find(rule => rule.includes("tools.google.com"));
  if (first !== wanted) config.rules.unshift(wanted);
  return config;
}

// 用户自己的其他逻辑，管理器不得改动。
function keepUserCode() { return 42; }
"""

BASE_CONFIG = {
    "rules": ["DOMAIN-SUFFIX,guat.edu.cn,DIRECT", "DOMAIN-SUFFIX,tools.google.com,🎯 全球直连", "MATCH,🚀 节点选择"],
    "dns": {"unchanged": True}, "proxies": [{"name": "LA", "password": "fixture-only"}],
    "proxy-groups": [{"name": "🚀 节点选择", "type": "select", "proxies": ["LA"]}],
}


class FakeTools:
    def describe(self):
        return {"mode": "fixture", "auth": "not_required"}

    def validate(self, source, rules):
        return {"javascript_syntax": True, "mihomo_rule_syntax": True, "fixture": True}


def fixture(root: Path) -> Path:
    app = root / "app data"
    (app / "profiles").mkdir(parents=True)
    metadata = {
        "current": "SUB", "items": [
            {"uid": "SUB", "name": "sub", "type": "remote", "option": {"script": "OWN"},
             "url": "https://example.invalid/?token=DO_NOT_PRINT_SECRET"},
            {"uid": "OWN", "type": "script", "file": "owned.js"},
            {"uid": "OTHER", "name": "KeepOther", "type": "remote", "option": {"script": "OTHER_SCRIPT"}},
            {"uid": "OTHER_SCRIPT", "type": "script", "file": "other.js"},
        ],
    }
    (app / "profiles.yaml").write_text(yaml.safe_dump(metadata, allow_unicode=True), encoding="utf-8")
    (app / "profiles/owned.js").write_text(BASE_SCRIPT, encoding="utf-8")
    (app / "profiles/owned.js").chmod(0o640)
    (app / "profiles/other.js").write_text("// 不属于本工具\n", encoding="utf-8")
    return app


def edit_metadata(app: Path, change) -> None:
    path = app / "profiles.yaml"
    data = yaml.safe_load(path.read_text(encoding="utf-8"))
    change(data)
    path.write_text(yaml.safe_dump(data, allow_unicode=True), encoding="utf-8")


def evaluate(source: str, config: dict) -> dict:
    code = """const fs=require('node:fs'), vm=require('node:vm');
const input=JSON.parse(fs.readFileSync(0,'utf8'));
const context=vm.createContext({config:input.config});
const result=vm.runInContext(input.source+'\\nmain(config, "sub");',context,{timeout:1000});
process.stdout.write(JSON.stringify(result));"""
    tools = Tools.discover()
    env = dict(os.environ)
    env.pop("NODE_OPTIONS", None)
    result = subprocess.run([str(tools.node), "-e", code], input=json.dumps({"source": source, "config": config}),
                            text=True, capture_output=True, timeout=10, env=env, check=True)
    return json.loads(result.stdout)
