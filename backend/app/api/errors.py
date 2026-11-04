"""Every failure leaves the gateway in the same envelope."""

from __future__ import annotations

import logging

from fastapi import FastAPI, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from starlette.exceptions import HTTPException as StarletteHTTPException

from app.models.domain import Identity
from app.models.errors import ApiError
from app.security.log_hygiene import validation_summary

logger = logging.getLogger("digital_commons.request")

_STATUS_CODES = {
    401: "auth_required",
    403: "permission_denied",
    404: "not_found",
    413: "request_too_large",
    429: "busy",
}


def _can_see_detail(request: Request) -> bool:
    identity: Identity | None = getattr(request.state, "identity", None)
    return identity is not None and identity.permissions.view_diagnostics


def envelope(error: ApiError, *, include_detail: bool, status: int | None = None) -> JSONResponse:
    return JSONResponse(
        status_code=status or error.status,
        content={"error": error.body(include_detail=include_detail)},
    )


async def _on_api_error(request: Request, exc: Exception) -> JSONResponse:
    assert isinstance(exc, ApiError)
    return envelope(exc, include_detail=_can_see_detail(request))


async def _on_validation(request: Request, exc: Exception) -> JSONResponse:
    assert isinstance(exc, RequestValidationError)
    summary = validation_summary(exc.errors())
    logger.info("validation failed errors=%s", summary)
    error = ApiError("validation_error", "; ".join(summary))
    return envelope(error, include_detail=_can_see_detail(request))


async def _on_http(request: Request, exc: Exception) -> JSONResponse:
    assert isinstance(exc, StarletteHTTPException)
    code = _STATUS_CODES.get(exc.status_code)
    error = ApiError(code or "validation_error", f"HTTP {exc.status_code}")
    response = envelope(
        error, include_detail=_can_see_detail(request), status=exc.status_code
    )
    response.headers.update(exc.headers or {})
    return response


def install_error_handlers(app: FastAPI) -> None:
    app.add_exception_handler(ApiError, _on_api_error)
    app.add_exception_handler(RequestValidationError, _on_validation)
    app.add_exception_handler(StarletteHTTPException, _on_http)
