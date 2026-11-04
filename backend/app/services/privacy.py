"""Privacy statements, computed from running state. Labels and values stay non-technical."""

from __future__ import annotations

import asyncio
import ipaddress
import socket
import sys
from collections.abc import Mapping
from pathlib import Path
from urllib.parse import urlsplit

from app.config.settings import Settings
from app.models.domain import Identity
from app.providers.base import InferenceProvider
from app.security.locality import compute_locality, is_loopback_host
from app.security.paths import folder_keeps_user_only_access

NOTICE = (
    "Local processing alone does not make a system secure or compliant. "
    "This beta has not been approved for production use."
)
_DEFAULT_OLLAMA_PORT = 11434
_MAX_ADDRESSES = 16


def local_ipv4_addresses() -> list[str]:
    """This computer's non-loopback IPv4 addresses. No traffic leaves the machine."""
    found: set[str] = set()
    try:
        for info in socket.getaddrinfo(socket.gethostname(), None, socket.AF_INET):
            found.add(str(info[4][0]))
    except OSError:
        pass
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as probe:
            # Connecting a UDP socket only selects a route. 192.0.2.1 is a reserved test address.
            probe.connect(("192.0.2.1", 9))
            found.add(probe.getsockname()[0])
    except OSError:
        pass
    usable = []
    for address in sorted(found):
        parsed = ipaddress.ip_address(address)
        if not parsed.is_loopback and not parsed.is_unspecified:
            usable.append(address)
    return usable[:_MAX_ADDRESSES]


async def _accepts(address: str, port: int, timeout: float) -> bool:
    try:
        _, writer = await asyncio.wait_for(asyncio.open_connection(address, port), timeout)
    except (OSError, TimeoutError):
        return False
    writer.close()
    return True


async def exposed_addresses(addresses: list[str], port: int, timeout: float) -> list[str]:
    results = await asyncio.gather(*(_accepts(address, port, timeout) for address in addresses))
    return [address for address, accepted in zip(addresses, results, strict=True) if accepted]


def _statement(id_: str, label: str, value: str, status: str, detail: str) -> dict:
    return {"id": id_, "label": label, "value": value, "status": status, "detail": detail}


class PrivacyService:
    def __init__(
        self,
        settings: Settings,
        provider: InferenceProvider,
        *,
        env: Mapping[str, str],
        probe_timeout_s: float,
    ) -> None:
        self._settings = settings
        self._provider = provider
        self._env = env
        self._probe_timeout = probe_timeout_s

    async def build(self, identity: Identity) -> dict:
        gate = await self._provider.cloud_gate()
        statements = [
            self._processing(gate),
            self._external_ai(gate),
            _statement(
                "cloud_fallback",
                "Backup AI services",
                "None. If the AI service is down, requests stop here",
                "ok",
                "One provider is active per gateway, and no code path falls back to another.",
            ),
            self._telemetry(),
            self._storage(),
            _statement(
                "logging",
                "What is recorded",
                "Request times and outcomes, never what you write",
                "ok",
                "The gateway logs method, route template, status, duration and request id. "
                "This statement speaks for the gateway only, not for other software.",
            ),
            await self._network(),
            _statement(
                "encryption_at_rest",
                "Encryption of saved conversations",
                "Not encrypted in this beta",
                "warn",
                f"SQLite files under {self._settings.data_dir} are stored in the clear.",
            ),
            self._authentication(),
        ]
        if not identity.permissions.advanced_mode:
            for statement in statements:
                statement["detail"] = None
        locality = compute_locality(self._provider.kind, self._provider.base_url, gate is True)
        return {
            "locality": locality.wire(),
            "statements": statements,
            "notice": NOTICE,
        }

    def _processing(self, gate: bool | None) -> dict:
        kind = self._provider.kind
        label = "Where the AI runs"
        if kind == "mock":
            return _statement(
                "processing",
                label,
                "Nowhere. This is a demonstration",
                "info",
                "The mock provider produces simulated text and no model runs.",
            )
        url = urlsplit(self._provider.base_url or "")
        detail = f"Inference is handled by {self._provider.display_name} at {url.netloc}."
        if kind == "ollama":
            if gate is True:
                return _statement("processing", label, "On this computer", "ok", detail)
            return _statement("processing", label, "Could not be confirmed", "warn", detail)
        return _statement("processing", label, "On your organization's service", "info", detail)

    def _external_ai(self, gate: bool | None) -> dict:
        label = "Outside AI services"
        kind = self._provider.kind
        if kind == "mock":
            return _statement(
                "external_ai", label, "Not used", "ok", "The mock provider contacts nothing."
            )
        if kind == "inference_api":
            return _statement(
                "external_ai",
                label,
                "Cannot be checked from here",
                "info",
                "What the organization server does is outside what this gateway can establish.",
            )
        if gate is True:
            return _statement(
                "external_ai",
                label,
                "Turned off",
                "ok",
                "Checked live: GET /api/status reports cloud.disabled true.",
            )
        if gate is False:
            detail = "GET /api/status did not report cloud.disabled true. Chat is refused."
        else:
            detail = "Ollama did not answer GET /api/status. Chat is refused."
        return _statement("external_ai", label, "Could not be confirmed as off", "warn", detail)

    def _telemetry(self) -> dict:
        label = "Usage data"
        if self._provider.kind == "ollama":
            return _statement(
                "telemetry",
                label,
                "Digital Commons sends none. Your AI software may check for its own updates",
                "info",
                "Ollama itself may contact ollama.com to check for updates. "
                "No prompt content is involved.",
            )
        return _statement(
            "telemetry",
            label,
            "Digital Commons sends none",
            "ok",
            "The gateway ships no analytics or telemetry exporter.",
        )

    def _storage(self) -> dict:
        days = self._settings.conversations.retention_days
        kept = f"for {days} days" if days else "until you delete them"
        label = "Where conversations are kept"
        stored = "with the text of attached files"
        detail = (
            f"SQLite under {self._settings.data_dir}. Attached file text is stored with its "
            "message, as far as it was read, so later questions in the same chat can use it "
            "while it still fits, and is deleted with it."
        )
        folder = self._settings.data_dir
        if folder_keeps_user_only_access(folder, self._env, sys.platform, Path.home()):
            return _statement(
                "conversation_storage",
                label,
                f"In a private folder on the computer running Digital Commons, {stored}, {kept}",
                "ok",
                detail,
            )
        return _statement(
            "conversation_storage",
            label,
            f"In a folder on the computer running Digital Commons, {stored}, {kept}. "
            "Other accounts on that computer may be able to open it",
            "warn",
            f"{detail} The folder is outside the user profile and %LOCALAPPDATA%, so it "
            "inherits its parent's permissions, which this gateway does not check.",
        )

    def _remote_client_hosts(self) -> list[str]:
        """Allowed Host names that are not this computer, as a proxy in front would use."""
        return [
            host
            for host in self._settings.security.allowed_client_hosts
            if not is_loopback_host(host.strip().strip("[]"))
        ]

    async def _network(self) -> dict:
        label = "Who can connect"
        gateway = self._settings.gateway
        detail = f"The gateway listens on {gateway.host}:{gateway.port}."
        if self._provider.kind == "ollama":
            port = urlsplit(self._provider.base_url or "").port or _DEFAULT_OLLAMA_PORT
            try:
                addresses = await asyncio.to_thread(local_ipv4_addresses)
                exposed = await exposed_addresses(addresses, port, self._probe_timeout)
            except Exception as error:
                return _statement(
                    "network_exposure",
                    label,
                    "Could not be fully checked",
                    "warn",
                    f"{detail} The provider port test failed: {type(error).__name__}.",
                )
            if exposed:
                return _statement(
                    "network_exposure",
                    label,
                    "Your AI software may be open to other computers",
                    "warn",
                    f"{detail} Port {port} accepted a connection on {', '.join(exposed)}.",
                )
            tested = ", ".join(addresses) or "no non-loopback addresses found"
            detail = f"{detail} Port {port} refused or ignored a connection on: {tested}."
        remote = self._remote_client_hosts()
        if remote:
            # The bind is loopback, but a proxy can forward other computers to it.
            return _statement(
                "network_exposure",
                label,
                "Other computers on your organization's network",
                "info",
                f"{detail} security.allowed_client_hosts also admits {', '.join(remote)}, so "
                "requests forwarded under those names, for example by a proxy, can come from "
                "other computers. Each still needs the bearer token.",
            )
        return _statement(
            "network_exposure", label, "Only programs on this computer", "ok", detail
        )

    def _authentication(self) -> dict:
        label = "How this service knows it's you"
        if self._settings.identity.mode == "local":
            return _statement(
                "authentication",
                label,
                "A private key kept on this computer",
                "info",
                "Local mode: a 256-bit bearer token in the data folder.",
            )
        return _statement(
            "authentication",
            label,
            "A key shared by everyone who uses this service",
            "warn",
            "shared_token mode: one token is one identity. Interim only.",
        )
