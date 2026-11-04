"""The only entry point. `python -m app` from backend/."""

from __future__ import annotations

import argparse
import os
import sys
from collections.abc import Sequence

import uvicorn

from app.config.settings import ConfigError, load_settings
from app.lifeline import watch_lifeline
from app.main import create_app
from app.security.locality import BindRefused, enforce_loopback_bind
from app.security.log_hygiene import configure_logging


def main(argv: Sequence[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="python -m app", description="Digital Commons gateway")
    parser.add_argument("--host")
    parser.add_argument("--port", type=int)
    args = parser.parse_args(argv)
    watch_lifeline(os.environ)
    try:
        settings = load_settings()
        host = args.host or settings.gateway.host
        enforce_loopback_bind(host)
        settings.gateway.host = host
        if args.port is not None:
            settings.gateway.port = args.port
        configure_logging(settings.logging.level)
        app = create_app(settings)
    except (ConfigError, BindRefused) as error:
        print(f"Digital Commons cannot start: {error}", file=sys.stderr)
        return 2
    uvicorn.run(
        app,
        host=host,
        port=settings.gateway.port,
        proxy_headers=False,
        access_log=False,
        server_header=False,
        log_config=None,
        timeout_graceful_shutdown=5,
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
