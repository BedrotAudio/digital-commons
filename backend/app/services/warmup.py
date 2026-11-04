"""Background warm-up. It never blocks startup and holds the inference slot while loading."""

from __future__ import annotations

import asyncio
import logging

from app.models.domain import TierSpec
from app.models.errors import ApiError
from app.providers.base import LOADING_MESSAGE, InferenceProvider
from app.router.registry import ModelRegistry
from app.services.slots import InferenceSlots
from app.services.status import StatusMonitor

logger = logging.getLogger("digital_commons.warmup")

_RETRY_CODES = ("provider_unavailable", "timeout")
_FAILED_MESSAGE = "The AI model could not be loaded ahead of time. It will load with your first message."


class WarmupService:
    def __init__(
        self,
        provider: InferenceProvider,
        registry: ModelRegistry,
        slots: InferenceSlots,
        monitor: StatusMonitor,
        *,
        enabled: bool,
        poll_s: float,
    ) -> None:
        self._provider = provider
        self._registry = registry
        self._slots = slots
        self._monitor = monitor
        self._enabled = enabled and provider.supports_warmup
        self._poll = poll_s
        self._task: asyncio.Task[None] | None = None
        self._served = False  # a reply has already loaded the warm-up model
        self.state = "idle"
        self.message: str | None = None

    def start(self) -> None:
        spec = self._registry.warmup_spec()
        if self._enabled and spec is not None:
            self._task = asyncio.create_task(self._run(spec))

    async def stop(self) -> None:
        if self._task is not None:
            self._task.cancel()
            await asyncio.gather(self._task, return_exceptions=True)

    def _can_warm(self, spec: TierSpec) -> bool:
        health = self._monitor.health
        return (
            health is not None
            and health.reachable
            and health.cloud_gate is True
            and self._registry.is_usable(spec, health)
        )

    def _provider_gone(self) -> bool:
        health = self._monitor.health
        return health is not None and not health.reachable

    def _set(self, state: str, message: str | None) -> None:
        self.state = state
        self.message = message

    def note_model_loaded(self, model: str | None) -> None:
        """A reply produced text from the warm-up model, so it is loaded.

        Without this, a first message that wins the slot leaves warm-up waiting behind
        it, and status says the model is still loading after the reply has finished.
        """
        spec = self._registry.warmup_spec()
        if self._enabled and spec is not None and model == spec.model:
            self._served = True
            self._set("ready", None)

    async def _run(self, spec: TierSpec) -> None:
        while True:
            while not self._can_warm(spec):
                await asyncio.sleep(self._poll)
            retry = await self._attempt(spec)
            if retry:
                await asyncio.sleep(self._poll)
                continue
            # A provider restart loses the model, so warm again when it comes back.
            while not self._provider_gone():
                await asyncio.sleep(self._poll)
            self._served = False

    async def _attempt(self, spec: TierSpec) -> bool:
        if self._served:
            self._set("ready", None)
            return False
        self._set("loading", LOADING_MESSAGE)
        ticket = self._slots.reserve(internal=True)
        try:
            await self._slots.acquire(ticket)
            if self._served:  # A reply loaded the model while this waited.
                self._set("ready", None)
                return False
            await self._provider.warm(spec)
        except ApiError as error:
            logger.warning("warm-up failed code=%s", error.code)
            if error.code in _RETRY_CODES:
                self._set("idle", None)
                return True
            self._set("failed", _FAILED_MESSAGE)
            return False
        except asyncio.CancelledError:
            self._set("idle", None)
            raise
        except Exception as error:
            logger.warning("warm-up failed type=%s", type(error).__name__)
            self._set("failed", _FAILED_MESSAGE)
            return False
        finally:
            self._slots.release(ticket)
        self._set("ready", None)
        return False
