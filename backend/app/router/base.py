"""The Router protocol. A router picks among tiers it was given and nothing else."""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass
from typing import Protocol

from app.models.domain import TierSpec


@dataclass(frozen=True)
class RouteSignals:
    message: str
    attachment_count: int
    use_documents: bool
    assistant: str


@dataclass(frozen=True)
class RouteDecision:
    tier: str
    reason: str


class Router(Protocol):
    def choose(self, signals: RouteSignals, tiers: Sequence[TierSpec]) -> RouteDecision | None: ...
