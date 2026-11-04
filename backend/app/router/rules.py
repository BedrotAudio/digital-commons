"""RuleBasedRouter: simple, explainable signals. A placeholder, not intelligence."""

from __future__ import annotations

import re
from collections.abc import Sequence

from app.models.domain import TierSpec
from app.router.base import RouteDecision, RouteSignals

_CODE_MARKERS = re.compile(
    r"```|\bdef \w+\(|\bclass \w+|\bfunction \w+\(|\bSELECT\b.+\bFROM\b|#include\b|\bimport \w+|"
    r"\bstack trace\b|\bregex\b|\bpython\b|\bjavascript\b|\bsql\b",
    re.IGNORECASE | re.DOTALL,
)
_COMPLEX_WORDS = (
    "analyze",
    "analyse",
    "compare",
    "evaluate",
    "explain why",
    "implications",
    "reconcile",
    "recommend",
    "step by step",
    "strategy",
    "trade-off",
    "tradeoff",
)
_PREFERENCE = {
    "fast": ("fast", "standard", "advanced"),
    "standard": ("standard", "advanced", "fast"),
    "advanced": ("advanced", "standard", "fast"),
}


class RuleBasedRouter:
    def choose(self, signals: RouteSignals, tiers: Sequence[TierSpec]) -> RouteDecision | None:
        by_name = {tier.tier: tier for tier in tiers if tier.enabled and not tier.think}
        wanted, reason = self._wanted(signals)
        for candidate in _PREFERENCE[wanted]:
            if candidate in by_name:
                closest = "" if candidate == wanted else " (closest permitted tier)"
                return RouteDecision(candidate, reason + closest)
        return None

    @staticmethod
    def _wanted(signals: RouteSignals) -> tuple[str, str]:
        text = signals.message
        lowered = text.lower()
        complex_hits = sum(word in lowered for word in _COMPLEX_WORDS)
        if signals.assistant == "code" or _CODE_MARKERS.search(text):
            return "advanced", "Code in the request"
        if complex_hits >= 2 or len(text) > 2000:
            return "advanced", "Complex request"
        if signals.attachment_count:
            return "standard", "Attachment present"
        if signals.use_documents:
            return "standard", "Approved documents requested"
        if complex_hits or len(text) > 240:
            return "standard", "General request"
        return "fast", "Short question"
