"""发布期间的受限 systemd/Compose 操作；命令执行器可注入隔离测试。"""

from __future__ import annotations

import json
import os
import re
import shlex
import time
from pathlib import Path

from release_common import IsolationError, copy_atomic, fail, sanitized_container_inspect, sha256_file


RUNNER_UNIT = "areasong-ops-runner.service"
UPDATER_PREFIX = "areasong-ops-runner-update@"
UNIT_NAME = re.compile(r"[A-Za-z0-9:_.@\\-]+\.(?:service|socket|target|device|mount|automount|swap|timer|path|slice|scope)")


def systemd_job_units(output: str) -> list[str]:
    if len(output) > 256 * 1024:
        fail("systemd 作业列表超过读取上限")
    units = []
    for line in output.splitlines():
        fields = line.split()
        if not fields:
            continue
        if len(fields) != 4:
            fail("systemd 作业列表不是完整的四列输出")
        job_id, unit, job_type, state = fields
        if (not re.fullmatch(r"[1-9][0-9]*", job_id) or not UNIT_NAME.fullmatch(unit)
                or not re.fullmatch(r"[a-z]+(?:-[a-z]+)*", job_type) or state not in {"waiting", "running"}):
            fail("systemd 作业列表包含无效或截断字段")
        units.append(unit)
    return units


class Runtime:
    def __init__(self, args, execute):
        self.args = args
        self.execute = execute
        self.stop_attempts = set()

    def checked(self, command: list[str], label: str, **options) -> str:
        result = self.execute(command, **options)
        if result.returncode:
            fail(f"{label}失败 (exit={result.returncode})")
        return result.stdout

    def compose(self, *arguments: str) -> list[str]:
        return [
            "docker", "compose", "--project-directory", str(self.args.runtime_dir),
            "--env-file", str(Path(self.args.runtime_dir) / ".env"),
            "-f", str(Path(self.args.runtime_dir) / "compose.yml"), *arguments,
        ]

    def inspect_web(self) -> dict:
        return sanitized_container_inspect(self.checked(["docker", "inspect", self.args.container_name], "读取 Web 状态"))

    def unit_state(self, name: str) -> dict[str, str]:
        output = self.checked([
            "systemctl", "show", name, "--property=LoadState,ActiveState,SubState,MainPID,ControlGroup,DropInPaths,Environment,ExecStart,FragmentPath",
        ], "读取 Runner unit")
        return dict(line.split("=", 1) for line in output.splitlines() if "=" in line)

    def assert_no_jobs(self) -> None:
        # systemd 255 的 list-jobs 不支持 JSON；固定无表头、无截断的文本合同。
        environment = {**os.environ, "LC_ALL": "C", "SYSTEMD_COLORS": "0", "SYSTEMD_URLIFY": "0"}
        output = self.checked([
            "systemctl", "list-jobs", "--no-legend", "--plain", "--full", "--no-pager",
        ], "读取 systemd 作业", env=environment)
        for unit in systemd_job_units(output):
            if unit == RUNNER_UNIT or unit.startswith(UPDATER_PREFIX):
                fail("Runner/Updater 仍有排队的 systemd 作业")

    def assert_updaters_idle(self) -> None:
        units = json.loads(self.checked([
            "systemctl", "list-units", "--all", "--output=json", "--no-pager", UPDATER_PREFIX + "*.service",
        ], "读取 Updater 实例"))
        if not isinstance(units, list):
            fail("Updater 实例列表无效")
        self.assert_no_jobs()
        for unit in units:
            name = unit.get("unit", "")
            if not name.startswith(UPDATER_PREFIX) or not name.endswith(".service"):
                fail("Updater unit 名称不符合固定范围")
            self.assert_stopped_unit(name)

    def assert_stopped_unit(self, name: str) -> None:
        state = self.unit_state(name)
        if state.get("ActiveState") != "inactive" or state.get("SubState") != "dead" or state.get("MainPID") != "0":
            fail("Runner/Updater 尚未完全停止")
        group = state.get("ControlGroup")
        if group:
            expected = "/system.slice/" + name
            if group != expected:
                fail("Runner cgroup 路径不是固定 unit 的路径")
            root = Path(self.args.cgroup_root) / group.lstrip("/")
            if not root.is_dir() or root.is_symlink() or not (root / "cgroup.procs").is_file():
                fail("Runner cgroup 无法核验，不能视为无进程")
            for path in root.rglob("cgroup.procs"):
                if path.read_text(encoding="utf-8").strip():
                    fail("Runner cgroup 仍有遗留进程")

    def assert_unit_binding(self) -> None:
        state = self.unit_state(RUNNER_UNIT)
        expected_binary = str(Path(self.args.runner_root) / "runner/areasong-ops-runner")
        if state.get("LoadState") != "loaded" or state.get("DropInPaths"):
            fail("Runner unit 未加载或存在未纳入发布的 drop-in")
        if state.get("FragmentPath") != str(self.args.unit_path) or f"path={expected_binary} ;" not in state.get("ExecStart", ""):
            fail("Runner ExecStart/FragmentPath 与固定发布路径不一致")
        environment = dict(item.split("=", 1) for item in shlex.split(state.get("Environment", "")) if "=" in item)
        expected = {
            "OPS_STATE_ROOT": str(Path(self.args.db_path).parent),
            "OPS_SERVICE_CATALOG": str(Path(self.args.config_dir) / "services.json"),
            "OPS_RUNNER_SOCKET": str(self.args.socket_path),
        }
        if any(environment.get(key) != value for key, value in expected.items()):
            fail("Runner 生效环境不指向受控状态/配置/Socket")

    def stop_web(self) -> None:
        if "web" not in self.stop_attempts:
            self.stop_attempts.add("web")
            self.checked(self.compose("stop", "web"), "停止 Web")
        self.assert_web_stopped()

    def assert_web_stopped(self) -> None:
        state = self.inspect_web()["State"]
        if state.get("Running") is not False or state.get("Status") not in {"exited", "created"}:
            fail("Web 尚未完全停止")

    def stop_runner(self) -> None:
        if "runner" not in self.stop_attempts:
            self.stop_attempts.add("runner")
            self.checked(["systemctl", "stop", RUNNER_UNIT], "停止 Runner")
        self.assert_stopped_unit(RUNNER_UNIT)
        self.assert_updaters_idle()

    def assert_stopped(self) -> None:
        self.assert_web_stopped()
        self.assert_stopped_unit(RUNNER_UNIT)
        self.assert_updaters_idle()

    def install_runner(self, staging: Path) -> None:
        for source, target, mode in (
            (staging / "areasong-ops-runner", Path(self.args.runner_root) / "runner/areasong-ops-runner", 0o755),
            (staging / "areasong-ops-runner-updater", Path(self.args.runner_root) / "areasong-ops-runner-updater", 0o755),
            (Path(self.args.candidate_unit), Path(self.args.unit_path), 0o644),
            (Path(self.args.candidate_updater_unit), Path(self.args.updater_unit_path), 0o644),
        ):
            copy_atomic(source, target, mode)
        self.checked(["systemctl", "daemon-reload"], "加载 Runner unit")
        self.assert_unit_binding()

    def prepare_image(self, metadata: dict) -> None:
        image = metadata["web_image"]
        self.checked(["docker", "pull", image], "拉取固定 Web 镜像")
        images = json.loads(self.checked(["docker", "image", "inspect", image], "读取镜像身份"))
        repo, digest = image.rsplit("@", 1)
        expected = repo.rsplit(":", 1)[0] + "@" + digest
        if len(images) != 1 or expected not in images[0].get("RepoDigests", []):
            fail("Web 镜像摘要未被运行环境证明")
        labels = images[0].get("Config", {}).get("Labels", {})
        if images[0].get("Os") != "linux" or images[0].get("Architecture") != "amd64" or labels.get("org.opencontainers.image.revision") != metadata["revision"]:
            fail("Web 镜像平台或构建 revision 不匹配")
        self.checked(["docker", "tag", image, "areasong-ops-web:" + metadata["revision"]], "绑定 Web 镜像")

    def start_runner(self, revision: str, *, maintenance: bool, deployment_id: str = "") -> dict:
        self.stop_attempts.discard("runner")
        self.checked(["systemctl", "start", RUNNER_UNIT], "启动 Runner")
        return self.wait_runner(revision, maintenance=maintenance, deployment_id=deployment_id)

    def wait_runner(self, revision: str, *, maintenance: bool, deployment_id: str = "") -> dict:
        for _ in range(30):
            result = self.execute(["curl", "--max-time", "2", "-fsS", "--unix-socket", str(self.args.socket_path), "http://runner/healthz"])
            try:
                body = json.loads(result.stdout) if result.returncode == 0 else {}
            except json.JSONDecodeError:
                body = {}
            if body.get("ok") is True and body.get("revision") == revision:
                if bool(body.get("releaseMaintenance")) != maintenance:
                    raise IsolationError("Runner 发布验收模式不符合预期")
                if maintenance and (body.get("releaseProtocol") != 1 or body.get("deploymentId") != deployment_id):
                    raise IsolationError("Runner 发布隔离身份不匹配")
                return body
            time.sleep(0.2)
        fail("Runner 健康/构建身份未在期限内就绪")

    def start_web(self) -> None:
        self.stop_attempts.discard("web")
        self.checked(self.compose("up", "-d", "--force-recreate", "--no-deps", "web"), "重建 Web")

    def restart_runner(self) -> None:
        self.stop_attempts.discard("runner")
        self.checked(["systemctl", "restart", RUNNER_UNIT], "正式激活 Runner")

    def restore_image(self, manifest: dict) -> None:
        image_id = manifest["image"].get("Image", "")
        if not image_id.startswith("sha256:"):
            fail("旧 Web 镜像身份缺失")
        self.checked(["docker", "tag", image_id, "areasong-ops-web:" + manifest["revision"]], "恢复旧 Web 镜像绑定")

    def candidate_info(self, staging: Path, metadata: dict) -> dict:
        environment = os.environ.copy()
        # 旧 Runner 忽略参数；使用不存在的 catalog，让它在打开数据库前失败。
        missing = staging / "release-probe-no-catalog.json"
        if os.path.lexists(missing):
            fail("发布协议探针的隔离路径被占用")
        environment.update({"OPS_SERVICE_CATALOG": str(missing), "OPS_STATE_ROOT": str(staging / "probe-state")})
        raw = self.checked([str(staging / "areasong-ops-runner"), "--release-info"], "检测候选 Runner 发布协议", env=environment)
        info = json.loads(raw)
        if info.get("releaseProtocol") != 1 or info.get("revision") != metadata["revision"] or info.get("version") != metadata["version"]:
            fail("候选 Runner 不支持绑定本次版本的发布隔离协议")
        if type(info.get("schemaVersion")) is not int or info["schemaVersion"] < 45:
            fail("候选 Runner 数据库版本无效")
        info["runnerSha256"] = sha256_file(staging / "areasong-ops-runner")
        return info
