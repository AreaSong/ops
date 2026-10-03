from typing import Any, Optional


class ToolError(Exception):
    def __init__(
        self, code: str, message: str, details: Optional[dict[str, Any]] = None,
        exit_code: int = 2,
    ) -> None:
        super().__init__(message)
        self.code = code
        self.details = details or {}
        self.exit_code = exit_code

    def payload(self) -> dict[str, Any]:
        return {"code": self.code, "message": str(self), "details": self.details}
