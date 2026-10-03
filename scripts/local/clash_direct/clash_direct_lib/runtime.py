from __future__ import annotations

import json
import os
import shutil
import subprocess
import tempfile
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

from .errors import ToolError


NORMALIZE_URL_JS = r"""
const fs = require('node:fs');
const net = require('node:net');
try {
  const raw = JSON.parse(fs.readFileSync(0, 'utf8')).value;
  let address = raw;
  if (!/^[a-zA-Z][a-zA-Z0-9+.-]*:\/\//.test(raw)) {
    address = net.isIP(raw) === 6 ? `https://[${raw}]` : `https://${raw}`;
  }
  const url = new URL(address);
  if (!['http:', 'https:'].includes(url.protocol)) throw new Error('scheme');
  if (url.username || url.password) throw new Error('credentials');
  process.stdout.write(JSON.stringify({
    host: url.hostname,
    path_ignored: url.pathname !== '/' || !!url.search || !!url.hash,
    port_ignored: !!url.port,
  }));
} catch (error) {
  process.stdout.write(JSON.stringify({error: error.message === 'credentials'
    ? 'credentials' : 'invalid_url'}));
}
"""


def find_executable(explicit: Optional[str], candidates: list[str]) -> Optional[Path]:
    for candidate in [explicit] if explicit else candidates:
        found = shutil.which(candidate) if candidate else None
        path = Path(found or candidate or "").expanduser()
        if candidate and path.is_file() and os.access(path, os.X_OK):
            return path.resolve()
    return None


@dataclass(frozen=True)
class Tools:
    node: Optional[Path]
    core: Optional[Path]

    @classmethod
    def discover(cls, node: Optional[str] = None, core: Optional[str] = None) -> Tools:
        return cls(
            find_executable(node, ["node", "/opt/homebrew/bin/node", "/usr/local/bin/node"]),
            find_executable(core, [
                "/Applications/Clash Verge.app/Contents/MacOS/verge-mihomo",
                "mihomo", "verge-mihomo",
            ]),
        )

    def describe(self) -> dict:
        return {
            "node": str(self.node) if self.node else None,
            "mihomo": str(self.core) if self.core else None,
            "mode": "local_offline", "auth": "not_required",
        }

    def require(self, kind: str) -> Path:
        path = self.node if kind == "node" else self.core
        if not path:
            raise ToolError("dependency_missing", f"未找到 {kind}；不会自动安装依赖。")
        return path

    def run(self, argv: list[str], content: Optional[str], label: str) -> str:
        # 校验进程不加载 NODE_OPTIONS 的额外模块，避免无关副作用。
        env = dict(os.environ)
        env.pop("NODE_OPTIONS", None)
        env.pop("NODE_PATH", None)
        try:
            result = subprocess.run(
                argv, input=content, text=True, encoding="utf-8", capture_output=True,
                timeout=15, env=env, check=False,
            )
        except (OSError, subprocess.TimeoutExpired) as error:
            raise ToolError("check_failed", f"{label} 未能完成，未保存。", exit_code=4) from error
        if result.returncode:
            # Node 的错误输出可能带原脚本源码，不转发其中的凭据或私有内容。
            raise ToolError("check_failed", f"{label} 未通过，未保存。",
                            {"checker_exit": result.returncode}, 4)
        return result.stdout

    def normalize(self, value: str) -> dict:
        output = self.run(
            [str(self.require("node")), "-e", NORMALIZE_URL_JS],
            json.dumps({"value": value}), "网址解析",
        )
        try:
            parsed = json.loads(output)
        except (ValueError, TypeError) as error:
            raise ToolError("invalid_input", "网址解析结果无效。") from error
        if not isinstance(parsed, dict):
            raise ToolError("invalid_input", "网址解析结果结构无效。")
        if parsed.get("error") == "credentials":
            raise ToolError("invalid_input", "不接受带用户名或密码的网址，请仅提供网站地址。")
        if (parsed.get("error") or not isinstance(parsed.get("host"), str)
                or type(parsed.get("path_ignored")) is not bool
                or type(parsed.get("port_ignored")) is not bool):
            raise ToolError("invalid_input", "请输入有效的 HTTP(S) 网址、域名或单个 IP。")
        return parsed

    def validate(self, source: str, rules: list[str]) -> dict:
        node = self.require("node")
        core = self.require("core")
        self.run([str(node), "--check", "--input-type=commonjs"], source, "JavaScript 语法校验")
        # 只校验合成的 DIRECT 规则，不读取节点凭据，不在活动数据目录运行。
        config = {
            "mode": "rule", "log-level": "silent",
            "dns": {"enable": False}, "tun": {"enable": False},
            "proxies": [], "proxy-groups": [], "rules": rules + ["MATCH,DIRECT"],
        }
        with tempfile.TemporaryDirectory(prefix="clash-direct-check-") as directory:
            path = Path(directory) / "rules.json"
            path.write_text(json.dumps(config), encoding="utf-8")
            path.chmod(0o600)
            self.run([str(core), "-t", "-d", directory, "-f", str(path)], None, "Mihomo 规则校验")
        return {"javascript_syntax": True, "mihomo_rule_syntax": True, "runtime_verified": False}
