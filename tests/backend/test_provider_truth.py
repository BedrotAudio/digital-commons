"""What the adapters read from a provider, and what the gateway then claims."""

from __future__ import annotations

import json
import re
from pathlib import Path

import pytest

from app.security.paths import folder_keeps_user_only_access
from app.services import privacy
from harness import MODEL, chat_chunk, final_chunk, wait_until
from test_relay import relay, sse, upstream  # noqa: F401
from test_status_warmup_privacy import statements


def test_a_model_with_no_reasoning_says_what_to_change(make, fake) -> None:
    fake.chat_script.append((400, {"error": f'"{MODEL}" does not support thinking'}))
    error = make().post_chat(tier="advanced").data("error")
    assert error["code"] == "model_not_installed"
    assert error["retryable"] is False
    assert "think" in error["detail"]
    assert "models.yaml" in error["detail"]


def test_one_failed_inventory_is_not_reported_as_no_model(make, fake) -> None:
    gateway = make()
    wait_until(lambda: gateway.status()["state"] == "ready")
    fake.redirect_paths = {"/api/tags"}
    view = wait_until(lambda: (lambda seen: seen if seen["state"] != "ready" else None)(gateway.status()))
    assert view["state"] == "unavailable"
    assert view["reason"] == "provider_unreachable"
    assert "installed" not in view["summary"]
    assert view["inference"]["missing_models"] == []
    tiers = gateway.client.get("/v1/models").json()["tiers"]
    assert not any(tier["unavailable_reason"] == "Not installed" for tier in tiers)

    fake.redirect_paths = set()
    wait_until(lambda: gateway.status()["state"] == "ready")


def test_a_model_truly_missing_is_still_reported(make, fake) -> None:
    fake.tags = []
    gateway = make()
    view = wait_until(lambda: (lambda seen: seen if seen["reason"] else None)(gateway.status()))
    assert view["reason"] == "model_missing"


@pytest.mark.parametrize("character", ["\u0085", "\u2028", "\u2029", "\x0b", "\x0c", "\x1c"])
def test_ollama_text_may_hold_any_line_separator(make, fake, character) -> None:
    fake.raw_chunks = True
    fake.chunks = [chat_chunk(f"before{character}after"), final_chunk()]
    result = make().post_chat()
    assert result.text == f"before{character}after"
    assert result.data("done")


def raw_sse(event: str, data: dict) -> str:
    """As a Python host would send it: json.dumps leaves U+2028 and U+0085 raw."""
    return f"event: {event}\ndata: {json.dumps(data, ensure_ascii=False)}\n\n"


@pytest.mark.parametrize("character", ["\u0085", "\u2028", "\u2029", "\x0c"])
def test_relayed_text_may_hold_any_line_separator(make, upstream, character) -> None:  # noqa: F811
    upstream.events = raw_sse("delta", {"text": f"before{character}after"}) + raw_sse(
        "done", {"finish_reason": "stop", "usage": {}}
    )
    result = relay(make, upstream.url).post_chat()
    assert result.text == f"before{character}after"
    assert result.data("done")


def test_lines_split_across_reads_are_joined(make, upstream) -> None:  # noqa: F811
    upstream.events = sse("delta", {"text": "é" * 40000}) + sse(
        "done", {"finish_reason": "stop", "usage": {}}
    )
    result = relay(make, upstream.url).post_chat()
    assert result.text == "é" * 40000


def test_who_can_connect_follows_the_allowed_hosts(make) -> None:
    gateway = make(
        overrides={"security": {"allowed_client_hosts": ["127.0.0.1", "localhost", "ai.example.gov"]}}
    )
    item = statements(gateway)["network_exposure"]
    assert item["status"] == "info"
    assert "this computer" not in item["value"]
    assert "ai.example.gov" in item["detail"]


def test_who_can_connect_ignores_loopback_names(make, monkeypatch) -> None:
    monkeypatch.setattr(privacy, "local_ipv4_addresses", lambda: [])
    gateway = make(overrides={"security": {"allowed_client_hosts": ["127.0.0.1", "LOCALHOST", "[::1]"]}})
    assert statements(gateway)["network_exposure"]["value"] == "Only programs on this computer"


def test_basic_mode_is_told_attached_text_is_stored(make) -> None:
    item = statements(make())["conversation_storage"]
    assert "attached file" in item["value"]


@pytest.mark.parametrize(
    "platform,path,env,expected",
    [
        ("win32", r"C:\Users\pat\AppData\Local\DigitalCommons", {"LOCALAPPDATA": r"C:\Users\pat\AppData\Local"}, True),
        ("win32", r"c:\users\PAT\scratch\dc", {}, True),
        ("win32", r"D:\Redirected\Local\DigitalCommons", {"LOCALAPPDATA": r"D:\Redirected\Local"}, True),
        ("win32", r"C:\DigitalCommons", {"LOCALAPPDATA": r"C:\Users\pat\AppData\Local"}, False),
        ("win32", r"C:\temp\dc-scratch", {}, False),
        ("win32", r"D:\data", {}, False),
        ("darwin", "/opt/dc", {}, True),
        ("linux", "/opt/dc", {}, True),
    ],
)
def test_which_folders_keep_user_only_access(platform, path, env, expected) -> None:
    home = r"C:\Users\pat" if platform == "win32" else "/home/pat"
    assert folder_keeps_user_only_access(path, env, platform, home) is expected


def test_a_folder_that_may_be_shared_is_not_called_private(make, monkeypatch) -> None:
    monkeypatch.setattr(privacy, "folder_keeps_user_only_access", lambda *_: False)
    item = statements(make())["conversation_storage"]
    assert item["status"] == "warn"
    assert "private" not in item["value"]
    assert "Other accounts" in item["value"]


def test_the_warning_values_stay_plain(make, monkeypatch) -> None:
    monkeypatch.setattr(privacy, "folder_keeps_user_only_access", lambda *_: False)
    gateway = make(overrides={"security": {"allowed_client_hosts": ["127.0.0.1", "ai.example.gov"]}})
    technical = re.compile(
        r"ollama|sqlite|tcp|http|api|port|token|127\.0\.0\.1|localhost|gateway|proxy|host",
        re.IGNORECASE,
    )
    found = statements(gateway)
    for name in ("conversation_storage", "network_exposure"):
        assert not technical.search(found[name]["value"]), found[name]["value"]


def test_samples_are_labelled_for_the_person_and_the_model(make, fake) -> None:
    gateway = make()
    result = gateway.post_chat(message="remote work eligibility", use_documents=True)
    citations = result.data("citations")["citations"]
    assert citations and all(item["sample"] is True for item in citations)
    assert "invented sample" in fake.chats[0]["messages"][-1]["content"]

    stored = gateway.conversation(result.conversation_id)["messages"][-1]["citations"]
    assert all(item["sample"] is True for item in stored)


def test_a_real_document_is_not_labelled(tmp_path) -> None:
    from app.services.retriever import KeywordRetriever, citation_wire

    folder = tmp_path / "documents"
    folder.mkdir()
    (folder / "real.md").write_text(
        "---\ndocument_id: real\ntitle: Real\ndepartment: Finance\nclassification: public\n"
        'allowed_groups: [staff]\nversion: "1"\ndocument_date: "2026-01-01"\n---\n\n'
        "## Rule\n\nA real rule about travel.\n",
        encoding="utf-8",
    )
    passage = KeywordRetriever(folder).search("travel", ["staff"], 1)[0]
    assert citation_wire(1, passage)["sample"] is False


def test_every_shipped_sample_declares_itself() -> None:
    from app.services.retriever import KeywordRetriever

    folder = Path(__file__).resolve().parents[2] / "data" / "sample_documents"
    sections = KeywordRetriever(folder)._sections
    assert sections and all(section.passage.sample for section in sections)
