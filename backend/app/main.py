"""The application factory."""

from __future__ import annotations

import asyncio
import logging
import os
import secrets
from collections.abc import AsyncIterator, Mapping
from contextlib import asynccontextmanager, suppress

import httpx
from fastapi import FastAPI

from app import __version__
from app.api.errors import install_error_handlers
from app.api.guard import SecurityGuard
from app.api.routes import Services, router, service_router
from app.config.settings import Settings, Tuning, load_settings
from app.providers.factory import build_provider
from app.router.registry import ModelRegistry
from app.router.rules import RuleBasedRouter
from app.security.identity import Authenticator
from app.security.paths import prepare_data_dir
from app.services.privacy import PrivacyService
from app.services.retriever import KeywordRetriever
from app.services.slots import InferenceSlots
from app.services.status import StatusMonitor
from app.services.store import DATABASE_FILE, SqliteConversationStore
from app.services.turn import TurnService
from app.services.warmup import WarmupService

logger = logging.getLogger("digital_commons.startup")

TELEMETRY_OFF = {
    "tracing": False,
    "metrics": False,
    "logs": False,
    "operation_spans": False,
    "auto_configure": False,
}


def _build_services(
    settings: Settings,
    env: Mapping[str, str],
    tuning: Tuning,
    transport: httpx.AsyncBaseTransport | None,
) -> Services:
    authenticator = Authenticator(settings, env)
    provider = build_provider(settings, env, transport)
    registry = ModelRegistry(settings)
    retriever = KeywordRetriever(settings.documents_path)
    store = SqliteConversationStore(settings.data_dir / DATABASE_FILE)
    slots = InferenceSlots(
        settings.inference.max_concurrent_requests, settings.inference.max_queued_requests
    )
    monitor = StatusMonitor(
        settings,
        provider,
        registry,
        instance_id=secrets.token_hex(4),
        interval_s=tuning.status_interval_s,
    )
    warmup = WarmupService(
        provider,
        registry,
        slots,
        monitor,
        enabled=settings.inference.warm_on_start,
        poll_s=tuning.warmup_poll_s,
    )
    monitor.warmup = warmup
    privacy = PrivacyService(
        settings, provider, env=env, probe_timeout_s=tuning.port_probe_timeout_s
    )
    turns = TurnService(
        settings, tuning, provider, registry, RuleBasedRouter(), retriever, store, slots, monitor
    )
    return Services(
        settings=settings,
        tuning=tuning,
        authenticator=authenticator,
        provider=provider,
        registry=registry,
        retriever=retriever,
        store=store,
        slots=slots,
        monitor=monitor,
        warmup=warmup,
        privacy=privacy,
        turns=turns,
    )


async def _retention_loop(services: Services) -> None:
    days = services.settings.conversations.retention_days
    while True:
        await asyncio.sleep(services.tuning.retention_interval_s)
        try:
            await services.store.purge_expired(days)
        except Exception as error:
            logger.error("retention failed type=%s", type(error).__name__)


def create_app(
    settings: Settings | None = None,
    *,
    env: Mapping[str, str] | None = None,
    transport: httpx.AsyncBaseTransport | None = None,
    tuning: Tuning | None = None,
) -> FastAPI:
    env = os.environ if env is None else env
    settings = settings or load_settings(env)
    tuning = tuning or Tuning()
    prepare_data_dir(settings.data_dir)
    services = _build_services(settings, env, tuning, transport)

    @asynccontextmanager
    async def lifespan(app: FastAPI) -> AsyncIterator[None]:
        await services.store.open()
        await services.store.purge_expired(settings.conversations.retention_days)
        retention = asyncio.create_task(_retention_loop(services))
        services.monitor.start()
        services.warmup.start()
        try:
            yield
        finally:
            await services.warmup.stop()
            await services.monitor.stop()
            await services.turns.shutdown()
            retention.cancel()
            with suppress(asyncio.CancelledError):
                await retention
            await services.provider.aclose()
            await services.store.close()

    docs = settings.gateway.docs_enabled
    app = FastAPI(
        title="Digital Commons Gateway",
        version=__version__,
        docs_url="/docs" if docs else None,
        redoc_url="/redoc" if docs else None,
        openapi_url="/openapi.json" if docs else None,
        lifespan=lifespan,
        redirect_slashes=False,
        telemetry=TELEMETRY_OFF,
    )
    app.state.services = services
    install_error_handlers(app)
    app.include_router(router)
    if settings.inference_service.enabled:
        app.include_router(service_router)
    app.add_middleware(SecurityGuard, settings=settings, authenticator=services.authenticator)
    return app
