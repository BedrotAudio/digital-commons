"""The Ollama cloud gate, proxies and redirects."""

from __future__ import annotations

from dataclasses import replace

import contextlib

import httpx
import pytest

from harness import FAST_TUNING, GATE_OK, MODEL, RecordingProxy, chat_chunk, final_chunk, wait_until

SLOW_STATUS = replace(FAST_TUNING, status_interval_s=3600)


def closed(fake) -> None:
    fake.status_body = {"cloud": {"disabled": False, "source": "none"}}


def code_500(fake) -> None:
    fake.status_code = 500


def code_404(fake) -> None:
    fake.status_code = 404


def malformed(fake) -> None:
    fake.status_text = "not json"


def no_cloud_key(fake) -> None:
    fake.status_body = {}


def string_true(fake) -> None:
    fake.status_body = {"cloud": {"disabled": "true"}}


def one_not_true(fake) -> None:
    fake.status_body = {"cloud": {"disabled": 1}}


def list_body(fake) -> None:
    fake.status_body = [GATE_OK]


FAILURES = [closed, code_500, code_404, malformed, no_cloud_key, string_true, one_not_true, list_body]


@pytest.mark.parametrize("failure", FAILURES)
def test_gate_failure_fails_closed(make, fake, failure) -> None:
    failure(fake)
    gateway = make()
    result = gateway.post_chat()
    assert result.status == 503
    assert result.error["code"] == "model_not_permitted"
    assert fake.chats == []
    assert gateway.conversations() == []

    status = wait_until(
        lambda: (view := gateway.status())["reason"] == "cloud_access_enabled" and view
    )
    assert status["state"] == "unavailable"
    assert status["locality"]["kind"] != "this_device"
    assert status["inference"]["cloud_access_disabled"] is False
    bootstrap = gateway.client.get("/v1/bootstrap").json()
    assert bootstrap["locality"]["kind"] != "this_device"


def test_gate_passes_and_locality_is_this_device(make, fake) -> None:
    gateway = make()
    result = gateway.post_chat()
    assert result.text == "Hello there."
    assert result.data("start")["route"]["locality"] == "this_device"
    wait_until(lambda: gateway.status()["locality"]["kind"] == "this_device")


def test_cloud_setting_change_is_reflected_without_restart(make, fake) -> None:
    gateway = make()
    wait_until(lambda: gateway.status()["state"] == "ready")
    closed(fake)
    view = wait_until(lambda: (body := gateway.status())["state"] == "unavailable" and body)
    assert view["locality"]["kind"] == "organization"
    fake.status_body = GATE_OK
    view = wait_until(lambda: (body := gateway.status())["state"] == "ready" and body)
    assert view["locality"]["kind"] == "this_device"


def test_gate_runs_again_immediately_before_chat(make, fake) -> None:
    gateway = make(tuning=SLOW_STATUS)
    wait_until(lambda: gateway.services.monitor.health is not None)
    fake.status_script.extend([(200, GATE_OK), (200, {"cloud": {"disabled": False}})])
    result = gateway.post_chat()
    assert result.status == 200
    assert result.names()[0] == "start"
    assert result.data("error")["code"] == "model_not_permitted"
    assert fake.chats == []


def test_gate_is_asked_for_every_turn(make, fake) -> None:
    gateway = make(tuning=SLOW_STATUS)
    wait_until(lambda: gateway.services.monitor.health is not None)
    fake.requests.clear()
    conversation = gateway.post_chat().conversation_id
    first = len(fake.calls("/api/status"))
    gateway.post_chat(conversation_id=conversation)
    assert first >= 1
    assert len(fake.calls("/api/status")) == first * 2


def cloud_named_catalog() -> dict:
    spec = {"name": "llama3:cloud", "context": 8192, "max_output": 1024, "temperature": 0.1, "think": False}
    return {"models": {"catalog": {"ollama": {"fast": spec, "standard": spec, "advanced": spec}}}}


def test_names_are_not_a_control_a_cloud_looking_name_passes_with_the_gate(make, fake) -> None:
    fake.tags = [{"name": "llama3:cloud"}]
    fake.ps = [fake.loaded(name="llama3:cloud")]
    gateway = make(overrides=cloud_named_catalog())
    assert gateway.post_chat().text == "Hello there."


def test_names_are_not_a_control_an_innocent_name_stops_on_the_gate(make, fake) -> None:
    closed(fake)
    gateway = make()
    assert MODEL.endswith("12b")
    assert gateway.post_chat().error["code"] == "model_not_permitted"


def test_no_name_matching_in_the_source() -> None:
    from pathlib import Path

    import app.providers.ollama as adapter

    source = Path(adapter.__file__).read_text(encoding="utf-8")
    assert ":cloud" not in source and "-cloud" not in source


@pytest.mark.parametrize(
    "show",
    [
        {"remote_host": "https://ollama.com", "remote_model": "x"},
        {"remote_host": "https://ollama.com"},
        {"remote_model": "x"},
    ],
)
def test_remote_model_on_show_is_rejected(make, fake, show) -> None:
    fake.show_body = show
    gateway = make()
    result = gateway.post_chat()
    assert result.status == 503
    assert result.error["code"] == "model_not_permitted"
    assert fake.chats == []
    view = wait_until(lambda: (body := gateway.status())["state"] == "unavailable" and body)
    assert view["reason"] == "cloud_access_enabled"


def test_empty_remote_fields_on_show_are_fine(make, fake) -> None:
    fake.show_body = {"remote_host": "", "remote_model": None}
    assert make().post_chat().text == "Hello there."


def test_remote_stub_in_tags_is_rejected(make, fake) -> None:
    fake.tags = [{"name": MODEL, "remote_model": "x", "remote_host": "https://ollama.com"}]
    gateway = make()
    assert gateway.post_chat().error["code"] == "model_not_permitted"


@pytest.mark.parametrize("field", ["remote_model", "remote_host"])
def test_remote_field_in_a_stream_chunk_aborts(make, fake, field) -> None:
    fake.chunks = [chat_chunk("Hel"), {**chat_chunk("lo"), field: "x"}, final_chunk()]
    gateway = make()
    result = gateway.post_chat()
    assert result.data("error")["code"] == "model_not_permitted"
    assert result.text == "Hel"
    assert "done" not in result.names()
    row = gateway.conversation(result.conversation_id)["messages"][-1]
    assert (row["content"], row["finish_reason"]) == ("Hel", "error")


def test_proxy_variables_are_ignored(make, fake, monkeypatch) -> None:
    proxy = RecordingProxy()
    try:
        for name in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"):
            monkeypatch.setenv(name, proxy.url)
        monkeypatch.delenv("NO_PROXY", raising=False)
        monkeypatch.delenv("no_proxy", raising=False)

        with httpx.Client(trust_env=True) as control, contextlib.suppress(httpx.HTTPError):
            control.get(f"{fake.url}/api/status")
        assert proxy.hits >= 1, "the proxy must be able to detect a client that trusts env"
        hits = proxy.hits

        gateway = make()
        result = gateway.post_chat()
        assert result.text == "Hello there."
        assert len(fake.chats) == 1
        assert proxy.hits == hits
    finally:
        proxy.stop()


def test_upstream_client_factory_is_the_only_constructor() -> None:
    from pathlib import Path

    import app

    package = Path(app.__file__).parent
    users = [
        path.name
        for path in package.rglob("*.py")
        if "httpx.AsyncClient(" in path.read_text(encoding="utf-8")
    ]
    assert users == ["client.py"]
    source = (package / "providers" / "client.py").read_text(encoding="utf-8")
    assert "trust_env=False" in source
    assert "follow_redirects=False" in source
    assert "verify=False" not in source


def test_redirect_on_chat_is_provider_error(make, fake) -> None:
    fake.redirect_paths = {"/api/chat"}
    gateway = make()
    result = gateway.post_chat()
    assert result.data("error")["code"] == "provider_error"
    assert result.data("error")["retryable"] is True
    assert fake.elsewhere_hits == 0


def test_redirect_on_gate_is_provider_error_and_fails_closed(make, fake) -> None:
    fake.redirect_paths = {"/api/status"}
    gateway = make()
    result = gateway.post_chat()
    assert result.status == 502
    assert result.error["code"] == "provider_error"
    assert fake.elsewhere_hits == 0
    view = wait_until(lambda: (body := gateway.status())["state"] == "unavailable" and body)
    assert view["locality"]["kind"] == "organization"


@pytest.mark.parametrize("path", ["/api/tags", "/api/show"])
def test_redirect_on_probes_is_provider_error(make, fake, path) -> None:
    fake.redirect_paths = {path}
    gateway = make()
    result = gateway.post_chat()
    assert result.status == 502
    assert result.error["code"] == "provider_error"
    assert fake.elsewhere_hits == 0
