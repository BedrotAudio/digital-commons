"""Ollama adapter. The cloud gate runs before every chat. See docs/security.md."""

from __future__ import annotations

import asyncio
import json
import re
from collections.abc import AsyncIterator
from datetime import UTC, datetime
from typing import Any

import httpx

from app.config.settings import ProviderConfig, Settings
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
    model_key,
)
from app.providers.client import aiter_lf_lines, create_upstream_client

_OVERFLOW = re.compile(
    r"request \((\d+) tokens\) exceeds the available context size \((\d+) tokens\)"
)
_NO_THINKING = "does not support thinking"
_REMOTE_FIELDS = ("remote_model", "remote_host")
_PROBE_TIMEOUT = httpx.Timeout(10.0, connect=3.0)


def _carries_remote(document: Any) -> bool:
    return isinstance(document, dict) and any(document.get(key) for key in _REMOTE_FIELDS)


def _utc(value: Any) -> str | None:
    """Ollama reports local time with nanoseconds. docs/api.md promises UTC."""
    if not isinstance(value, str):
        return None
    try:
        moment = datetime.fromisoformat(value).astimezone(UTC)
    except (ValueError, OverflowError):
        return None
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


class OllamaProvider:
    kind = "ollama"
    supports_warmup = True

    def __init__(
        self,
        name: str,
        config: ProviderConfig,
        settings: Settings,
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self.name = name
        self.display_name = config.display_name or "Ollama"
        self.base_url = (config.base_url or "").rstrip("/")
        self._config = config
        self._models = sorted(
            {spec.name for spec in settings.models.catalog.get(name, {}).values()}
        )
        self._client = create_upstream_client(
            connect_timeout_s=config.connect_timeout_s, transport=transport
        )

    def install_hint(self, model: str) -> str | None:
        return f"ollama pull {model}"

    async def aclose(self) -> None:
        await self._client.aclose()

    async def _request(self, method: str, path: str, **kwargs: Any) -> httpx.Response:
        try:
            response = await self._client.request(
                method, self.base_url + path, timeout=_PROBE_TIMEOUT, **kwargs
            )
        except httpx.TimeoutException as error:
            raise ApiError("timeout", f"{path}: {type(error).__name__}") from None
        except httpx.TransportError as error:
            raise ApiError("provider_unavailable", f"{path}: {type(error).__name__}") from None
        if 300 <= response.status_code < 400:
            raise ApiError("provider_error", f"{path}: refused redirect {response.status_code}")
        return response

    async def _read_gate(self) -> tuple[bool, str | None]:
        """Only `cloud.disabled` being exactly true passes. Anything else fails closed."""
        response = await self._request("GET", "/api/status")
        if response.status_code != 200:
            return False, f"/api/status returned {response.status_code}"
        try:
            body = response.json()
        except ValueError:
            return False, "/api/status returned malformed JSON"
        cloud = body.get("cloud") if isinstance(body, dict) else None
        if not isinstance(cloud, dict) or cloud.get("disabled") is not True:
            return False, "/api/status does not report cloud.disabled true"
        return True, None

    async def _require_gate(self) -> None:
        passed, reason = await self._read_gate()
        if not passed:
            raise ApiError("model_not_permitted", reason)

    async def cloud_gate(self) -> bool | None:
        try:
            passed, _ = await self._read_gate()
        except ApiError as error:
            return False if error.code == "provider_error" else None
        return passed

    async def _inventory(self) -> tuple[set[str], set[str]]:
        """Return (installed, rejected) as model keys, using /api/tags and /api/show."""
        response = await self._request("GET", "/api/tags")
        if response.status_code != 200:
            raise ApiError("provider_error", f"/api/tags returned {response.status_code}")
        try:
            entries = response.json().get("models") or []
        except (ValueError, AttributeError):
            raise ApiError("provider_error", "/api/tags returned malformed JSON") from None
        installed: set[str] = set()
        rejected: set[str] = set()
        for entry in entries:
            if not isinstance(entry, dict):
                continue
            key = model_key(str(entry.get("name") or entry.get("model") or ""))
            installed.add(key)
            if _carries_remote(entry):
                rejected.add(key)
        for name in self._models:
            key = model_key(name)
            if key in installed and key not in rejected and await self._is_remote(name):
                rejected.add(key)
        return installed, rejected

    async def _is_remote(self, name: str) -> bool:
        response = await self._request("POST", "/api/show", json={"model": name})
        if response.status_code != 200:
            return False
        try:
            return _carries_remote(response.json())
        except ValueError:
            return False

    async def _loaded(self) -> list[dict]:
        response = await self._request("GET", "/api/ps")
        if response.status_code != 200:
            return []
        try:
            entries = response.json().get("models") or []
        except (ValueError, AttributeError):
            return []
        return [entry for entry in entries if isinstance(entry, dict)]

    async def _version(self) -> str | None:
        response = await self._request("GET", "/api/version")
        if response.status_code != 200:
            return None
        try:
            version = response.json().get("version")
        except (ValueError, AttributeError):
            return None
        return version if isinstance(version, str) else None

    async def health(self) -> ProviderHealth:
        try:
            passed, reason = await self._read_gate()
        except ApiError as error:
            if error.code == "provider_error":
                return ProviderHealth(reachable=True, cloud_gate=False, error=error.detail)
            return ProviderHealth(reachable=False, error=error.detail)
        health = ProviderHealth(reachable=True, cloud_gate=passed, error=reason)
        try:
            health.version = await self._version()
            health.installed, health.rejected = await self._inventory()
            health.loaded_models = [
                {
                    "name": entry.get("name") or entry.get("model"),
                    "size_bytes": entry.get("size"),
                    "gpu_bytes": entry.get("size_vram"),
                    "context_tokens": entry.get("context_length"),
                    "expires_at": _utc(entry.get("expires_at")),
                }
                for entry in await self._loaded()
            ]
        except ApiError as error:
            health.error = health.error or error.detail
            if health.installed is None:
                # What is installed is unknown, which is not the same as nothing. A
                # restart or a slow reply between probes lands here.
                health.reachable = False
        return health

    async def preflight(self) -> ProviderHealth:
        await self._require_gate()
        installed, rejected = await self._inventory()
        return ProviderHealth(
            reachable=True, cloud_gate=True, installed=installed, rejected=rejected
        )

    async def _needs_loading(self, spec: TierSpec) -> bool:
        try:
            loaded = await self._loaded()
        except ApiError:
            return True
        wanted = model_key(spec.model or "")
        for entry in loaded:
            name = str(entry.get("name") or entry.get("model") or "")
            if model_key(name) == wanted and entry.get("context_length") == spec.context:
                return False
        return True

    async def stream(self, request: InferenceRequest) -> AsyncIterator[Chunk]:
        spec = request.spec
        await self._require_gate()
        needs_loading = await self._needs_loading(spec)
        if needs_loading:
            yield StatusChunk("loading_model", LOADING_MESSAGE)
        payload = {
            "model": spec.model,
            "messages": request.messages,
            "stream": True,
            "think": spec.think,
            "keep_alive": self._config.keep_alive,
            "truncate": False,
            "options": {
                "num_ctx": spec.context,
                "num_predict": spec.max_output,
                "temperature": spec.temperature,
            },
        }
        loop = asyncio.get_running_loop()
        finished = False
        try:
            # Ollama sends no headers until the first token, so one deadline covers
            # the wait for headers and the first chunk. After that it is the idle limit.
            async with asyncio.timeout(self._config.first_token_timeout_s) as deadline:
                async with self._client.stream(
                    "POST", self.base_url + "/api/chat", json=payload
                ) as response:
                    await self._check_chat_response(response, needs_loading, spec)
                    async for line in aiter_lf_lines(response):
                        if not line.strip():
                            continue
                        deadline.reschedule(loop.time() + self._config.stream_idle_timeout_s)
                        chunk = self._parse_chunk(line)
                        for item in self._read_chunk(chunk):
                            yield item
                        if chunk.get("done") is True:
                            finished = True
                            break
        except TimeoutError:
            raise ApiError("timeout", "no data from /api/chat within the limit") from None
        except httpx.TimeoutException as error:
            raise ApiError("timeout", f"/api/chat: {type(error).__name__}") from None
        except httpx.TransportError as error:
            raise ApiError("provider_unavailable", f"/api/chat: {type(error).__name__}") from None
        if not finished:
            raise ApiError("provider_error", "/api/chat ended without a final chunk")

    @staticmethod
    def _parse_chunk(line: str) -> dict:
        try:
            chunk = json.loads(line)
        except ValueError:
            raise ApiError("provider_error", "/api/chat sent malformed JSON") from None
        if not isinstance(chunk, dict):
            raise ApiError("provider_error", "/api/chat sent an unexpected chunk")
        return chunk

    @staticmethod
    def _read_chunk(chunk: dict) -> list[Chunk]:
        if any(key in chunk for key in _REMOTE_FIELDS):
            raise ApiError("model_not_permitted", "a stream chunk carried remote fields")
        if chunk.get("error"):
            raise ApiError("provider_error", "/api/chat reported an error mid-stream")
        items: list[Chunk] = []
        message = chunk.get("message")
        if isinstance(message, dict):
            thinking = message.get("thinking")
            if isinstance(thinking, str) and thinking:
                items.append(ThinkingChunk(thinking))
            content = message.get("content")
            if isinstance(content, str) and content:
                items.append(DeltaChunk(content))
        if chunk.get("done") is True:
            items.append(_final_chunk(chunk))
        return items

    async def _check_chat_response(
        self, response: httpx.Response, was_loading: bool, spec: TierSpec
    ) -> None:
        status = response.status_code
        if status == 200:
            return
        if 300 <= status < 400:
            raise ApiError("provider_error", f"/api/chat: refused redirect {status}")
        body = (await response.aread())[:4096].decode("utf-8", "replace")
        match = _OVERFLOW.search(body)
        if status == 400 and match:
            raise ContextOverflow(int(match.group(1)), int(match.group(2)))
        if 400 <= status < 500 and _NO_THINKING in body:
            # Trying again cannot help: the tier asks for reasoning the model lacks.
            raise ApiError(
                "model_not_installed",
                f"/api/chat: {spec.model} does not support thinking. Set think: false for "
                f"the {spec.tier} tier in config/models.yaml, or name a model that can reason.",
            )
        if status == 403:
            raise ApiError("model_not_permitted", "/api/chat refused the model")
        if status == 404:
            raise ApiError("model_not_installed", "/api/chat does not know the model")
        if status >= 500 and was_loading:
            raise ApiError("model_load_failed", f"/api/chat returned {status}")
        raise ApiError("provider_error", f"/api/chat returned {status}")

    async def warm(self, spec: TierSpec) -> None:
        await self._require_gate()
        if await self._is_remote(spec.model or ""):
            raise ApiError("model_not_permitted", "the model is a remote stub")
        payload = {
            "model": spec.model,
            "messages": [],
            "stream": False,
            "keep_alive": self._config.keep_alive,
            "options": {"num_ctx": spec.context},
        }
        limit = httpx.Timeout(
            self._config.first_token_timeout_s, connect=self._config.connect_timeout_s
        )
        try:
            response = await self._client.post(
                self.base_url + "/api/chat", json=payload, timeout=limit
            )
        except httpx.TimeoutException as error:
            raise ApiError("timeout", f"warm-up: {type(error).__name__}") from None
        except httpx.TransportError as error:
            raise ApiError("provider_unavailable", f"warm-up: {type(error).__name__}") from None
        if 300 <= response.status_code < 400:
            raise ApiError("provider_error", f"warm-up: refused redirect {response.status_code}")
        if response.status_code == 404:
            raise ApiError("model_not_installed", "warm-up: the model is not installed")
        if response.status_code != 200:
            raise ApiError("model_load_failed", f"warm-up returned {response.status_code}")
        try:
            if _carries_remote(response.json()):
                raise ApiError("model_not_permitted", "warm-up reply carried remote fields")
        except ValueError:
            raise ApiError("model_load_failed", "warm-up reply was malformed") from None


def _count(chunk: dict, key: str) -> int | None:
    value = chunk.get(key)
    return value if isinstance(value, int) and not isinstance(value, bool) else None


def _final_chunk(chunk: dict) -> FinalChunk:
    reason = "length" if chunk.get("done_reason") == "length" else "stop"
    completion = _count(chunk, "eval_count")
    eval_ns = _count(chunk, "eval_duration")
    load_ns = _count(chunk, "load_duration")
    return FinalChunk(
        finish_reason=reason,
        prompt_tokens=_count(chunk, "prompt_eval_count"),
        completion_tokens=completion,
        load_ms=load_ns // 1_000_000 if load_ns is not None else None,
        tokens_per_second=round(completion / (eval_ns / 1e9), 2)
        if completion and eval_ns
        else None,
    )
