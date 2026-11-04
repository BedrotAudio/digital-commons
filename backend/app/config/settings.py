"""Gateway settings: YAML files, environment overrides and validation."""

from __future__ import annotations

import logging
import os
import sys
from collections.abc import Mapping
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Literal

import yaml
from pydantic import BaseModel, ConfigDict, Field, ValidationError

from app.security.locality import (
    has_credentials,
    is_loopback_host,
    is_loopback_url,
    split_url,
)
from app.security.paths import DataDirRefused, check_data_dir, resolve_data_dir

ROOT = Path(__file__).resolve().parents[3]
TIER_ORDER = ("fast", "standard", "advanced", "specialist")
MIN_SHARED_TOKEN_LENGTH = 32

logger = logging.getLogger("digital_commons.config")


class ConfigError(Exception):
    """A refusal to start. The message is shown to the person running the gateway."""


class _Section(BaseModel):
    model_config = ConfigDict(extra="forbid", hide_input_in_errors=True)


class ApplicationConfig(_Section):
    name: str = "Digital Commons"
    stage: str = "beta"
    notice: str = ""


class GatewayConfig(_Section):
    host: str = "127.0.0.1"
    port: int = Field(default=8000, ge=0, le=65535)
    docs_enabled: bool = False


class StorageConfig(_Section):
    data_dir: str | None = None


class InferenceConfig(_Section):
    default_provider: str = "ollama"
    max_concurrent_requests: int = Field(default=1, ge=1)
    max_queued_requests: int = Field(default=8, ge=0)
    warm_on_start: bool = True


class ProviderConfig(_Section):
    kind: Literal["ollama", "mock", "inference_api"]
    display_name: str = ""
    base_url: str | None = None
    keep_alive: str | None = None
    connect_timeout_s: float = Field(default=3, gt=0)
    first_token_timeout_s: float = Field(default=900, gt=0)
    stream_idle_timeout_s: float = Field(default=120, gt=0)
    tokens_per_second: float = Field(default=40, gt=0)
    token_env: str | None = None
    ca_bundle: str | None = None


class InferenceServiceConfig(_Section):
    enabled: bool = False
    token_env: str = "DC_SERVICE_TOKEN"


class SecurityConfig(_Section):
    allowed_upstream_hosts: list[str] = Field(default_factory=list)
    allowed_client_hosts: list[str] = Field(default_factory=lambda: ["127.0.0.1", "localhost"])


class PermissionsConfig(_Section):
    advanced_mode: bool = False
    manual_model_selection: bool = False
    view_diagnostics: bool = False
    search_documents: bool = False
    allowed_tiers: list[str] = Field(default_factory=list)


class UserConfig(_Section):
    user_id: str
    display_name: str
    groups: list[str] = Field(default_factory=list)
    permissions: PermissionsConfig = Field(default_factory=PermissionsConfig)


class IdentityConfig(_Section):
    mode: Literal["local", "shared_token"] = "local"
    token_env: str = "DC_GATEWAY_TOKEN"
    user: UserConfig


class ConversationsConfig(_Section):
    retention_days: int = Field(default=30, ge=0)


class LoggingConfig(_Section):
    level: Literal["DEBUG", "INFO", "WARNING", "ERROR"] = "INFO"


class DocumentsConfig(_Section):
    path: str = "data/sample_documents"
    max_results: int = Field(default=4, ge=1)


class LimitsConfig(_Section):
    max_message_chars: int = Field(default=15000, ge=1)
    max_attachments: int = Field(default=4, ge=0)
    max_attachment_bytes: int = Field(default=262144, ge=1)
    attachment_extensions: list[str] = Field(
        default_factory=lambda: [".txt", ".md", ".csv", ".tsv"]
    )


class SuggestionConfig(_Section):
    id: str
    title: str
    subtitle: str
    action: Literal["prompt", "attach", "attach_two", "documents"]
    prompt: str = ""


class TierMetaConfig(_Section):
    label: str
    description: str
    enabled: bool = True


class ModelSpecConfig(_Section):
    name: str
    context: int = Field(gt=0)
    max_output: int = Field(gt=0)
    temperature: float = Field(ge=0)
    think: bool = False


class ModelsConfig(_Section):
    tiers: dict[str, TierMetaConfig]
    catalog: dict[str, dict[str, ModelSpecConfig]]


class Settings(_Section):
    application: ApplicationConfig = Field(default_factory=ApplicationConfig)
    gateway: GatewayConfig = Field(default_factory=GatewayConfig)
    storage: StorageConfig = Field(default_factory=StorageConfig)
    inference: InferenceConfig = Field(default_factory=InferenceConfig)
    providers: dict[str, ProviderConfig]
    inference_service: InferenceServiceConfig = Field(default_factory=InferenceServiceConfig)
    security: SecurityConfig = Field(default_factory=SecurityConfig)
    identity: IdentityConfig
    conversations: ConversationsConfig = Field(default_factory=ConversationsConfig)
    logging: LoggingConfig = Field(default_factory=LoggingConfig)
    documents: DocumentsConfig = Field(default_factory=DocumentsConfig)
    limits: LimitsConfig = Field(default_factory=LimitsConfig)
    suggestions: list[SuggestionConfig] = Field(default_factory=list)
    models: ModelsConfig
    data_dir: Path = Path()

    @property
    def provider_name(self) -> str:
        return self.inference.default_provider

    @property
    def provider(self) -> ProviderConfig:
        return self.providers[self.provider_name]

    @property
    def documents_path(self) -> Path:
        return _from_root(self.documents.path)


@dataclass(frozen=True)
class Tuning:
    """Timings fixed by docs/api.md. Tests shorten them; production never does."""

    ping_interval_s: float = 10.0
    status_interval_s: float = 5.0
    unfinished_turn_wait_s: float = 5.0
    warmup_poll_s: float = 2.0
    retention_interval_s: float = 3600.0
    port_probe_timeout_s: float = 0.25


def _from_root(value: str) -> Path:
    path = Path(value)
    return path if path.is_absolute() else ROOT / path


def _read_yaml(path: Path) -> dict[str, Any]:
    try:
        loaded = yaml.safe_load(path.read_text(encoding="utf-8"))
    except OSError as error:
        raise ConfigError(f"Cannot read {path}: {error.strerror}") from None
    except yaml.YAMLError:
        raise ConfigError(f"{path} is not valid YAML.") from None
    if loaded is None:
        return {}
    if not isinstance(loaded, dict):
        raise ConfigError(f"{path} must contain a mapping.")
    return loaded


def _merge(base: dict[str, Any], extra: Mapping[str, Any]) -> dict[str, Any]:
    merged = dict(base)
    for key, value in extra.items():
        if isinstance(value, Mapping) and isinstance(merged.get(key), dict):
            merged[key] = _merge(merged[key], value)
        else:
            merged[key] = value
    return merged


def _config_file(env: Mapping[str, str], variable: str, default: Path) -> Path:
    value = env.get(variable)
    return _from_root(value) if value else default


def load_settings(
    env: Mapping[str, str] | None = None,
    *,
    overrides: Mapping[str, Any] | None = None,
) -> Settings:
    """Read and validate configuration, or raise ConfigError with a plain reason."""
    env = os.environ if env is None else env
    config_path = _config_file(env, "DC_CONFIG", ROOT / "config" / "development.yaml")
    raw = _read_yaml(config_path)
    local = config_path.with_name(f"{config_path.stem}.local.yaml")
    if local.is_file():
        raw = _merge(raw, _read_yaml(local))
    raw["models"] = _read_yaml(_config_file(env, "DC_MODELS_CONFIG", ROOT / "config" / "models.yaml"))
    if overrides:
        raw = _merge(raw, overrides)
    provider = env.get("DC_PROVIDER")
    if provider:
        raw = _merge(raw, {"inference": {"default_provider": provider}})

    try:
        data_dir = resolve_data_dir(
            env, sys.platform, Path.home(), (raw.get("storage") or {}).get("data_dir")
        )
        check_data_dir(Path(data_dir), env)
    except DataDirRefused as error:
        raise ConfigError(str(error)) from None
    raw["data_dir"] = Path(data_dir)

    try:
        settings = Settings.model_validate(raw)
    except ValidationError as error:
        problems = "; ".join(
            f"{'.'.join(str(part) for part in item['loc'])}: {item['type']}"
            for item in error.errors(include_input=False, include_context=False)
        )
        raise ConfigError(f"Invalid configuration: {problems}") from None
    validate_settings(settings, env)
    return settings


def validate_settings(settings: Settings, env: Mapping[str, str]) -> None:
    _validate_identity(settings, env)
    _validate_providers(settings, env)
    _validate_models(settings)


def _validate_identity(settings: Settings, env: Mapping[str, str]) -> None:
    identity = settings.identity
    permissions = identity.user.permissions
    if not permissions.advanced_mode:
        if permissions.manual_model_selection:
            raise ConfigError("manual_model_selection requires advanced_mode.")
        if permissions.view_diagnostics:
            raise ConfigError("view_diagnostics requires advanced_mode.")
    unknown = sorted(set(permissions.allowed_tiers) - set(TIER_ORDER))
    if unknown:
        raise ConfigError(f"allowed_tiers names unknown tiers: {', '.join(unknown)}.")
    if identity.mode == "shared_token":
        token = env.get(identity.token_env, "")
        if len(token) < MIN_SHARED_TOKEN_LENGTH:
            raise ConfigError(
                f"shared_token mode needs {identity.token_env} set to at least "
                f"{MIN_SHARED_TOKEN_LENGTH} characters."
            )


def _validate_providers(settings: Settings, env: Mapping[str, str]) -> None:
    if settings.provider_name not in settings.providers:
        raise ConfigError(f"Unknown provider {settings.provider_name!r}.")
    for name, provider in settings.providers.items():
        if provider.kind == "ollama" and not is_loopback_url(provider.base_url):
            raise ConfigError(
                f"Provider {name}: an ollama base_url must be a loopback address. "
                "Only inference_api may be remote."
            )
        if provider.kind == "inference_api":
            _validate_url_shape(name, provider)

    active = settings.provider
    if active.kind == "inference_api":
        _, host = split_url(active.base_url)
        allowed = {item.lower() for item in settings.security.allowed_upstream_hosts}
        if not is_loopback_host(host) and (host or "").lower() not in allowed:
            raise ConfigError(
                f"Provider {settings.provider_name}: host {host} is not listed in "
                "security.allowed_upstream_hosts."
            )
        if not active.token_env or not env.get(active.token_env):
            raise ConfigError(
                f"Provider {settings.provider_name} needs its token: set {active.token_env}."
            )

    service = settings.inference_service
    if service.enabled and len(env.get(service.token_env, "")) < MIN_SHARED_TOKEN_LENGTH:
        raise ConfigError(
            f"inference_service needs {service.token_env} set to at least "
            f"{MIN_SHARED_TOKEN_LENGTH} characters."
        )


def _validate_url_shape(name: str, provider: ProviderConfig) -> None:
    scheme, host = split_url(provider.base_url)
    if scheme not in ("http", "https") or not host or has_credentials(provider.base_url or ""):
        raise ConfigError(f"Provider {name}: base_url is not a usable URL.")
    if scheme != "https" and not is_loopback_host(host):
        raise ConfigError(f"Provider {name}: a remote server must use https.")


def _validate_models(settings: Settings) -> None:
    catalog = settings.models.catalog.get(settings.provider_name)
    if catalog is None:
        raise ConfigError(f"models.yaml has no catalog for {settings.provider_name}.")
    contexts: dict[str, set[int]] = {}
    for tier, spec in catalog.items():
        if tier not in TIER_ORDER:
            raise ConfigError(f"models.yaml names an unknown tier: {tier}.")
        if spec.max_output >= spec.context:
            raise ConfigError(f"Tier {tier}: max_output must be smaller than context.")
        contexts.setdefault(spec.name.casefold(), set()).add(spec.context)
    for name, sizes in contexts.items():
        if len(sizes) > 1:
            logger.warning("tiers share model %s with different context sizes", name)
