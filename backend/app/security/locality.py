"""Host parsing and the one function that decides where processing happens."""

from __future__ import annotations

import ipaddress
from urllib.parse import urlsplit

from app.models.domain import Locality


class BindRefused(Exception):
    pass


def is_loopback_host(host: str | None) -> bool:
    """Parse, never prefix-match. 127.1, 2130706433 and 0.0.0.0 are all refused."""
    if not host:
        return False
    if host.lower() == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def split_url(url: str | None) -> tuple[str, str | None]:
    """Return (scheme, hostname), or ("", None) for anything unparseable."""
    if not url:
        return "", None
    try:
        parts = urlsplit(url)
        return parts.scheme.lower(), parts.hostname
    except ValueError:
        return "", None


def has_credentials(url: str) -> bool:
    try:
        parts = urlsplit(url)
    except ValueError:
        return True
    return parts.username is not None or parts.password is not None


def is_loopback_url(url: str | None) -> bool:
    scheme, host = split_url(url)
    return scheme in ("http", "https") and not has_credentials(url or "") and is_loopback_host(host)


def enforce_loopback_bind(host: str) -> None:
    if not is_loopback_host(host):
        raise BindRefused(
            f"Refusing to bind to {host!r}. Beta 1 listens on a loopback address only."
        )


_THIS_DEVICE = Locality(
    "this_device",
    "On this computer",
    "Your conversations are processed on this computer and are not sent to any outside AI service.",
)
_ORGANIZATION = Locality(
    "organization",
    "Organization server",
    "Your conversations are processed by a server your organization runs.",
)
_UNCONFIRMED = Locality(
    "organization",
    "Not confirmed",
    "Digital Commons could not confirm that outside AI access is off, so it does not "
    "treat this computer as a private place to process conversations.",
)
_DEMO = Locality(
    "demo",
    "Demonstration",
    "This is a demonstration. Replies are simulated and no AI model is used.",
)


def compute_locality(kind: str, base_url: str | None, cloud_gate_passed: bool) -> Locality:
    if kind == "mock":
        return _DEMO
    if kind == "ollama":
        if is_loopback_url(base_url) and cloud_gate_passed:
            return _THIS_DEVICE
        return _UNCONFIRMED
    return _ORGANIZATION
