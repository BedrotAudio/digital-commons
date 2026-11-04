"""The InferenceProvider protocol and the chunks every adapter yields."""

from __future__ import annotations

from collections.abc import AsyncIterator
from dataclasses import dataclass, field
from typing import Protocol, runtime_checkable

from app.models.domain import TierSpec

LOADING_MESSAGE = "Getting ready. The first response can take a few minutes."


@dataclass(frozen=True)
class StatusChunk:
    phase: str
    message: str


@dataclass(frozen=True)
class ThinkingChunk:
    text: str


@dataclass(frozen=True)
class DeltaChunk:
    text: str


@dataclass(frozen=True)
class FinalChunk:
    finish_reason: str
    prompt_tokens: int | None = None
    completion_tokens: int | None = None
    load_ms: int | None = None
    tokens_per_second: float | None = None
    model: str | None = None


Chunk = StatusChunk | ThinkingChunk | DeltaChunk | FinalChunk


@dataclass(frozen=True)
class InferenceRequest:
    spec: TierSpec
    messages: list[dict[str, str]]


@dataclass
class ProviderHealth:
    """`installed` None means the provider maps tiers itself, so all are available.

    `cloud_gate` is True or False for a provider with a gate and None otherwise,
    including when the provider could not be asked.
    """

    reachable: bool
    version: str | None = None
    error: str | None = None
    cloud_gate: bool | None = None
    installed: set[str] | None = None
    rejected: set[str] = field(default_factory=set)
    loaded_models: list[dict] = field(default_factory=list)


@runtime_checkable
class InferenceProvider(Protocol):
    name: str
    kind: str
    display_name: str
    base_url: str | None
    supports_warmup: bool

    async def health(self) -> ProviderHealth: ...

    async def preflight(self) -> ProviderHealth:
        """Raise ApiError when no turn can start. Runs before the response begins."""
        ...

    async def cloud_gate(self) -> bool | None: ...

    def stream(self, request: InferenceRequest) -> AsyncIterator[Chunk]: ...

    async def warm(self, spec: TierSpec) -> None: ...

    def install_hint(self, model: str) -> str | None: ...

    async def aclose(self) -> None: ...


def model_key(name: str) -> str:
    """Ollama resolves names case-insensitively, and an untagged name means latest."""
    key = name.casefold()
    return key if ":" in key else f"{key}:latest"
