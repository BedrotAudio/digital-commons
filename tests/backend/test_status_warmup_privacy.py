"""Status states and reasons, warm-up, and privacy statements."""

from __future__ import annotations

import re
import socket
import threading
import time
from dataclasses import replace

import pytest

from app.services import privacy
from harness import FAST_TUNING, MODEL, wait_until


def state_of(gateway, state: str) -> dict:
    return wait_until(lambda: (view := gateway.status())["state"] == state and view)


def test_ready(make) -> None:
    gateway = make()
    view = state_of(gateway, "ready")
    assert view["reason"] is None
    assert view["summary"] == "Ready"
    assert view["warmup"] == {"state": "idle", "message": None}
    assert set(view["gateway"]) == {"version", "instance_id", "uptime_s", "address"}
    assert view["gateway"]["address"] == "127.0.0.1:8000"
    inference = view["inference"]
    assert inference["provider"] == "ollama"
    assert inference["kind"] == "ollama"
    assert inference["reachable"] is True
    assert inference["version"] == "0.40.13"
    assert inference["cloud_access_disabled"] is True
    assert inference["missing_models"] == []
    assert inference["warmup_model"] == MODEL
    assert inference["loaded_models"][0] == {
        "name": MODEL,
        "size_bytes": 6401126562,
        "gpu_bytes": 0,
        "context_tokens": 8192,
        "expires_at": "2026-11-01T21:15:13Z",
    }


def test_demo(make) -> None:
    gateway = make(provider="mock")
    view = state_of(gateway, "demo")
    assert view["reason"] is None
    assert view["locality"]["kind"] == "demo"
    assert view["inference"]["kind"] == "mock"
    assert view["inference"]["loaded_models"] == []


def test_unreachable(make) -> None:
    gateway = make(fake_url="http://127.0.0.1:9")
    view = state_of(gateway, "unavailable")
    assert view["reason"] == "provider_unreachable"
    assert view["inference"]["reachable"] is False
    assert view["inference"]["cloud_access_disabled"] is None
    assert view["locality"]["kind"] == "organization"


def test_model_missing_is_unavailable_with_an_install_hint(make, fake) -> None:
    fake.tags = []
    gateway = make()
    view = state_of(gateway, "unavailable")
    assert view["reason"] == "model_missing"
    missing = view["inference"]["missing_models"]
    assert {item["tier"] for item in missing} == {"fast", "standard", "advanced"}
    assert missing[0]["install_hint"] == f"ollama pull {MODEL}"


def test_some_models_missing_is_degraded(make, fake) -> None:
    def spec(name: str) -> dict:
        return {"name": name, "context": 8192, "max_output": 1024, "temperature": 0.1, "think": False}

    fake.tags = [{"name": "small:1"}]
    overrides = {
        "models": {
            "catalog": {
                "ollama": {
                    "fast": spec("small:1"),
                    "standard": spec("middle:1"),
                    "advanced": spec("large:1"),
                }
            }
        }
    }
    gateway = make(overrides=overrides)
    view = state_of(gateway, "degraded")
    assert view["reason"] == "model_missing"
    assert {item["tier"] for item in view["inference"]["missing_models"]} == {
        "standard",
        "advanced",
    }


def test_inference_block_needs_diagnostics(make) -> None:
    gateway = make(
        overrides={
            "identity": {"user": {"permissions": {"view_diagnostics": False}}},
        }
    )
    view = state_of(gateway, "ready")
    assert view["inference"] is None
    assert view["locality"]["kind"] == "this_device"


def test_view_is_cached_between_refreshes(make, fake) -> None:
    gateway = make(tuning=replace(FAST_TUNING, status_interval_s=3600))
    state_of(gateway, "ready")
    fake.requests.clear()
    for _ in range(5):
        gateway.status()
    assert fake.requests == []


def test_instance_id_changes_per_start(make) -> None:
    assert (
        make(provider="mock").status()["gateway"]["instance_id"]
        != make(provider="mock").status()["gateway"]["instance_id"]
    )


WARM = {"inference": {"warm_on_start": True}}


def test_warmup_request_shape_and_ready(make, fake) -> None:
    gateway = make(overrides=WARM)
    view = wait_until(lambda: (body := gateway.status())["warmup"]["state"] == "ready" and body)
    assert view["state"] == "ready"
    body = fake.warmups[0]
    assert body["messages"] == []
    assert body["options"] == {"num_ctx": 8192}
    assert body["keep_alive"] == "30m"
    assert body["model"] == MODEL
    assert fake.calls("/api/show")


def test_warmup_does_not_block_startup_and_reports_starting(make, fake) -> None:
    # Far longer than any startup, so this fails only if startup really waits for it.
    fake.warm_delay = 4.0
    began = time.monotonic()
    gateway = make(overrides=WARM)
    assert time.monotonic() - began < fake.warm_delay
    assert gateway.client.get("/healthz").status_code == 200
    view = state_of(gateway, "starting")
    assert view["warmup"]["state"] == "loading"
    assert view["summary"].startswith("Getting ready")
    wait_until(lambda: gateway.status()["state"] == "ready", timeout=8)


def test_warmup_holds_the_inference_slot(make, fake) -> None:
    fake.warm_delay = 1.5
    gateway = make(overrides=WARM)
    wait_until(lambda: fake.warmups)
    assert gateway.services.slots.running == 1
    results = []
    thread = threading.Thread(target=lambda: results.append(gateway.post_chat()))
    thread.start()
    time.sleep(0.2)
    assert fake.chats == []
    thread.join()
    phases = [item["phase"] for item in results[0].all("status")]
    assert phases[0] == "queued"
    assert results[0].all("status")[0]["message"].startswith("Getting ready")
    assert len(fake.chats) == 1


def test_warmup_retries_when_the_provider_becomes_reachable(make, fake) -> None:
    fake.status_code = 200
    probe = socket.socket()
    probe.bind(("127.0.0.1", 0))
    port = probe.getsockname()[1]
    probe.close()

    from harness import RunningServer

    gateway = make(overrides=WARM, fake_url=f"http://127.0.0.1:{port}")
    view = state_of(gateway, "unavailable")
    assert view["reason"] == "provider_unreachable"
    assert view["warmup"]["state"] == "idle"

    late = RunningServer(fake.app, port=port)
    try:
        view = wait_until(lambda: (body := gateway.status())["warmup"]["state"] == "ready" and body)
        assert view["state"] == "ready"
        assert len(fake.warmups) == 1
    finally:
        late.stop()


def test_warmup_failure_is_degraded(make, fake) -> None:
    fake.warm_status = 500
    gateway = make(overrides=WARM)
    view = state_of(gateway, "degraded")
    assert view["reason"] == "model_load_failed"
    assert view["warmup"]["state"] == "failed"
    assert view["warmup"]["message"]
    assert gateway.post_chat().text == "Hello there."


def test_warmup_never_runs_with_the_gate_closed(make, fake) -> None:
    fake.status_body = {"cloud": {"disabled": False}}
    gateway = make(overrides=WARM)
    state_of(gateway, "unavailable")
    time.sleep(0.1)
    assert fake.warmups == []


def test_warmup_is_skipped_for_the_mock(make) -> None:
    gateway = make(provider="mock", overrides=WARM)
    assert gateway.status()["warmup"] == {"state": "idle", "message": None}


def test_warmup_refuses_a_remote_model_and_does_not_load_it(make, fake) -> None:
    fake.show_body = {"remote_host": "https://ollama.com"}
    gateway = make(overrides=WARM)
    state_of(gateway, "unavailable")
    time.sleep(0.1)
    assert fake.warmups == []


def statements(gateway) -> dict[str, dict]:
    body = gateway.client.get("/v1/privacy").json()
    return {item["id"]: item for item in body["statements"]}


def test_privacy_lists_every_statement(make) -> None:
    gateway = make()
    body = gateway.client.get("/v1/privacy").json()
    assert [item["id"] for item in body["statements"]] == [
        "processing",
        "external_ai",
        "cloud_fallback",
        "telemetry",
        "conversation_storage",
        "logging",
        "network_exposure",
        "encryption_at_rest",
        "authentication",
    ]
    assert body["locality"]["kind"] == "this_device"
    assert body["notice"].startswith("Local processing alone")
    for item in body["statements"]:
        assert set(item) == {"id", "label", "value", "status", "detail"}
        assert item["status"] in ("ok", "info", "warn")


@pytest.mark.parametrize("provider", ["ollama", "mock"])
def test_labels_and_values_are_free_of_technical_words(make, provider) -> None:
    gateway = make(provider=provider)
    technical = re.compile(
        r"ollama|sqlite|tcp|http|api|port|token|127\.0\.0\.1|localhost|gateway|inference|wal",
        re.IGNORECASE,
    )
    for item in statements(gateway).values():
        assert not technical.search(item["label"]), item["label"]
        assert not technical.search(item["value"]), item["value"]


def test_processing_names_the_real_host(make, fake) -> None:
    item = statements(make())["processing"]
    assert item["status"] == "ok"
    assert item["value"] == "On this computer"
    assert fake.url.removeprefix("http://") in item["detail"]


def test_gate_failure_is_a_plain_warning_never_omitted(make, fake) -> None:
    fake.status_body = {"cloud": {"disabled": False}}
    gateway = make()
    items = statements(gateway)
    assert items["external_ai"]["status"] == "warn"
    assert items["processing"]["status"] == "warn"
    assert gateway.client.get("/v1/privacy").json()["locality"]["kind"] == "organization"


def test_unreachable_provider_is_a_warning(make) -> None:
    items = statements(make(fake_url="http://127.0.0.1:9"))
    assert items["external_ai"]["status"] == "warn"


def test_external_ai_is_read_live(make, fake) -> None:
    gateway = make(tuning=replace(FAST_TUNING, status_interval_s=3600))
    assert statements(gateway)["external_ai"]["status"] == "ok"
    fake.status_body = {"cloud": {"disabled": False}}
    assert statements(gateway)["external_ai"]["status"] == "warn"


def test_telemetry_note_is_info_for_ollama_only(make) -> None:
    item = statements(make())["telemetry"]
    assert item["status"] == "info"
    assert "ollama.com" in item["detail"]
    assert statements(make(provider="mock"))["telemetry"]["status"] == "ok"


def test_storage_statement_reflects_retention(make) -> None:
    assert "30 days" in statements(make())["conversation_storage"]["value"]
    forever = make(overrides={"conversations": {"retention_days": 0}})
    assert "until you delete" in statements(forever)["conversation_storage"]["value"]
    assert "deleted with it" in statements(forever)["conversation_storage"]["detail"]


def test_encryption_is_always_a_warning(make) -> None:
    assert statements(make())["encryption_at_rest"]["status"] == "warn"
    assert statements(make(provider="mock"))["encryption_at_rest"]["status"] == "warn"


def test_authentication_statement(make) -> None:
    assert statements(make())["authentication"]["status"] == "info"
    token = "s" * 40
    shared = make(
        overrides={"identity": {"mode": "shared_token"}},
        env={"DC_GATEWAY_TOKEN": token},
        token=token,
    )
    assert statements(shared)["authentication"]["status"] == "warn"


def test_detail_needs_advanced_mode_permission(make) -> None:
    gateway = make(
        overrides={
            "identity": {
                "user": {
                    "permissions": {
                        "advanced_mode": False,
                        "manual_model_selection": False,
                        "view_diagnostics": False,
                    }
                }
            }
        }
    )
    assert all(item["detail"] is None for item in statements(gateway).values())


def listener(address: str) -> tuple[socket.socket, int]:
    server = socket.socket()
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((address, 0))
    server.listen(4)
    return server, server.getsockname()[1]


def test_port_test_finds_an_exposed_provider(make, fake, monkeypatch) -> None:
    server, port = listener("127.0.0.1")
    try:
        monkeypatch.setattr(privacy, "local_ipv4_addresses", lambda: ["127.0.0.1"])
        gateway = make(fake_url=fake.url)
        monkeypatch.setattr(
            privacy, "urlsplit", _with_port(port, privacy.urlsplit), raising=True
        )
        item = statements(gateway)["network_exposure"]
    finally:
        server.close()
    assert item["status"] == "warn"
    assert str(port) in item["detail"]


def _with_port(port: int, original):
    class Parts:
        def __init__(self, url: str) -> None:
            self.port = port
            self.netloc = original(url).netloc

    return Parts


def test_port_test_reports_a_closed_port_as_ok(make, monkeypatch) -> None:
    monkeypatch.setattr(privacy, "local_ipv4_addresses", lambda: ["127.0.0.1"])
    gateway = make(fake_url="http://127.0.0.1:9")
    item = statements(gateway)["network_exposure"]
    assert item["status"] == "ok"
    assert item["value"] == "Only programs on this computer"


def test_port_test_failure_is_a_warning(make, monkeypatch) -> None:
    def broken() -> list[str]:
        raise RuntimeError("no interfaces")

    monkeypatch.setattr(privacy, "local_ipv4_addresses", broken)
    item = statements(make())["network_exposure"]
    assert item["status"] == "warn"
    assert item["value"] == "Could not be fully checked"


def test_port_test_is_bounded_by_its_timeout(make, monkeypatch) -> None:
    monkeypatch.setattr(privacy, "local_ipv4_addresses", lambda: ["192.0.2.1", "192.0.2.2"])
    gateway = make()
    began = time.monotonic()
    statements(gateway)
    assert time.monotonic() - began < 1.5


def test_address_list_excludes_loopback() -> None:
    assert not [item for item in privacy.local_ipv4_addresses() if item.startswith("127.")]


def test_mock_network_statement_has_no_port_test(make, monkeypatch) -> None:
    calls = []
    monkeypatch.setattr(privacy, "local_ipv4_addresses", lambda: calls.append(1) or [])
    statements(make(provider="mock"))
    assert calls == []


def test_a_reply_that_loads_the_model_ends_the_warm_up(make, fake) -> None:
    """A first message can win the slot before warm-up starts. Status must not say
    the model is still loading once that reply has produced text."""
    import asyncio

    from app.models.domain import TierSpec
    from app.providers.base import ProviderHealth
    from app.services.slots import InferenceSlots
    from app.services.warmup import WarmupService

    spec = TierSpec("standard", "Standard", "", True, MODEL, 8192, 2048, 0.4, False)

    class Registry:
        def warmup_spec(self):
            return spec

        def is_usable(self, spec, health):
            return True

    class Monitor:
        health = ProviderHealth(reachable=True, cloud_gate=True, installed={MODEL})

    class Provider:
        supports_warmup = True
        warmed = 0

        async def warm(self, spec):
            self.warmed += 1

    async def scenario():
        slots = InferenceSlots(1, 8)
        provider = Provider()
        warmup = WarmupService(provider, Registry(), slots, Monitor(), enabled=True, poll_s=0.01)
        reply = slots.reserve()
        await slots.acquire(reply)
        warmup.start()
        await asyncio.sleep(0.05)
        assert warmup.state == "loading"
        warmup.note_model_loaded(MODEL)
        assert warmup.state == "ready"
        slots.release(reply)
        await asyncio.sleep(0.05)
        await warmup.stop()
        return provider.warmed, warmup.state, slots.outstanding

    warmed, state, outstanding = asyncio.run(scenario())
    assert (warmed, state, outstanding) == (0, "ready", 0)


def test_a_reply_from_another_model_does_not_end_the_warm_up() -> None:
    import asyncio

    from app.models.domain import TierSpec
    from app.services.warmup import WarmupService

    spec = TierSpec("standard", "Standard", "", True, MODEL, 8192, 2048, 0.4, False)

    class Registry:
        def warmup_spec(self):
            return spec

    class Provider:
        supports_warmup = True

    warmup = WarmupService(Provider(), Registry(), None, None, enabled=True, poll_s=0.01)
    warmup.state = "loading"
    warmup.note_model_loaded("another-model:1b")
    assert warmup.state == "loading"
    asyncio.run(asyncio.sleep(0))
