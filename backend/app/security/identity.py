"""Tokens and the identity they stand for. Identity comes only from the token."""

from __future__ import annotations

import os
import secrets
from collections.abc import Mapping
from pathlib import Path

from app.config.settings import Settings
from app.models.domain import Identity, Permissions

LOCAL_TOKEN_FILE = "local-token"
MIN_LOCAL_TOKEN_LENGTH = 32


def load_or_create_local_token(data_dir: Path) -> str:
    path = data_dir / LOCAL_TOKEN_FILE
    try:
        existing = path.read_text(encoding="ascii").strip()
    except FileNotFoundError:
        existing = ""
    if len(existing) >= MIN_LOCAL_TOKEN_LENGTH:
        return existing
    token = secrets.token_hex(32)
    path.unlink(missing_ok=True)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="ascii") as handle:
        handle.write(token + "\n")
    return token


def bearer_token(header: str | None) -> str | None:
    if not header:
        return None
    scheme, _, value = header.partition(" ")
    if scheme.lower() != "bearer":
        return None
    return value.strip() or None


def _matches(candidate: str | None, expected: bytes | None) -> bool:
    if candidate is None or not expected:
        return False
    return secrets.compare_digest(candidate.encode("utf-8", "replace"), expected)


class Authenticator:
    def __init__(self, settings: Settings, env: Mapping[str, str]) -> None:
        identity = settings.identity
        if identity.mode == "local":
            token = load_or_create_local_token(settings.data_dir)
        else:
            token = env.get(identity.token_env, "")
        self._token = token.encode("utf-8")
        permissions = identity.user.permissions
        self.identity = Identity(
            user_id=identity.user.user_id,
            display_name=identity.user.display_name,
            groups=tuple(identity.user.groups),
            permissions=Permissions(
                advanced_mode=permissions.advanced_mode,
                manual_model_selection=permissions.manual_model_selection,
                view_diagnostics=permissions.view_diagnostics,
                search_documents=permissions.search_documents,
                allowed_tiers=tuple(permissions.allowed_tiers),
            ),
            auth_mode=identity.mode,
        )
        service = settings.inference_service
        self._service_token = (
            env.get(service.token_env, "").encode("utf-8") if service.enabled else None
        )

    def authenticate(self, header: str | None) -> Identity | None:
        return self.identity if _matches(bearer_token(header), self._token) else None

    def authenticate_service(self, header: str | None) -> bool:
        return _matches(bearer_token(header), self._service_token)
