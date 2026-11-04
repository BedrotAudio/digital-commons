"""Maps tiers to models on the active provider. It never crosses providers."""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass

from app.config.settings import TIER_ORDER, Settings
from app.models.domain import TierSpec
from app.models.errors import ApiError
from app.providers.base import ProviderHealth, model_key


@dataclass(frozen=True)
class Resolution:
    spec: TierSpec
    fallback_from: str | None


class ModelRegistry:
    def __init__(self, settings: Settings) -> None:
        catalog = settings.models.catalog.get(settings.provider_name, {})
        self._tiers: dict[str, TierSpec] = {}
        for tier in TIER_ORDER:
            meta = settings.models.tiers.get(tier)
            entry = catalog.get(tier)
            self._tiers[tier] = TierSpec(
                tier=tier,
                label=meta.label if meta else tier.title(),
                description=meta.description if meta else "",
                enabled=bool(meta and meta.enabled and entry),
                model=entry.name if entry else None,
                context=entry.context if entry else None,
                max_output=entry.max_output if entry else None,
                temperature=entry.temperature if entry else None,
                think=entry.think if entry else False,
            )

    def all(self) -> list[TierSpec]:
        return list(self._tiers.values())

    def enabled(self) -> list[TierSpec]:
        return [spec for spec in self._tiers.values() if spec.enabled]

    def get(self, tier: str) -> TierSpec | None:
        return self._tiers.get(tier)

    def permitted(self, allowed: Sequence[str]) -> list[TierSpec]:
        return [spec for spec in self.enabled() if spec.tier in allowed]

    def warmup_spec(self) -> TierSpec | None:
        standard = self._tiers["standard"]
        if standard.enabled:
            return standard
        enabled = self.enabled()
        return enabled[0] if enabled else None

    @staticmethod
    def is_usable(spec: TierSpec, health: ProviderHealth | None) -> bool:
        if not spec.enabled or spec.model is None or health is None:
            return False
        key = model_key(spec.model)
        if key in health.rejected:
            return False
        return health.installed is None or key in health.installed

    def resolve(
        self, tier: str, permitted: Sequence[TierSpec], health: ProviderHealth
    ) -> Resolution:
        """The requested tier if usable, else the nearest usable permitted tier.

        Reasoning is a manual choice, so a fallback never lands on a reasoning tier.
        """
        by_name = {spec.tier: spec for spec in permitted}
        requested = by_name.get(tier)
        if requested is not None and self.is_usable(requested, health):
            return Resolution(requested, None)
        order = list(TIER_ORDER)
        position = order.index(tier)
        for candidate in sorted(order, key=lambda name: abs(order.index(name) - position)):
            spec = by_name.get(candidate)
            if spec is not None and not spec.think and self.is_usable(spec, health):
                return Resolution(spec, tier)
        if any(model_key(spec.model or "") in health.rejected for spec in permitted):
            raise ApiError("model_not_permitted", "the configured model is a remote stub")
        raise ApiError("model_not_installed", f"no usable model for tier {tier}")
