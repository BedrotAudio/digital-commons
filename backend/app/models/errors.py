"""The error catalog from docs/api.md, shared by every layer."""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class ErrorKind:
    status: int
    retryable: bool
    message: str


CATALOG: dict[str, ErrorKind] = {
    "validation_error": ErrorKind(
        422, False, "That request could not be processed. Please check it and try again."
    ),
    "auth_required": ErrorKind(401, False, "Sign-in is required to use this service."),
    "permission_denied": ErrorKind(403, False, "Your account doesn't have access to that."),
    "not_found": ErrorKind(404, False, "That item could not be found."),
    "conversation_locality_changed": ErrorKind(
        409, False, "This conversation began in a different place. Please start a new chat."
    ),
    "request_too_large": ErrorKind(
        413, False, "That is too large to send. Try a shorter message or a smaller file."
    ),
    "unsupported_attachment": ErrorKind(415, False, "That type of file can't be attached."),
    "busy": ErrorKind(429, True, "The assistant is busy right now. Please try again in a moment."),
    "provider_unavailable": ErrorKind(
        503, True, "The AI service isn't responding. Please try again in a moment."
    ),
    "model_not_installed": ErrorKind(
        503, False, "No AI model is available for this request yet."
    ),
    "model_load_failed": ErrorKind(503, True, "The AI model could not be loaded. Please try again."),
    "model_not_permitted": ErrorKind(
        503,
        False,
        "This AI service was blocked to protect your data, because outside AI access may be on.",
    ),
    "context_too_large": ErrorKind(
        422, False, "That message is too long for the assistant. Please shorten it and try again."
    ),
    "timeout": ErrorKind(504, True, "The AI service stopped responding. Please try again."),
    "provider_error": ErrorKind(502, True, "The AI service ran into a problem. Please try again."),
    "internal": ErrorKind(500, True, "Something went wrong. Please try again."),
}


class ApiError(Exception):
    """A failure with a contract code. `detail` is technical and diagnostics-only."""

    def __init__(self, code: str, detail: str | None = None) -> None:
        super().__init__(code)
        self.code = code
        self.detail = detail
        kind = CATALOG[code]
        self.status = kind.status
        self.retryable = kind.retryable
        self.message = kind.message

    def body(self, *, include_detail: bool) -> dict:
        return {
            "code": self.code,
            "message": self.message,
            "detail": self.detail if include_detail else None,
            "retryable": self.retryable,
        }


class ContextOverflow(ApiError):
    """The provider refused the prompt as too long. Sizes are absent when not reported."""

    def __init__(
        self, reported_tokens: int | None = None, context_tokens: int | None = None
    ) -> None:
        super().__init__("context_too_large", f"reported {reported_tokens} of {context_tokens}")
        self.reported_tokens = reported_tokens
        self.context_tokens = context_tokens
