"""A demonstration provider. It needs no model and says so in every reply."""

from __future__ import annotations

import asyncio
import re
import time
from collections.abc import AsyncIterator

from app.config.settings import ProviderConfig
from app.models.domain import TierSpec
from app.providers.base import (
    Chunk,
    DeltaChunk,
    FinalChunk,
    InferenceRequest,
    ProviderHealth,
    ThinkingChunk,
)
from app.services.context import (
    ATTACHMENT_END,
    ATTACHMENT_PATTERN,
    CODE_MARKER,
    DOCUMENT_END,
    DOCUMENT_PATTERN,
)

_TOKENS = re.compile(r"\s*\S+|\s+")
_QUOTE_LIMIT = 120
_DOCUMENT_BLOCK = re.compile(
    DOCUMENT_PATTERN + r"\n(.*?)\n" + re.escape(DOCUMENT_END), re.DOTALL
)
_ATTACHMENT_BLOCK = re.compile(
    ATTACHMENT_PATTERN + r"\n.*?\n?" + re.escape(ATTACHMENT_END), re.DOTALL
)

DEMO_NOTICE = (
    "**This is a demonstration reply.** No AI wrote it, and nothing you typed was "
    "analyzed. It shows how a real answer looks and flows."
)
_THINKING = (
    "This is simulated reasoning for the demonstration. A real model would work "
    "through the question here before answering. On a computer without a graphics "
    "card, this step can take much longer than the answer itself."
)
_CODE_SAMPLE = '''```python
from datetime import date, timedelta


def working_days_until(deadline: date, start: date) -> int:
    """Count Monday to Friday days from start up to, not including, deadline."""
    days = 0
    current = start
    while current < deadline:
        if current.weekday() < 5:
            days += 1
        current += timedelta(days=1)
    return days


print(working_days_until(date(2026, 12, 1), date(2026, 11, 2)))
```'''


def _last_user_text(messages: list[dict[str, str]]) -> str:
    for message in reversed(messages):
        if message["role"] == "user":
            return message["content"]
    return ""


def compose_reply(messages: list[dict[str, str]]) -> str:
    text = _last_user_text(messages)
    system = messages[0]["content"] if messages and messages[0]["role"] == "system" else ""
    names = _ATTACHMENT_BLOCK.findall(text)
    documents = _DOCUMENT_BLOCK.findall(text)
    question = _ATTACHMENT_BLOCK.sub("", _DOCUMENT_BLOCK.sub("", text))
    question = " ".join(question.split())

    parts = [DEMO_NOTICE]
    if question:
        quoted = question if len(question) <= _QUOTE_LIMIT else question[:_QUOTE_LIMIT] + "..."
        parts.append(f"You asked:\n\n> {quoted}")
    if names:
        listing = "\n".join(f"- `{name}`" for name in names)
        parts.append(f"## Attached files\n\nThe demonstration received:\n\n{listing}")
    if documents:
        lines = []
        for number, name, body in documents:
            sentence = re.split(r"(?<=[.!?])\s", " ".join(body.split()), maxsplit=1)[0]
            lines.append(f"- *{name}*: {sentence} [{number}]")
        parts.append(
            "## From approved documents\n\nThese passages matched your question:\n\n"
            + "\n".join(lines)
        )
    if CODE_MARKER in system:
        parts.append(
            "## Example\n\nCode assistant replies look like this sample, which counts "
            f"working days:\n\n{_CODE_SAMPLE}"
        )
    parts.append(
        "## What happens next\n\n"
        "1. Ask your IT team to finish setting up the AI service.\n"
        "2. Ask the same question again.\n\n"
        "Until then, this reply stays much the same whatever you ask."
    )
    return "\n\n".join(parts)


class MockProvider:
    kind = "mock"
    supports_warmup = False
    base_url = None

    def __init__(self, name: str, config: ProviderConfig) -> None:
        self.name = name
        self.display_name = config.display_name or "Demonstration"
        self._interval = 1.0 / config.tokens_per_second

    def install_hint(self, model: str) -> str | None:
        return None

    async def aclose(self) -> None:
        return None

    async def cloud_gate(self) -> bool | None:
        return None

    async def warm(self, spec: TierSpec) -> None:
        return None

    async def health(self) -> ProviderHealth:
        return ProviderHealth(reachable=True)

    async def preflight(self) -> ProviderHealth:
        return ProviderHealth(reachable=True)

    async def _paced(self, text: str, budget: int) -> AsyncIterator[str]:
        """Yield word-sized pieces on a steady clock, at most `budget` of them."""
        began = time.monotonic()
        for index, token in enumerate(_TOKENS.findall(text)[:budget]):
            delay = began + (index + 1) * self._interval - time.monotonic()
            await asyncio.sleep(max(delay, 0))
            yield token

    async def stream(self, request: InferenceRequest) -> AsyncIterator[Chunk]:
        spec = request.spec
        budget = spec.max_output or 0
        used = 0
        if spec.think:
            async for token in self._paced(_THINKING, budget):
                used += 1
                yield ThinkingChunk(token)
        reply = compose_reply(request.messages)
        left = budget - used
        async for token in self._paced(reply, left):
            yield DeltaChunk(token)
        total = len(_TOKENS.findall(reply))
        yield FinalChunk(finish_reason="length" if total > left else "stop", model=spec.model)
