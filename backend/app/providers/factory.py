"""Builds the single active provider."""

from __future__ import annotations

from collections.abc import Mapping

import httpx

from app.config.settings import Settings
from app.providers.base import InferenceProvider
from app.providers.inference_api import InferenceApiProvider
from app.providers.mock import MockProvider
from app.providers.ollama import OllamaProvider


def build_provider(
    settings: Settings,
    env: Mapping[str, str],
    transport: httpx.AsyncBaseTransport | None = None,
) -> InferenceProvider:
    name = settings.provider_name
    config = settings.provider
    if config.kind == "ollama":
        return OllamaProvider(name, config, settings, transport)
    if config.kind == "inference_api":
        return InferenceApiProvider(name, config, env, transport)
    return MockProvider(name, config)
