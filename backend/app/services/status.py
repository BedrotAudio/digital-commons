"""The status monitor: a cached provider view refreshed by one background task."""

from __future__ import annotations

import asyncio
import logging
import time
from typing import TYPE_CHECKING

from app import __version__
from app.config.settings import Settings
from app.models.domain import Identity, Locality
from app.providers.base import LOADING_MESSAGE, InferenceProvider, ProviderHealth, model_key
from app.router.registry import ModelRegistry
from app.security.locality import compute_locality

if TYPE_CHECKING:
    from app.services.warmup import WarmupService

logger = logging.getLogger("digital_commons.status")

_SUMMARIES = {
    "provider_unreachable": "The AI service isn't responding.",
    "cloud_access_enabled": "Turned off to protect your data, because outside AI access may be on.",
    "model_missing": "No AI model is installed yet.",
    "model_load_failed": "The AI model could not be loaded.",
}


class StatusMonitor:
    def __init__(
        self,
        settings: Settings,
        provider: InferenceProvider,
        registry: ModelRegistry,
        *,
        instance_id: str,
        interval_s: float,
    ) -> None:
        self._settings = settings
        self._provider = provider
        self._registry = registry
        self._interval = interval_s
        self.instance_id = instance_id
        self.started_at = time.monotonic()
        self.health: ProviderHealth | None = None
        self.warmup: WarmupService | None = None
        self._first = asyncio.Event()
        self._task: asyncio.Task[None] | None = None

    def start(self) -> None:
        self._task = asyncio.create_task(self._loop())

    async def stop(self) -> None:
        if self._task is not None:
            self._task.cancel()
            await asyncio.gather(self._task, return_exceptions=True)

    async def wait_first(self, timeout: float) -> None:
        try:
            await asyncio.wait_for(self._first.wait(), timeout)
        except TimeoutError:
            pass

    async def _loop(self) -> None:
        while True:
            await self.refresh()
            await asyncio.sleep(self._interval)

    async def refresh(self) -> None:
        try:
            self.health = await self._provider.health()
        except Exception as error:
            logger.warning("provider health failed type=%s", type(error).__name__)
            self.health = ProviderHealth(reachable=False, error=type(error).__name__)
        self._first.set()

    def locality(self) -> Locality:
        """Recomputed on every read, so a changed cloud setting shows on the next poll."""
        health = self.health
        passed = health is not None and health.cloud_gate is True
        return compute_locality(self._provider.kind, self._provider.base_url, passed)

    def provider_locality(self) -> Locality:
        """Where a conversation belongs if this provider serves it with its gate passing.

        Conversations compare against this, so an outage or a closed gate reports its
        own error instead of telling the user their conversation has moved.
        """
        return compute_locality(self._provider.kind, self._provider.base_url, True)

    def usable_tiers(self) -> list[str]:
        return [
            spec.tier
            for spec in self._registry.enabled()
            if self._registry.is_usable(spec, self.health)
        ]

    def state(self) -> tuple[str, str | None]:
        if self._provider.kind == "mock":
            return "demo", None
        health = self.health
        if health is None:
            return "starting", None
        if not health.reachable:
            return "unavailable", "provider_unreachable"
        if health.cloud_gate is False:
            return "unavailable", "cloud_access_enabled"
        enabled = self._registry.enabled()
        usable = self.usable_tiers()
        if not usable:
            if health.rejected:
                return "unavailable", "cloud_access_enabled"
            return "unavailable", "model_missing"
        warmup_state = self.warmup.state if self.warmup else "idle"
        if warmup_state == "loading":
            return "starting", None
        if len(usable) < len(enabled):
            return "degraded", "model_missing"
        if warmup_state == "failed":
            return "degraded", "model_load_failed"
        return "ready", None

    def _summary(self, state: str, reason: str | None) -> str:
        if state == "demo":
            return "Demonstration mode. Replies are simulated."
        if state == "starting":
            return LOADING_MESSAGE
        if state == "ready":
            return "Ready"
        message = _SUMMARIES.get(reason or "", "")
        return f"Working, with limits. {message}".strip() if state == "degraded" else message

    def _missing_models(self) -> list[dict]:
        health = self.health
        if health is None or health.installed is None or not health.reachable:
            return []
        return [
            {
                "tier": spec.tier,
                "model": spec.model,
                "install_hint": self._provider.install_hint(spec.model or ""),
            }
            for spec in self._registry.enabled()
            if model_key(spec.model or "") not in health.installed
        ]

    def _inference(self) -> dict:
        health = self.health or ProviderHealth(reachable=False)
        warm = self._registry.warmup_spec() if self._provider.supports_warmup else None
        return {
            "provider": self._provider.name,
            "kind": self._provider.kind,
            "reachable": health.reachable,
            "version": health.version,
            "error": health.error,
            "cloud_access_disabled": health.cloud_gate,
            "missing_models": self._missing_models(),
            "warmup_model": warm.model if warm else None,
            "loaded_models": health.loaded_models,
        }

    def view(self, identity: Identity) -> dict:
        state, reason = self.state()
        warmup = self.warmup
        return {
            "state": state,
            "reason": reason,
            "summary": self._summary(state, reason),
            "locality": self.locality().wire(),
            "warmup": {
                "state": warmup.state if warmup else "idle",
                "message": warmup.message if warmup else None,
            },
            "gateway": {
                "version": __version__,
                "instance_id": self.instance_id,
                "uptime_s": int(time.monotonic() - self.started_at),
                "address": f"{self._settings.gateway.host}:{self._settings.gateway.port}",
            },
            "inference": self._inference() if identity.permissions.view_diagnostics else None,
        }
