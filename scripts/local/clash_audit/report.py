"""稳定输出；调用者只能提交脱敏后的常量消息及计数。"""


class Report:
    def __init__(self, runtime=False):
        self.checks = []
        self.runtime_requested = runtime
        self.runtime_observed = False
        self.active = None

    def add(self, scope, code, status, message, required=True):
        self.checks.append(dict(scope=scope, code=code, status=status,
                                message=message, required=required))

    def test(self, scope, code, condition, message):
        self.add(scope, code, "pass" if condition else "issue", message)

    def result(self):
        issues = any(x["status"] == "issue" for x in self.checks)
        unknown = any(x["status"] == "unverified" and x["required"] for x in self.checks)
        code = 1 if issues else 2 if unknown else 0
        runtime = [x for x in self.checks if x["scope"] == "runtime"]
        verified = (not issues and not unknown and self.runtime_requested and self.runtime_observed and self.active is True
                    and bool(runtime) and all(x["status"] == "pass" for x in runtime))
        return {"schema_version": 1, "status": {0: "passed", 1: "issues", 2: "incomplete"}[code],
                "ok": code == 0, "exit_code": code, "active": self.active,
                "runtime_requested": self.runtime_requested,
                "runtime_observed": self.runtime_observed, "runtime_verified": verified,
                "all_checks_passed": all(x["status"] == "pass" for x in self.checks),
                "checks": self.checks}
