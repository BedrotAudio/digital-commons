"""The demonstration provider."""

from __future__ import annotations

import re
import time

from app.providers.mock import DEMO_NOTICE, compose_reply
from app.services.context import SYSTEM_CODE, SYSTEM_GENERAL, attachment_block

FAST = {"providers": {"mock": {"tokens_per_second": 5000}}}


def test_every_reply_says_it_is_a_demonstration_near_the_start(make) -> None:
    gateway = make(provider="mock", overrides=FAST)
    result = gateway.post_chat(message="What is the capital of France?")
    assert result.text.startswith(DEMO_NOTICE)
    assert "demonstration" in result.text[:40]
    assert result.data("start")["route"]["locality"] == "demo"
    assert result.data("done")["usage"]["estimated"] is True


def test_code_assistant_gets_a_code_block(make) -> None:
    gateway = make(provider="mock", overrides=FAST)
    assert "```python" in gateway.post_chat(assistant="code").text
    assert "```" not in gateway.post_chat(assistant="general").text


def test_sources_are_cited_in_bracket_style(make) -> None:
    gateway = make(provider="mock", overrides=FAST)
    result = gateway.post_chat(message="remote work eligibility", use_documents=True)
    citations = result.data("citations")["citations"]
    assert citations
    assert "[1]" in result.text
    assert citations[0]["title"] in result.text


def test_no_citation_marks_without_sources(make) -> None:
    gateway = make(provider="mock", overrides=FAST)
    assert "[1]" not in gateway.post_chat(message="hello").text


def test_attached_file_names_are_mentioned(make) -> None:
    gateway = make(provider="mock", overrides=FAST)
    result = gateway.post_chat(
        message="",
        attachments=[{"name": "budget.csv", "text": "a,b"}, {"name": "notes.md", "text": "n"}],
    )
    assert "budget.csv" in result.text and "notes.md" in result.text
    assert "You asked" not in result.text


def test_advanced_tier_streams_thinking(make) -> None:
    gateway = make(provider="mock", overrides=FAST)
    result = gateway.post_chat(tier="advanced")
    assert result.all("thinking")
    assert "simulated" in "".join(item["text"] for item in result.all("thinking"))
    assert "thinking" not in gateway.post_chat(tier="standard").names()


def test_streams_at_the_configured_rate(make) -> None:
    gateway = make(provider="mock", overrides={"providers": {"mock": {"tokens_per_second": 100}}})
    began = time.monotonic()
    result = gateway.post_chat()
    elapsed = time.monotonic() - began
    expected = len(result.all("delta")) / 100
    assert expected * 0.8 <= elapsed <= expected * 2 + 0.5


def test_stops_at_max_output(make) -> None:
    catalog = {
        "models": {
            "catalog": {
                "mock": {
                    "fast": {
                        "name": "demo-fast",
                        "context": 8192,
                        "max_output": 5,
                        "temperature": 0.0,
                        "think": False,
                    }
                }
            }
        }
    }
    gateway = make(provider="mock", overrides={**FAST, **catalog})
    result = gateway.post_chat(tier="fast")
    assert result.data("done")["finish_reason"] == "length"
    assert len(result.all("delta")) == 5


def test_needs_no_model_and_reports_tiers_available(make) -> None:
    gateway = make(provider="mock", overrides=FAST)
    tiers = gateway.client.get("/v1/models").json()["tiers"]
    assert [item["available"] for item in tiers] == [True, True, True, False]


def test_compose_reply_keeps_to_the_basic_mode_vocabulary() -> None:
    banned = re.compile(
        r"\b(models?|llms?|inference|gateways?|backends?|servers?|ollama|tokens?|contexts?"
        r"|prompts?|tiers?|providers?|gpus?|apis?)\b",
        re.IGNORECASE,
    )
    assert banned.findall(compose_reply([{"role": "user", "content": "Hello"}])) == []


def test_compose_reply_reads_only_messages() -> None:
    messages = [
        {"role": "system", "content": SYSTEM_GENERAL},
        {"role": "user", "content": attachment_block("a.txt", "contents") + "\n\nquestion"},
    ]
    reply = compose_reply(messages)
    assert "`a.txt`" in reply and "> question" in reply and "contents" not in reply
    assert "```" in compose_reply([{"role": "system", "content": SYSTEM_CODE}, *messages[1:]])


def test_long_question_is_shortened_in_the_quote() -> None:
    reply = compose_reply([{"role": "user", "content": "word " * 100}])
    assert "..." in reply.split("\n\n")[2]
