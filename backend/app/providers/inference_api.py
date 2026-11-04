"""Relay to an organization model server. See the model host contract in docs/api.md."""

from __future__ import annotations

import asyncio
import json
from collections.abc import AsyncIterator, Mapping
from typing import Any

import httpx

from app.config.settings import ROOT, ProviderConfig
from app.models.domain import TierSpec
from app.models.errors import ApiError, ContextOverflow
from app.providers.base import (
    LOADING_MESSAGE,
    Chunk,
    DeltaChunk,
    FinalChunk,
    InferenceRequest,
    ProviderHealth,
    StatusChunk,
    ThinkingChunk,
)
from app.providers.client import aiter_lf_lines, create_upstream_client

# Upstream status text could name a location, so only the phase is read.
_STATUS_MESSAGES = {
    "queued": "Waiting for the assistant to become available.",
    "loading_model": LOADING_MESSAGE,
    "reading": "Reading your request.",
    "generating": "Writing a response.",
}
_RELAYED_ERRORS = {
    "busy",
    "model_load_failed",
    "model_not_installed",
    "model_not_permitted",
    "provider_unavailable",
    "timeout",
}


def _failure_from(code: Any, origin: str) -> ApiError:
    if code == "context_too_large":
        return ContextOverflow()
    if code in _RELAYED_ERRORS:
        return ApiError(code, f"model server reported {code}")
    return ApiError("provider_error", f"{origin}: the model server reported a failure")


class InferenceApiProvider:
    kind = "inference_api"
    supports_warmup = False

    def __init__(
        self,
        name: str,
        config: ProviderConfig,
        env: Mapping[str, str],
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self.name = name
        self.display_name = config.display_name or "Organization server"
        self.base_url = (config.base_url or "").rstrip("/")
        self._config = config
        self._token = env.get(config.token_env or "", "")
        self._client = create_upstream_client(
            connect_timeout_s=config.connect_timeout_s,
            ca_bundle=ROOT / config.ca_bundle if config.ca_bundle else None,
            transport=transport,
        )

    def install_hint(self, model: str) -> str | None:
        return None

    async def aclose(self) -> None:
        await self._client.aclose()

    async def cloud_gate(self) -> bool | None:
        return None

    async def warm(self, spec: TierSpec) -> None:
        return None

    async def _healthz(self) -> None:
        try:
            response = await self._client.get(
                self.base_url + "/healthz",
                timeout=httpx.Timeout(5.0, connect=self._config.connect_timeout_s),
            )
        except httpx.TimeoutException as error:
            raise ApiError("timeout", f"/healthz: {type(error).__name__}") from None
        except httpx.TransportError as error:
            raise ApiError("provider_unavailable", f"/healthz: {type(error).__name__}") from None
        if 300 <= response.status_code < 400:
            raise ApiError("provider_error", f"/healthz: refused redirect {response.status_code}")
        if response.status_code != 200:
            raise ApiError("provider_unavailable", f"/healthz returned {response.status_code}")

    async def health(self) -> ProviderHealth:
        try:
            await self._healthz()
        except ApiError as error:
            return ProviderHealth(reachable=False, error=error.detail)
        return ProviderHealth(reachable=True)

    async def preflight(self) -> ProviderHealth:
        await self._healthz()
        return ProviderHealth(reachable=True)

    async def stream(self, request: InferenceRequest) -> AsyncIterator[Chunk]:
        loop = asyncio.get_running_loop()
        idle = self._config.stream_idle_timeout_s
        headers = {"Authorization": f"Bearer {self._token}"}
        body = {"tier": request.spec.tier, "messages": request.messages}
        try:
            async with asyncio.timeout(idle) as deadline:
                async with self._client.stream(
                    "POST", self.base_url + "/v1/inference/stream", json=body, headers=headers
                ) as response:
                    await self._check_response(response)
                    event = ""
                    data: list[str] = []
                    async for line in aiter_lf_lines(response):
                        deadline.reschedule(loop.time() + idle)
                        if line.startswith(":"):
                            continue
                        if line.startswith("event:"):
                            event = line[6:].strip()
                        elif line.startswith("data:"):
                            data.append(line[5:].removeprefix(" "))
                        elif not line and event:
                            chunk = _read_event(event, "\n".join(data))
                            event, data = "", []
                            if chunk is not None:
                                yield chunk
                                if isinstance(chunk, FinalChunk):
                                    return
        except TimeoutError:
            raise ApiError("timeout", "the model server went quiet") from None
        except httpx.TimeoutException as error:
            raise ApiError("timeout", f"stream: {type(error).__name__}") from None
        except httpx.TransportError as error:
            raise ApiError("provider_unavailable", f"stream: {type(error).__name__}") from None
        raise ApiError("provider_error", "the model server's stream ended without done or error")

    async def _check_response(self, response: httpx.Response) -> None:
        status = response.status_code
        if status == 200:
            return
        if 300 <= status < 400:
            raise ApiError("provider_error", f"stream: refused redirect {status}")
        body = (await response.aread())[:4096]
        try:
            code = json.loads(body)["error"]["code"]
        except (ValueError, KeyError, TypeError):
            code = None
        raise _failure_from(code, f"stream returned {status}")


def _read_event(event: str, data: str) -> Chunk | None:
    if event not in ("status", "thinking", "delta", "done", "error"):
        return None
    try:
        payload = json.loads(data)
    except ValueError:
        raise ApiError("provider_error", "the model server sent malformed JSON") from None
    if not isinstance(payload, dict):
        raise ApiError("provider_error", "the model server sent an unexpected event")
    if event == "error":
        raise _failure_from(payload.get("code"), "stream event")
    if event == "status":
        phase = payload.get("phase")
        message = _STATUS_MESSAGES.get(phase) if isinstance(phase, str) else None
        return StatusChunk(phase, message) if message else None
    if event in ("thinking", "delta"):
        text = payload.get("text")
        if not isinstance(text, str) or not text:
            return None
        return ThinkingChunk(text) if event == "thinking" else DeltaChunk(text)
    return _final_from(payload)


def _number(source: Mapping[str, Any], key: str) -> Any:
    value = source.get(key)
    return value if isinstance(value, int | float) and not isinstance(value, bool) else None


def _final_from(payload: dict) -> FinalChunk:
    usage = payload.get("usage") if isinstance(payload.get("usage"), dict) else {}
    model = payload.get("model")
    estimated = usage.get("estimated") is True
    return FinalChunk(
        finish_reason="length" if payload.get("finish_reason") == "length" else "stop",
        prompt_tokens=None if estimated else _number(usage, "prompt_tokens"),
        completion_tokens=None if estimated else _number(usage, "completion_tokens"),
        load_ms=_number(usage, "load_ms"),
        tokens_per_second=_number(usage, "tokens_per_second"),
        model=model[:200] if isinstance(model, str) else None,
    )
