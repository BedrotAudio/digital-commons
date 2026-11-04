"""The Ollama wire format, and how its replies map onto the gateway contract."""

from __future__ import annotations

import pytest

from harness import MODEL, chat_chunk, final_chunk

OVERFLOW = {"error": "request (9000 tokens) exceeds the available context size (8192 tokens), try increasing it"}


def test_chat_request_shape_is_exact(make, fake) -> None:
    gateway = make()
    gateway.post_chat(tier="standard")
    body = fake.chats[0]
    assert set(body) == {"model", "messages", "stream", "think", "keep_alive", "truncate", "options"}
    assert body["stream"] is True
    assert body["think"] is False
    assert body["truncate"] is False
    assert body["keep_alive"] == "30m"
    assert body["model"] == MODEL
    assert body["options"] == {"num_ctx": 8192, "num_predict": 2048, "temperature": 0.4}


@pytest.mark.parametrize("forbidden", ["tools", "format", "raw", "template"])
def test_never_sends_extra_controls(make, fake, forbidden) -> None:
    make().post_chat(assistant="code", tier="advanced")
    assert forbidden not in fake.chats[0]


def test_think_follows_the_tier(make, fake) -> None:
    gateway = make()
    gateway.post_chat(tier="advanced")
    gateway.post_chat(tier="fast")
    assert [body["think"] for body in fake.chats] == [True, False]


def test_only_content_thinking_and_statistics_are_read(make, fake) -> None:
    hostile = {
        **chat_chunk("visible"),
        "tool_calls": [{"function": {"name": "x"}}],
        "extra": "ignored",
    }
    hostile["message"]["tool_calls"] = [{"function": {"name": "x"}}]
    hostile["message"]["role"] = "system"
    fake.chunks = [hostile, final_chunk()]
    result = make().post_chat()
    assert result.text == "visible"
    assert "tool_calls" not in result.raw


def test_usage_comes_from_the_final_chunk(make, fake) -> None:
    usage = make().post_chat().data("done")["usage"]
    assert usage["prompt_tokens"] == 23
    assert usage["completion_tokens"] == 4
    assert usage["total_tokens"] == 27
    assert usage["load_ms"] == 78
    assert usage["tokens_per_second"] == 4.0
    assert usage["estimated"] is False
    assert usage["first_token_ms"] >= 0
    assert usage["total_ms"] >= usage["first_token_ms"]


def test_length_finish_reason(make, fake) -> None:
    fake.chunks = [chat_chunk("cut"), final_chunk("length")]
    gateway = make()
    result = gateway.post_chat()
    assert result.data("done")["finish_reason"] == "length"
    assert gateway.conversation(result.conversation_id)["messages"][-1]["finish_reason"] == "length"


def test_loading_status_when_the_model_is_not_loaded(make, fake) -> None:
    fake.ps = []
    result = make().post_chat()
    assert [item["phase"] for item in result.all("status")][0] == "loading_model"
    assert "first response" in result.all("status")[0]["message"]


def test_loading_status_when_loaded_at_another_context(make, fake) -> None:
    fake.ps = [fake.loaded(context=4096)]
    phases = [item["phase"] for item in make().post_chat().all("status")]
    assert "loading_model" in phases


def test_no_loading_status_when_loaded(make, fake) -> None:
    phases = [item["phase"] for item in make().post_chat().all("status")]
    assert "loading_model" not in phases


def test_model_names_compare_case_insensitively(make, fake) -> None:
    fake.tags = [{"name": "gemma4:12B"}]
    fake.ps = [fake.loaded(name="GEMMA4:12b")]
    result = make().post_chat()
    assert result.text == "Hello there."
    assert "loading_model" not in [item["phase"] for item in result.all("status")]


def test_ps_runs_before_every_chat(make, fake) -> None:
    from dataclasses import replace

    from harness import FAST_TUNING, wait_until

    gateway = make(tuning=replace(FAST_TUNING, status_interval_s=3600))
    wait_until(lambda: gateway.services.monitor.health is not None)
    fake.requests.clear()
    gateway.post_chat()
    assert len(fake.calls("/api/ps")) == 1


def test_overflow_is_retried_once_with_scaled_content(make, fake) -> None:
    fake.chat_script.append((400, OVERFLOW))
    gateway = make()
    result = gateway.post_chat(
        attachments=[{"name": "big.txt", "media_type": "text/plain", "text": "word " * 4000}]
    )
    assert result.data("done")["finish_reason"] == "stop"
    assert len(fake.chats) == 2
    first, second = (len(body["messages"][-1]["content"]) for body in fake.chats)
    assert second < first * 0.75
    assert any("left out" in item["message"] for item in result.all("status"))


def test_overflow_twice_emits_context_too_large(make, fake) -> None:
    fake.chat_script.extend([(400, OVERFLOW), (400, OVERFLOW)])
    gateway = make()
    result = gateway.post_chat(
        attachments=[{"name": "big.txt", "media_type": "text/plain", "text": "word " * 4000}]
    )
    assert result.data("error")["code"] == "context_too_large"
    assert len(fake.chats) == 2


def test_overflow_where_shrinking_cannot_help(make, fake) -> None:
    fake.chat_script.append((400, OVERFLOW))
    gateway = make()
    result = gateway.post_chat(message="word " * 2800)
    assert result.data("error")["code"] == "context_too_large"
    assert len(fake.chats) == 1


def test_400_of_another_kind_is_provider_error(make, fake) -> None:
    fake.chat_script.append((400, {"error": "invalid options"}))
    assert make().post_chat().data("error")["code"] == "provider_error"


@pytest.mark.parametrize(
    "status,code",
    [(403, "model_not_permitted"), (404, "model_not_installed"), (500, "provider_error")],
)
def test_chat_status_mapping(make, fake, status, code) -> None:
    fake.chat_script.append((status, {"error": "ollama cloud is disabled: remote model is unavailable"}))
    assert make().post_chat().data("error")["code"] == code


def test_500_while_loading_is_model_load_failed(make, fake) -> None:
    fake.ps = []
    fake.chat_script.append((500, {"error": "llama runner process has terminated"}))
    assert make().post_chat().data("error")["code"] == "model_load_failed"


def test_stream_ending_without_a_final_chunk_is_provider_error(make, fake) -> None:
    fake.chunks = [chat_chunk("partial")]
    gateway = make()
    result = gateway.post_chat()
    assert result.data("error")["code"] == "provider_error"
    assert gateway.conversation(result.conversation_id)["messages"][-1]["finish_reason"] == "error"


def test_error_chunk_mid_stream_is_provider_error(make, fake) -> None:
    fake.chunks = [chat_chunk("partial"), {"error": "runner died"}]
    assert make().post_chat().data("error")["code"] == "provider_error"


def test_malformed_stream_is_provider_error(make, fake) -> None:
    fake.chunks = [chat_chunk("partial"), "not an object"]
    assert make().post_chat().data("error")["code"] == "provider_error"


def test_first_token_limit(make, fake) -> None:
    fake.first_token_delay = 2
    gateway = make(overrides={"providers": {"ollama": {"first_token_timeout_s": 0.2}}})
    result = gateway.post_chat()
    assert result.data("error")["code"] == "timeout"
    assert result.data("error")["retryable"] is True


def test_idle_limit_applies_after_the_first_chunk(make, fake) -> None:
    fake.chunks = [chat_chunk("partial"), chat_chunk("late"), final_chunk()]
    fake.chunk_delay = 1
    gateway = make(overrides={"providers": {"ollama": {"stream_idle_timeout_s": 0.2}}})
    result = gateway.post_chat()
    assert result.text == "partial"
    assert result.data("error")["code"] == "timeout"
    assert gateway.conversation(result.conversation_id)["messages"][-1]["finish_reason"] == "error"


def test_idle_limit_does_not_apply_to_the_first_token(make, fake) -> None:
    fake.first_token_delay = 0.5
    gateway = make(overrides={"providers": {"ollama": {"stream_idle_timeout_s": 0.1}}})
    assert gateway.post_chat().text == "Hello there."


def test_unreachable_provider_is_a_normal_error(make) -> None:
    gateway = make(fake_url="http://127.0.0.1:9")
    result = gateway.post_chat()
    assert result.status == 503
    assert result.error["code"] == "provider_unavailable"
    assert result.error["retryable"] is True
    assert gateway.conversations() == []


def two_models() -> dict:
    def spec(name: str, think: bool = False) -> dict:
        return {"name": name, "context": 8192, "max_output": 1024, "temperature": 0.1, "think": think}

    return {
        "models": {
            "catalog": {
                "ollama": {
                    "fast": spec("small:1"),
                    "standard": spec("middle:1"),
                    "advanced": spec("large:1", True),
                }
            }
        }
    }


def test_missing_model_falls_back_and_says_so(make, fake) -> None:
    fake.tags = [{"name": "small:1"}]
    gateway = make(overrides=two_models())
    result = gateway.post_chat(tier="standard")
    start = result.data("start")
    assert start["route"]["tier"] == "fast"
    assert start["route"]["fallback_from"] == "standard"
    assert [item["code"] for item in start["notices"]] == ["tier_fallback"]


def test_fallback_never_lands_on_a_reasoning_tier(make, fake) -> None:
    fake.tags = [{"name": "large:1"}]
    gateway = make(overrides=two_models())
    result = gateway.post_chat(tier="standard")
    assert result.status == 503
    assert result.error["code"] == "model_not_installed"


def test_no_model_at_all(make, fake) -> None:
    fake.tags = []
    result = make().post_chat()
    assert result.status == 503
    assert result.error["code"] == "model_not_installed"
    assert result.error["retryable"] is False
