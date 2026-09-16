"""维护入口的固定路径、只读取证及与发布入口共用的停机/锁边界。"""

from __future__ import annotations

import fcntl
import json
import os
import re
import socket
import sqlite3
import stat
import subprocess
import urllib.error
import urllib.request
from contextlib import contextmanager, nullcontext
from pathlib import Path
from types import SimpleNamespace

from observation_contract import digest
from release_common import sha256_file, snapshot_sqlite
from release_runtime import Runtime


def private_file(path: Path, uid: int = 0, secret: bool = True) -> dict:
    if not path.is_absolute() or ".." in path.parts:
        raise ValueError("必须使用受控文件的完整绝对路径")
    info = path.lstat()
    if (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != uid
            or info.st_mode & (0o077 if secret else 0o022)):
        raise ValueError("文件属主、权限或类型不可信: " + path.name)
    parent = path.parent
    while True:
        item = parent.lstat()
        if not stat.S_ISDIR(item.st_mode) or item.st_uid not in {0, uid} or item.st_mode & 0o022:
            raise ValueError("文件目录信任链不可信")
        if parent == parent.parent:
            break
        parent = parent.parent
    return {"device": info.st_dev, "inode": info.st_ino, "uid": info.st_uid,
            "gid": info.st_gid, "mode": stat.S_IMODE(info.st_mode)}


def read_json(path: Path, uid: int = 0) -> dict:
    expected = private_file(path, uid)
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(descriptor, "r", encoding="utf-8") as handle:
        before = os.fstat(handle.fileno())
        actual = {"device": before.st_dev, "inode": before.st_ino, "uid": before.st_uid,
                  "gid": before.st_gid, "mode": stat.S_IMODE(before.st_mode)}
        if actual != expected or not stat.S_ISREG(before.st_mode) or before.st_nlink != 1:
            raise ValueError("受控文件在检查与打开之间被替换")
        if before.st_size > 1024 * 1024:
            raise ValueError("受控 JSON 超过大小限制")
        content = handle.read(1024 * 1024 + 1)
        after = os.fstat(handle.fileno())
        if (len(content) > 1024 * 1024
                or (before.st_size, before.st_mtime_ns, before.st_ctime_ns)
                != (after.st_size, after.st_mtime_ns, after.st_ctime_ns)):
            raise ValueError("受控 JSON 过大或读取期间发生变化")
    value = json.loads(content)
    if not isinstance(value, dict):
        raise ValueError("受控 JSON 必须是对象")
    return value


class MaintenanceRuntime:
    def __init__(self, args=None, *, execute=None, uid=0):
        self.uid = uid
        self.args = args or SimpleNamespace(
            db_path=Path("/var/lib/areasong-ops/ops.db"), config_dir=Path("/etc/areasong-ops"),
            state_dir=Path("/var/lib/areasong-ops/release-orchestrator"),
            runner_root=Path("/usr/local/libexec/areasong-ops"),
            unit_path=Path("/etc/systemd/system/areasong-ops-runner.service"),
            updater_unit_path=Path("/etc/systemd/system/areasong-ops-runner-update@.service"),
            socket_path=Path("/var/lib/areasong-ops/run/runner.sock"),
            runtime_dir=Path("/opt/services/areasong-ops"), container_name="areasong-ops-web",
            cgroup_root=Path("/sys/fs/cgroup"), boot_id_path=Path("/proc/sys/kernel/random/boot_id"),
        )
        self.runtime = Runtime(self.args, execute or self.execute)

    @staticmethod
    def execute(command, **options):
        environment = dict(options.pop("env", os.environ))
        if command[0] == "docker":
            if not (command[1] in {"inspect", "info"} or command[1:3] == ["image", "inspect"]):
                raise ValueError("维护入口禁止 Docker 写动作")
            command = ["/usr/bin/docker", "--host", "unix:///var/run/docker.sock", *command[1:]]
            for key in ("DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_TLS_VERIFY", "DOCKER_CERT_PATH", "DOCKER_API_VERSION"):
                environment.pop(key, None)
        elif command[0] == "systemctl" and command[1] in {"show", "list-units", "list-jobs"}:
            command = ["/usr/bin/systemctl", "--system", *command[1:]]
            environment["DBUS_SYSTEM_BUS_ADDRESS"] = "unix:path=/run/dbus/system_bus_socket"
            environment.pop("SYSTEMD_HOST", None)
            environment.pop("SYSTEMD_MACHINE", None)
        else:
            raise ValueError("维护入口命令不在固定只读集合")
        return subprocess.run(command, capture_output=True, text=True, timeout=20, env=environment, **options)

    def base(self) -> tuple[dict, dict]:
        catalog = read_json(Path(self.args.config_dir) / "services.json", self.uid)
        fleet = catalog.get("fleet") or {}
        if fleet.get("allowRemoteRunners") or (fleet.get("remoteWorker") or {}).get("enabled"):
            raise ValueError("本机维护不能覆盖远程 Runner")
        self.no_release()
        boot = Path(self.args.boot_id_path).read_text().strip()
        if not re.fullmatch(r"[a-f0-9-]{36}", boot):
            raise ValueError("主机启动身份无效")
        files = [Path(self.args.unit_path), Path(self.args.updater_unit_path),
                 Path(self.args.runner_root) / "runner/areasong-ops-runner",
                 Path(self.args.runner_root) / "areasong-ops-runner-updater"]
        fingerprints = {}
        for path in files:
            private_file(path, self.uid, secret=False)
            fingerprints[str(path)] = sha256_file(path)
        evidence = {"host": socket.gethostname(), "bootId": boot, "runtimeFiles": fingerprints,
                    "database": private_file(Path(self.args.db_path), self.uid), "catalogHash": digest(catalog)}
        daemon = json.loads(self.runtime.checked(["docker", "info", "--format", "{{json .ID}}"], "读取本机 Docker 身份"))
        if not isinstance(daemon, str) or not daemon:
            raise ValueError("Docker daemon 身份不可证明")
        evidence["dockerDaemon"] = daemon
        return catalog, evidence

    def no_release(self) -> None:
        if any(os.path.lexists(Path(self.args.state_dir) / name) for name in ("active.json", "maintenance.json")):
            raise ValueError("已有活动发布或维护标记，拒绝并行处置")

    def stopped(self) -> None:
        self.no_release()
        self.runtime.assert_unit_binding()
        self.runtime.assert_stopped()

    def observe(self, service: dict, silence_id: str) -> dict:
        name = (service.get("runtime") or {}).get("applicationContainer", "")
        if not re.fullmatch(r"[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}", name):
            raise ValueError("仅支持目录已固定容器的 Compose 服务")
        data = json.loads(self.runtime.checked(["docker", "inspect", "--type=container", name], "读取服务身份"))[0]
        image_id = data["Image"]
        image = json.loads(self.runtime.checked(["docker", "image", "inspect", image_id], "读取镜像身份"))[0]
        labels = image["Config"].get("Labels") or {}
        version = labels.get("org.opencontainers.image.version", "")
        if not version or data["State"].get("Health", {}).get("Status") != "healthy" or not data["State"].get("Running"):
            raise ValueError("服务当前身份或健康不可证明")
        return {"containerId": data["Id"], "imageId": image_id, "version": version,
                "image": data["Config"]["Image"], "silence": self.silence(silence_id)}

    @staticmethod
    def silence(identifier: str) -> str:
        if not identifier:
            return "none"
        if not re.fullmatch(r"[a-f0-9-]{36}", identifier):
            raise ValueError("静默 ID 无效")
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        try:
            with opener.open("http://127.0.0.1:9093/api/v2/silence/" + identifier, timeout=10) as response:
                data = json.load(response)
        except urllib.error.HTTPError as error:
            if error.code == 404:
                return "absent"
            raise
        if data.get("id") != identifier or data.get("status", {}).get("state") != "expired":
            raise ValueError("关联静默尚未过期或身份不一致")
        return "expired"

    def self_contained(self) -> None:
        path = Path(self.args.db_path)
        if any(os.path.lexists(str(path) + suffix) for suffix in ("-wal", "-shm", "-journal")):
            raise ValueError("零写入预览/状态查询拒绝 WAL/SHM/journal；先在批准窗口停稳控制面")

    @contextmanager
    def connect(self, *, write=False):
        # 预览不创建锁；与 apply 全程互斥，避免 immutable 读取时漏掉并发 WAL。
        with (nullcontext() if write else self.locked(exclusive=False)):
            if not write:
                self.stopped()
                self.self_contained()
            identity = private_file(Path(self.args.db_path), self.uid)
            connection = self.open_database(write=write)
            try:
                if private_file(Path(self.args.db_path), self.uid) != identity:
                    raise ValueError("数据库在打开期间被替换")
                yield connection
                if private_file(Path(self.args.db_path), self.uid) != identity:
                    raise ValueError("数据库在读取/事务期间被替换")
                if not write:
                    self.self_contained()
            finally:
                connection.close()

    def open_database(self, *, write=False):
        path = Path(self.args.db_path)
        private_file(path, self.uid)
        sidecars = [Path(str(path) + suffix) for suffix in ("-wal", "-shm")]
        exists = [os.path.lexists(p) for p in sidecars]
        if not write:
            self.self_contained()
        if os.path.lexists(str(path) + "-journal") or any(exists) and not all(exists):
            raise ValueError("数据库 journal/WAL/SHM 状态不明，拒绝自动恢复")
        for sidecar in sidecars:
            if os.path.lexists(sidecar):
                private_file(sidecar, self.uid)
        immutable = not write  # 此处已证明停机且不存在任何 WAL/journal，不能用于活库。
        uri = path.absolute().as_uri() + ("?mode=rw" if write else "?mode=ro")
        connection = sqlite3.connect(uri + ("&immutable=1" if immutable else ""), uri=True, timeout=5)
        connection.row_factory = sqlite3.Row
        if not write:
            connection.execute("PRAGMA query_only=ON")
        if connection.execute("PRAGMA user_version").fetchone()[0] not in {45, 47}:
            connection.close()
            raise ValueError("维护入口仅支持 schema 45/47，绝不迁移")
        if ([row[0] for row in connection.execute("PRAGMA quick_check")] != ["ok"]
                or connection.execute("PRAGMA foreign_key_check").fetchone() is not None):
            connection.close()
            raise ValueError("数据库完整性或外键校验失败")
        return connection

    @contextmanager
    def locked(self, *, exclusive=True):
        root = Path(self.args.state_dir)
        if not root.is_dir() or root.is_symlink() or root.stat().st_mode & 0o077 or root.stat().st_uid != self.uid:
            raise ValueError("发布锁目录不可信")
        path = root / ".lock"
        if not path.exists():
            raise ValueError("发布锁尚未准备；只读预览不会创建锁文件")
        expected = private_file(path, self.uid)
        descriptor = os.open(path, (os.O_RDWR if exclusive else os.O_RDONLY) | os.O_NOFOLLOW)
        try:
            metadata = os.fstat(descriptor)
            if metadata.st_ino != expected["inode"] or metadata.st_dev != expected["device"]:
                raise ValueError("发布锁身份发生变化")
            fcntl.flock(descriptor, (fcntl.LOCK_EX if exclusive else fcntl.LOCK_SH) | fcntl.LOCK_NB)
            self.no_release()
            yield
        finally:
            os.close(descriptor)

    def backup(self, operation: str) -> dict:
        root = Path(self.args.state_dir) / ("observation-" + operation)
        root.mkdir(mode=0o700)  # 存在即拒绝，失败重跑不得覆盖之前的恢复材料。
        target = root / "ops.db"
        snapshot_sqlite(Path(self.args.db_path), target)
        private_file(target, self.uid)
        return {"path": str(target), "sha256": sha256_file(target)}
