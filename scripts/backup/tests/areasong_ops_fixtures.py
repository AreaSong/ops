"""从仓库实际迁移 SQL 创建测试库，不用空表冒充生产 schema。"""

import re
import sqlite3
import tempfile
from pathlib import Path


SCHEMA_SOURCE = Path(__file__).resolve().parents[3] / "services/areasong-ops/internal/store/schema.go"


def create_database(path: Path, version: int) -> None:
    source = SCHEMA_SOURCE.read_text(encoding="utf-8")
    base = re.search(r"const schema = `([^`]*)`", source)
    migrations = re.findall(r"`([^`]*)`", source.split("var migrations = []string{", 1)[1])
    if base is None or not 1 <= version <= len(migrations):
        raise AssertionError("真实迁移夹具不完整")
    path.unlink(missing_ok=True)
    with tempfile.TemporaryDirectory(dir=path.parent) as directory:
        connection = sqlite3.connect(Path(directory) / "source.db")
        try:
            connection.executescript(base[1])
            for index, statement in enumerate(migrations[:version], 1):
                connection.executescript(statement)
                connection.execute(f"PRAGMA user_version={index}")
            connection.commit()
            # 与 Runner 一致，用 VACUUM INTO 交付真正自包含的快照。
            connection.execute("VACUUM INTO ?", (str(path),))
        finally:
            connection.close()
