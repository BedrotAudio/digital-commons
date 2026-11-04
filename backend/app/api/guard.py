"""The outer ASGI layer: request id, request log, and every check before a route runs.

Pure ASGI on purpose. BaseHTTPMiddleware would break client disconnect detection.
"""

from __future__ import annotations

import logging
import secrets
import time

from starlette.types import ASGIApp, Message, Receive, Scope, Send

from app.api.errors import envelope
from app.config.settings import LimitsConfig, Settings
from app.models.errors import ApiError
from app.security.identity import Authenticator
from app.security.locality import is_loopback_host
from app.security.log_hygiene import safe_method

logger = logging.getLogger("digital_commons.request")

SERVICE_PATH = "/v1/inference/stream"
_REQUEST_OVERHEAD = 65536


def body_limit(limits: LimitsConfig) -> int:
    """JSON escaping can grow text sixfold, so the cap sits above the parsed limits."""
    return (
        _REQUEST_OVERHEAD
        + 12 * limits.max_message_chars
        + 6 * limits.max_attachments * limits.max_attachment_bytes
    )


def host_without_port(value: str) -> str:
    value = value.strip().lower()
    if value.startswith("["):
        return value.partition("]")[0] + "]"
    return value.partition(":")[0]


def _is_json(content_type: str | None) -> bool:
    if content_type is None:
        return False
    return content_type.partition(";")[0].strip().lower() == "application/json"


class SecurityGuard:
    def __init__(self, app: ASGIApp, *, settings: Settings, authenticator: Authenticator) -> None:
        self.app = app
        self._allowed_hosts = {item.lower() for item in settings.security.allowed_client_hosts}
        self._auth = authenticator
        self._body_limit = body_limit(settings.limits)
        self._service_enabled = settings.inference_service.enabled

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if scope["type"] == "lifespan":
            await self.app(scope, receive, send)
            return
        if scope["type"] != "http":
            await send({"type": "websocket.close", "code": 1008})
            return

        request_id = secrets.token_hex(8)
        scope.setdefault("state", {})["request_id"] = request_id
        began = time.monotonic()
        status = 0
        started = False

        async def send_with_id(message: Message) -> None:
            nonlocal status, started
            if message["type"] == "http.response.start":
                started = True
                status = message["status"]
                headers = [
                    (name, value)
                    for name, value in message.get("headers", [])
                    if name.lower() != b"x-request-id"
                ]
                headers.append((b"x-request-id", request_id.encode("ascii")))
                headers.append((b"x-content-type-options", b"nosniff"))
                if scope["path"].startswith("/v1/"):
                    headers.append((b"cache-control", b"no-store"))
                message = {**message, "headers": headers}
            await send(message)

        try:
            refusal, receive = await self._screen(scope, receive)
            if refusal is not None:
                response = envelope(refusal, include_detail=False)
                await response(scope, receive, send_with_id)
            else:
                await self.app(scope, receive, send_with_id)
        except Exception as error:
            logger.error("unhandled type=%s request_id=%s", type(error).__name__, request_id)
            if not started:
                response = envelope(ApiError("internal"), include_detail=False)
                await response(scope, receive, send_with_id)
        finally:
            route = scope.get("route")
            logger.info(
                "request method=%s route=%s status=%d duration_ms=%d request_id=%s",
                safe_method(scope["method"]),
                getattr(route, "path", "(none)"),
                status,
                int((time.monotonic() - began) * 1000),
                request_id,
            )

    async def _screen(self, scope: Scope, receive: Receive) -> tuple[ApiError | None, Receive]:
        headers = {
            name.decode("latin-1").lower(): value.decode("latin-1")
            for name, value in scope["headers"]
        }

        # Refuses a direct `uvicorn --host 0.0.0.0`, which skips the entry point's bind rule.
        server = scope.get("server")
        if server is not None and not is_loopback_host(server[0]):
            return ApiError("permission_denied"), receive
        if host_without_port(headers.get("host", "")) not in self._allowed_hosts:
            return ApiError("permission_denied"), receive
        if "origin" in headers or "sec-fetch-site" in headers:
            return ApiError("permission_denied"), receive

        if not scope["path"].startswith("/v1/"):
            return None, receive

        header = headers.get("authorization")
        if self._service_enabled and scope["path"] == SERVICE_PATH:
            if not self._auth.authenticate_service(header):
                return ApiError("auth_required"), receive
        else:
            identity = self._auth.authenticate(header)
            if identity is None:
                return ApiError("auth_required"), receive
            scope["state"]["identity"] = identity

        if scope["method"] != "POST":
            return None, receive
        if not _is_json(headers.get("content-type")):
            return ApiError("validation_error"), receive
        return await self._read_body(headers, receive)

    async def _read_body(
        self, headers: dict[str, str], receive: Receive
    ) -> tuple[ApiError | None, Receive]:
        """Read the capped body once, then replay it. Disconnects still pass through."""
        declared = headers.get("content-length", "")
        if declared.isdigit() and int(declared) > self._body_limit:
            return ApiError("request_too_large"), receive
        chunks: list[bytes] = []
        size = 0
        while True:
            message = await receive()
            if message["type"] != "http.request":
                break
            chunk = message.get("body", b"")
            size += len(chunk)
            if size > self._body_limit:
                return ApiError("request_too_large"), receive
            chunks.append(chunk)
            if not message.get("more_body", False):
                break
        body = b"".join(chunks)
        replayed = False

        async def replay() -> Message:
            nonlocal replayed
            if replayed:
                return await receive()
            replayed = True
            return {"type": "http.request", "body": body, "more_body": False}

        return None, replay
