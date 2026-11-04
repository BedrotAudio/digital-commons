"""Exact SSE format and event ordering."""

from __future__ import annotations

import json
import re

from harness import chat_chunk, final_chunk


def test_content_type_and_headers(make) -> None:
    result = make().post_chat()
    assert result.headers["content-type"].startswith("text/event-stream")
    assert result.headers["cache-control"] == "no-store"
    assert result.headers["x-request-id"]


def test_exact_wire_format(make) -> None:
    result = make().post_chat()
    assert "\r" not in result.raw
    assert result.raw.endswith("\n\n")
    for block in result.raw.strip("\n").split("\n\n"):
        event, data = block.split("\n")
        assert re.fullmatch(r"event: [a-z]+", event)
        assert data.startswith("data: {")
        payload = data[6:]
        assert json.dumps(json.loads(payload), separators=(",", ":"), ensure_ascii=False) == payload


def test_order_without_documents(make) -> None:
    names = make().post_chat().names()
    assert names[0] == "start"
    assert names[-1] == "done"
    assert names.count("start") == names.count("done") == 1
    assert names[names.index("delta") - 1] == "status"


def test_start_payload(make) -> None:
    result = make().post_chat(tier="standard")
    start = result.data("start")
    assert start["request_id"] == result.headers["x-request-id"]
    assert re.fullmatch(r"[0-9a-f]{32}", start["conversation_id"])
    assert re.fullmatch(r"[0-9a-f]{32}", start["message_id"])
    assert start["route"] == {
        "requested_tier": "standard",
        "tier": "standard",
        "locality": "this_device",
        "fallback_from": None,
        "model": "Gemma4:12b",
        "provider": "ollama",
        "reason": "Manual selection",
    }
    assert start["context"]["budget_tokens"] == 6144
    assert start["context"]["omitted_messages"] == 0
    assert start["context"]["estimated_tokens"] > 0
    assert start["notices"] == []


def test_citations_come_once_and_before_the_first_delta(make) -> None:
    result = make().post_chat(message="remote work eligibility", use_documents=True)
    names = result.names()
    assert names.count("citations") == 1
    assert names.index("start") < names.index("citations") < names.index("delta")
    citation = result.data("citations")["citations"][0]
    assert set(citation) == {
        "number",
        "document_id",
        "title",
        "department",
        "classification",
        "version",
        "document_date",
        "source_filename",
        "section",
        "page",
        "snippet",
        "sample",
    }
    assert citation["number"] == 1


def test_no_citations_event_without_documents(make) -> None:
    assert "citations" not in make().post_chat(message="remote work").names()


def test_documents_none_found_notice(make) -> None:
    result = make().post_chat(message="xylophone zebra", use_documents=True)
    assert "citations" not in result.names()
    assert [item["code"] for item in result.data("start")["notices"]] == ["documents_none_found"]


def test_reading_status_only_above_1000_tokens(make, fake) -> None:
    gateway = make()
    assert "reading" not in [item["phase"] for item in gateway.post_chat().all("status")]
    long = gateway.post_chat(attachments=[{"name": "a.txt", "text": "word " * 1500}])
    assert long.data("start")["context"]["estimated_tokens"] > 1000
    assert "reading" in [item["phase"] for item in long.all("status")]


def test_loading_status_is_the_last_one_before_the_wait(make, fake) -> None:
    fake.ps = []
    long = make().post_chat(attachments=[{"name": "a.txt", "text": "word " * 1500}])
    phases = [item["phase"] for item in long.all("status")]
    assert phases.index("reading") < phases.index("loading_model") < phases.index("generating")


def test_thinking_counts_as_first_token_and_precedes_delta(make, fake) -> None:
    fake.chunks = [chat_chunk("", thinking="hmm"), chat_chunk("Yes"), final_chunk()]
    result = make().post_chat(tier="advanced")
    names = result.names()
    assert names.index("thinking") < names.index("delta")
    assert result.data("done")["usage"]["first_token_ms"] is not None


def test_error_is_terminal_and_has_the_contract_fields(make, fake) -> None:
    fake.chunks = [chat_chunk("x")]
    result = make().post_chat()
    assert result.names()[-1] == "error"
    assert "done" not in result.names()
    assert set(result.data("error")) == {"code", "message", "detail", "retryable"}


def test_done_has_the_contract_fields(make) -> None:
    done = make().post_chat().data("done")
    assert set(done) == {"message_id", "finish_reason", "usage"}
    assert set(done["usage"]) == {
        "prompt_tokens",
        "completion_tokens",
        "total_tokens",
        "first_token_ms",
        "total_ms",
        "load_ms",
        "tokens_per_second",
        "estimated",
    }


def test_route_diagnostics_are_hidden_without_permission(make) -> None:
    gateway = make(
        overrides={
            "identity": {
                "user": {
                    "permissions": {
                        "view_diagnostics": False,
                        "manual_model_selection": False,
                    }
                }
            }
        }
    )
    result = gateway.post_chat()
    route = result.data("start")["route"]
    assert set(route) == {"requested_tier", "tier", "locality", "fallback_from"}
    stored = gateway.conversation(result.conversation_id)["messages"][-1]["route"]
    assert set(stored) == set(route)


def test_non_ascii_text_stays_readable_on_the_wire(make, fake) -> None:
    fake.chunks = [chat_chunk("café \U0001f600"), final_chunk()]
    gateway = make()
    result = gateway.post_chat()
    assert "café" in result.raw
    assert result.text == "café \U0001f600"


def test_lone_surrogate_from_a_provider_cannot_break_the_stream(make, fake) -> None:
    fake.chunks = [chat_chunk("before \ud83d after"), final_chunk()]
    gateway = make()
    result = gateway.post_chat()
    assert result.data("done")["finish_reason"] == "stop"
    assert gateway.conversation(result.conversation_id)["messages"][-1]["content"].startswith("before")
