"""The inference_api adapter against a real second gateway, and the host contract."""

from __future__ import annotations

import asyncio
import json
import time
from dataclasses import replace

import httpx
import pytest
from starlette.applications import Starlette
from starlette.responses import JSONResponse, RedirectResponse, StreamingResponse
from starlette.routing import Route

from app.config.settings import ConfigError
from harness import FAST_TUNING, Gateway, RunningServer, parse_sse, wait_until

SERVICE_TOKEN = "service-token-" + "x" * 32


def host(make, rate: float = 2000) -> Gateway:
    return make(
        provider="mock",
        overrides={
            "inference_service": {"enabled": True},
            "providers": {"mock": {"tokens_per_second": rate}},
        },
        env={"DC_SERVICE_TOKEN": SERVICE_TOKEN},
    )


def relay(make, url: str, token: str = SERVICE_TOKEN, **provider: object) -> Gateway:
    return make(
        provider="org_server",
        overrides={"providers": {"org_server": {"base_url": url, **provider}}},
        env={"DC_ORG_SERVER_TOKEN": token},
    )


def inference(gateway: Gateway, body: dict, token: str = SERVICE_TOKEN) -> httpx.Response:
    return httpx.post(
        f"{gateway.url}/v1/inference/stream",
        json=body,
        headers={"Authorization": f"Bearer {token}"},
        trust_env=False,
        timeout=30,
    )


MESSAGES = [{"role": "system", "content": "s"}, {"role": "user", "content": "hello"}]


def test_relay_streams_through_a_real_second_gateway(make) -> None:
    server = host(make)
    local = relay(make, server.url)
    result = local.post_chat(message="hello through the relay", tier="standard")
    start = result.data("start")
    assert start["route"]["locality"] == "organization"
    assert start["route"]["provider"] == "org_server"
    assert start["route"]["model"] == "organization-standard"
    assert start["context"]["budget_tokens"] == 32768 - 4096
    assert "demonstration reply" in result.text
    assert "hello through the relay" in result.text
    assert result.data("done")["finish_reason"] == "stop"
    assert result.data("done")["usage"]["estimated"] is True

    row = local.conversation(result.conversation_id)["messages"][-1]
    assert row["route"]["model"] == "demo-standard"
    assert server.conversations() == [], "the model host must store nothing"


def test_status_reports_organization(make) -> None:
    local = relay(make, host(make).url)
    view = wait_until(lambda: (body := local.status())["state"] == "ready" and body)
    assert view["locality"]["kind"] == "organization"
    assert view["locality"]["label"] == "Organization server"
    assert view["inference"]["kind"] == "inference_api"
    assert view["inference"]["cloud_access_disabled"] is None
    assert view["inference"]["missing_models"] == []


def test_privacy_shows_the_real_upstream_host(make) -> None:
    server = host(make)
    local = relay(make, server.url)
    items = {item["id"]: item for item in local.client.get("/v1/privacy").json()["statements"]}
    assert server.url.removeprefix("http://") in items["processing"]["detail"]
    assert items["processing"]["value"] == "On your organization's service"
    assert items["external_ai"]["status"] == "info"


def test_relay_carries_thinking_for_the_advanced_tier(make) -> None:
    result = relay(make, host(make).url).post_chat(tier="advanced")
    assert result.all("thinking")
    assert result.names().index("thinking") < result.names().index("delta")


def test_relay_passes_history_and_regenerate(make) -> None:
    local = relay(make, host(make).url)
    first = local.post_chat(message="first question")
    conversation_id = first.conversation_id
    second = local.post_chat(message="second question", conversation_id=conversation_id)
    assert "second question" in second.text
    again = local.post_chat(conversation_id=conversation_id, regenerate=True)
    assert "second question" in again.text
    messages = local.conversation(conversation_id)["messages"]
    assert [row["role"] for row in messages] == ["user", "assistant", "user", "assistant"]
    assert messages[-1]["id"] == again.data("done")["message_id"]


def test_stop_frees_both_sides(make) -> None:
    server = host(make, rate=30)
    local = relay(make, server.url)
    with local.client.stream("POST", "/v1/chat/stream", json={"message": "hi"}) as response:
        seen = ""
        for chunk in response.iter_text():
            seen += chunk
            if seen.count("event: delta") >= 3:
                break
    conversation_id = seen.split('"conversation_id":"')[1].split('"')[0]
    began = time.monotonic()
    wait_until(lambda: local.services.slots.outstanding == 0, timeout=1.0)
    wait_until(lambda: server.services.slots.outstanding == 0, timeout=1.0)
    assert time.monotonic() - began < 1.0
    wait_until(
        lambda: local.conversation(conversation_id)["messages"][-1]["role"] == "assistant",
        timeout=1.0,
    )
    row = local.conversation(conversation_id)["messages"][-1]
    assert row["finish_reason"] == "cancelled"
    assert row["content"].startswith("**This")
    assert row["usage"]["estimated"] is True

    again = local.post_chat(conversation_id=conversation_id, regenerate=True)
    assert again.data("done")["finish_reason"] == "stop"
    final = local.conversation(conversation_id)["messages"]
    assert len(final) == 2 and final[-1]["finish_reason"] == "stop"


def test_relay_context_overflow_from_the_host_retries_then_fails(make) -> None:
    server = host(make)
    local = relay(make, server.url)
    result = local.post_chat(message="x" * 14000, tier="fast")
    assert result.data("done")["finish_reason"] == "stop"
    long = "word " * 3000
    attachments = [{"name": f"{index}.txt", "text": long * 4} for index in range(4)]
    result = local.post_chat(message="y" * 14000, attachments=attachments, tier="fast")
    assert result.data("error")["code"] == "context_too_large"


def test_relay_unreachable_host(make) -> None:
    local = relay(make, "http://127.0.0.1:9")
    result = local.post_chat()
    assert result.status == 503
    assert result.error["code"] == "provider_unavailable"
    assert local.conversations() == []
    view = wait_until(lambda: (body := local.status())["state"] == "unavailable" and body)
    assert view["reason"] == "provider_unreachable"


def test_wrong_service_token_is_a_provider_error(make) -> None:
    server = host(make)
    local = relay(make, server.url, token="wrong")
    result = local.post_chat()
    assert result.data("error")["code"] == "provider_error"


def test_host_requires_the_service_token(make) -> None:
    server = host(make)
    assert inference(server, {"tier": "fast", "messages": MESSAGES}, token="nope").status_code == 401
    # The gateway's own token opens the terminal API, not the model host contract.
    assert (
        inference(server, {"tier": "fast", "messages": MESSAGES}, token=server.token).status_code
        == 401
    )


def test_host_endpoint_is_absent_unless_enabled(make) -> None:
    gateway = make(provider="mock")
    response = gateway.client.post(
        "/v1/inference/stream", json={"tier": "fast", "messages": MESSAGES}
    )
    assert response.status_code == 404


def test_host_stream_has_only_contract_events_in_order(make) -> None:
    response = inference(host(make), {"tier": "advanced", "messages": MESSAGES})
    assert response.status_code == 200
    names = [name for name, _ in parse_sse(response.text)]
    assert set(names) <= {"status", "thinking", "delta", "done", "error"}
    assert names[-1] == "done"
    assert names.count("done") == 1
    done = parse_sse(response.text)[-1][1]
    assert done["model"] == "demo-advanced"
    assert done["finish_reason"] == "stop"
    assert "usage" in done


def test_host_enforces_its_own_limits(make) -> None:
    server = host(make)
    body = {"tier": "fast", "messages": [{"role": "user", "content": "x" * 40000}]}
    response = inference(server, body)
    assert response.status_code == 422
    assert response.json()["error"]["code"] == "context_too_large"


def test_host_validates_requests(make) -> None:
    server = host(make)
    assert inference(server, {"tier": "fast", "messages": []}).status_code == 422
    assert inference(server, {"tier": "nope", "messages": MESSAGES}).status_code == 422
    assert inference(server, {"tier": "specialist", "messages": MESSAGES}).status_code == 503


def test_host_sends_pings(make) -> None:
    server = make(
        provider="mock",
        overrides={
            "inference_service": {"enabled": True},
            "providers": {"mock": {"tokens_per_second": 20}},
        },
        env={"DC_SERVICE_TOKEN": SERVICE_TOKEN},
        tuning=replace(FAST_TUNING, ping_interval_s=0.02),
    )
    response = inference(server, {"tier": "fast", "messages": MESSAGES})
    assert response.status_code == 200
    assert ": ping\n\n" in response.text


def test_host_queue_is_bounded(make) -> None:
    server = make(
        provider="mock",
        overrides={
            "inference_service": {"enabled": True},
            "inference": {"max_queued_requests": 0},
            "providers": {"mock": {"tokens_per_second": 10}},
        },
        env={"DC_SERVICE_TOKEN": SERVICE_TOKEN},
    )
    local = relay(make, server.url)
    with local.client.stream("POST", "/v1/chat/stream", json={"message": "hi"}) as response:
        chunks = response.iter_text()
        next(chunks)
        wait_until(lambda: server.services.slots.running == 1)
        busy = inference(server, {"tier": "fast", "messages": MESSAGES})
    assert busy.status_code == 429
    assert busy.json()["error"]["code"] == "busy"


class FakeHost:
    """A model host that misbehaves on request."""

    def __init__(self) -> None:
        self.events = ""
        self.status = 200
        self.body: dict = {}
        self.redirect = False
        self.health_status = 200
        self.requests: list[dict] = []

        async def stream(request):
            self.requests.append({"body": await request.json(), "headers": dict(request.headers)})
            if self.redirect:
                return RedirectResponse(f"{self.url}/elsewhere", status_code=302)
            if self.status != 200:
                return JSONResponse(self.body, status_code=self.status)

            async def generate():
                yield self.events
                await asyncio.sleep(0)

            return StreamingResponse(generate(), media_type="text/event-stream")

        async def health(request):
            return JSONResponse({"status": "ok"}, status_code=self.health_status)

        self.server = RunningServer(
            Starlette(
                routes=[
                    Route("/v1/inference/stream", stream, methods=["POST"]),
                    Route("/healthz", health),
                ]
            )
        )
        self.url = self.server.url


@pytest.fixture
def upstream():
    server = FakeHost()
    yield server
    server.server.stop()


def sse(event: str, data: dict) -> str:
    return f"event: {event}\ndata: {json.dumps(data)}\n\n"


DONE = sse("done", {"finish_reason": "stop", "model": "m", "usage": {"prompt_tokens": 5, "completion_tokens": 2}})


def test_request_is_stateless_and_carries_the_token(make, upstream) -> None:
    upstream.events = sse("delta", {"text": "ok"}) + DONE
    local = relay(make, upstream.url, token="the-token")
    result = local.post_chat(tier="fast")
    assert result.text == "ok"
    seen = upstream.requests[0]
    assert set(seen["body"]) == {"tier", "messages"}
    assert seen["body"]["tier"] == "fast"
    assert seen["headers"]["authorization"] == "Bearer the-token"
    assert "origin" not in seen["headers"]
    usage = result.data("done")["usage"]
    assert (usage["prompt_tokens"], usage["estimated"]) == (5, False)


def test_unknown_events_are_ignored_and_no_remote_locality_passes(make, upstream) -> None:
    upstream.events = (
        ": ping\n\n"
        + sse("start", {"route": {"locality": "this_device"}, "locality": "on-the-moon"})
        + sse("citations", {"citations": [{"number": 1, "title": "remote"}]})
        + sse("mystery", {"text": "ignored"})
        + sse("status", {"phase": "reading", "message": "On the moon", "locality": "on-the-moon"})
        + sse("status", {"phase": "brand-new", "message": "On the moon"})
        + sse("delta", {"text": "ok", "locality": "on-the-moon"})
        + sse("done", {"finish_reason": "stop", "model": "m", "locality": "on-the-moon", "usage": {}})
    )
    local = relay(make, upstream.url)
    result = local.post_chat()
    assert "on-the-moon" not in result.raw
    assert "On the moon" not in result.raw
    assert "citations" not in result.names()
    assert result.data("start")["route"]["locality"] == "organization"
    assert "ignored" not in result.text


@pytest.mark.parametrize("status", [301, 302, 303, 307, 308])
def test_every_3xx_is_provider_error(make, upstream, status) -> None:
    upstream.status = status
    upstream.body = {"error": {"code": "busy"}}
    result = relay(make, upstream.url).post_chat()
    assert result.data("error")["code"] == "provider_error"


def test_redirect_is_not_followed(make, upstream) -> None:
    upstream.redirect = True
    result = relay(make, upstream.url).post_chat()
    assert result.data("error")["code"] == "provider_error"
    assert len(upstream.requests) == 1


def test_redirect_on_healthz_is_provider_error(make, upstream) -> None:
    upstream.health_status = 302
    result = relay(make, upstream.url).post_chat()
    assert result.status == 502
    assert result.error["code"] == "provider_error"


@pytest.mark.parametrize(
    "status,code,expected",
    [
        (429, "busy", "busy"),
        (503, "model_not_installed", "model_not_installed"),
        (500, "internal", "provider_error"),
        (401, "auth_required", "provider_error"),
        (500, "made_up", "provider_error"),
    ],
)
def test_error_status_mapping(make, upstream, status, code, expected) -> None:
    upstream.status = status
    upstream.body = {"error": {"code": code, "message": "On the moon"}}
    result = relay(make, upstream.url).post_chat()
    assert result.data("error")["code"] == expected
    assert "On the moon" not in result.raw


def test_error_event_keeps_partial_text_and_local_wording(make, upstream) -> None:
    upstream.events = sse("delta", {"text": "partial"}) + sse(
        "error", {"code": "timeout", "message": "On the moon"}
    )
    local = relay(make, upstream.url)
    result = local.post_chat()
    assert result.data("error")["code"] == "timeout"
    assert "On the moon" not in result.raw
    row = local.conversation(result.conversation_id)["messages"][-1]
    assert (row["content"], row["finish_reason"]) == ("partial", "error")


def test_stream_end_without_terminal_event_is_provider_error(make, upstream) -> None:
    upstream.events = sse("delta", {"text": "partial"})
    assert relay(make, upstream.url).post_chat().data("error")["code"] == "provider_error"


def test_malformed_data_is_provider_error(make, upstream) -> None:
    upstream.events = "event: delta\ndata: not json\n\n"
    assert relay(make, upstream.url).post_chat().data("error")["code"] == "provider_error"


def test_idle_limit_applies(make) -> None:
    async def stream(request):
        async def generate():
            yield sse("delta", {"text": "partial"})
            await asyncio.sleep(5)

        return StreamingResponse(generate(), media_type="text/event-stream")

    async def health(request):
        return JSONResponse({})

    server = RunningServer(
        Starlette(
            routes=[Route("/v1/inference/stream", stream, methods=["POST"]), Route("/healthz", health)]
        )
    )
    try:
        local = relay(make, server.url, stream_idle_timeout_s=0.2)
        assert local.post_chat().data("error")["code"] == "timeout"
    finally:
        server.stop()


def test_startup_fails_without_the_token(make) -> None:
    with pytest.raises(ConfigError, match="DC_ORG_SERVER_TOKEN"):
        make(
            provider="org_server",
            overrides={"providers": {"org_server": {"base_url": "http://127.0.0.1:9"}}},
        )


def test_conversation_cannot_move_from_relay_to_mock(make, tmp_path) -> None:
    data_dir = tmp_path / "shared"
    server = host(make)
    local = make(
        data_dir=data_dir,
        provider="org_server",
        overrides={"providers": {"org_server": {"base_url": server.url}}},
        env={"DC_ORG_SERVER_TOKEN": SERVICE_TOKEN},
    )
    conversation_id = local.post_chat().conversation_id
    make.stop(local)
    demo = make(data_dir=data_dir, provider="mock")
    result = demo.post_chat(conversation_id=conversation_id)
    assert result.status == 409
    assert result.error["code"] == "conversation_locality_changed"
