"""Logging setup. Gateway logs carry metadata only, never content."""

from __future__ import annotations

import logging
import sys
from collections.abc import Iterable
from typing import Any

FORMAT = "%(asctime)s %(levelname)s %(name)s %(message)s"
METHODS = {"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}


def configure_logging(level: str) -> None:
    root = logging.getLogger()
    root.setLevel(level)
    if not root.handlers:
        handler = logging.StreamHandler(sys.stderr)
        handler.setFormatter(logging.Formatter(FORMAT))
        root.addHandler(handler)
    # httpx logs upstream URLs at INFO, and httpcore logs at DEBUG.
    for name in ("httpx", "httpcore", "hpack"):
        logging.getLogger(name).setLevel(logging.WARNING)
    logging.getLogger("uvicorn.access").disabled = True


def safe_method(method: str) -> str:
    return method if method in METHODS else "OTHER"


def validation_summary(errors: Iterable[dict[str, Any]]) -> list[str]:
    """Only loc and type. `input` and `msg` can both carry what the user typed."""
    summary = []
    for error in errors:
        location = ".".join(str(part) for part in error.get("loc", ()))
        summary.append(f"{location}:{error.get('type', 'unknown')}")
    return summary
