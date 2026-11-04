"""Permissions, the router's boundaries, document filtering and conversation ownership."""

from __future__ import annotations

import re
from pathlib import Path

import pytest

import app
from app.config.settings import ROOT
from app.models.domain import TierSpec
from app.router.base import RouteSignals
from app.router.rules import RuleBasedRouter
from app.services.retriever import KeywordRetriever

RESTRICTED = {
    "identity": {
        "user": {
            "user_id": "restricted.user",
            "display_name": "Restricted",
            "groups": ["all-staff"],
            "permissions": {
                "advanced_mode": False,
                "manual_model_selection": False,
                "view_diagnostics": False,
                "search_documents": False,
                "allowed_tiers": ["fast"],
            },
        }
    }
}


def identity(**permissions: object) -> dict:
    return {"identity": {"user": {"permissions": permissions}}}


def tier(name: str, think: bool = False) -> TierSpec:
    return TierSpec(name, name, "", True, "m", 8192, 1024, 0.1, think)


def signals(message: str = "hi", **extra: object) -> RouteSignals:
    values = {"attachment_count": 0, "use_documents": False, "assistant": "general", **extra}
    return RouteSignals(message, **values)


ALL = [tier("fast"), tier("standard"), tier("advanced", True)]


@pytest.mark.parametrize(
    "request_signals,expected",
    [
        (signals("What is the capital of France?"), "fast"),
        (signals("hello", attachment_count=1), "standard"),
        (signals("hello", use_documents=True), "standard"),
        (signals("word " * 100), "standard"),
    ],
)
def test_router_signals(request_signals, expected) -> None:
    assert RuleBasedRouter().choose(request_signals, ALL).tier == expected


def test_router_always_gives_a_reason() -> None:
    assert RuleBasedRouter().choose(signals(), ALL).reason


@pytest.mark.parametrize(
    "message",
    ["def f(x):\n    return 1", "analyze and compare the strategy step by step", "x" * 3000],
)
def test_router_never_selects_a_reasoning_tier(message) -> None:
    assert RuleBasedRouter().choose(signals(message), ALL).tier == "standard"


def test_router_picks_advanced_when_it_does_not_reason() -> None:
    tiers = [tier("fast"), tier("standard"), tier("advanced", False)]
    assert RuleBasedRouter().choose(signals("def f(x): pass"), tiers).tier == "advanced"


def test_router_only_reasoning_tiers_gives_none() -> None:
    assert RuleBasedRouter().choose(signals(), [tier("advanced", True)]) is None


@pytest.mark.parametrize("message", ["hi", "def f(x): pass", "x" * 3000])
def test_router_cannot_widen_access(message) -> None:
    decision = RuleBasedRouter().choose(signals(message), [tier("fast")])
    assert decision.tier == "fast"


def test_auto_reaches_only_allowed_tiers(make, fake) -> None:
    gateway = make(overrides=RESTRICTED)
    long = "analyze and compare " * 100
    result = gateway.post_chat(message=long[:14000], attachments=[{"name": "a.txt", "text": "x"}])
    assert result.data("start")["route"]["tier"] == "fast"


def test_only_a_reasoning_tier_allowed_gives_a_clear_error(make) -> None:
    gateway = make(overrides=identity(allowed_tiers=["advanced"]))
    result = gateway.post_chat()
    assert result.status == 503
    assert result.error["code"] == "model_not_installed"


def test_restricted_identity_is_denied_manual_tier(make) -> None:
    gateway = make(overrides=RESTRICTED)
    result = gateway.post_chat(tier="fast")
    assert (result.status, result.error["code"]) == (403, "permission_denied")
    assert gateway.conversations() == []


def test_manual_tier_outside_allowed_tiers_is_denied(make) -> None:
    gateway = make(overrides=identity(allowed_tiers=["fast", "standard"]))
    result = gateway.post_chat(tier="advanced")
    assert (result.status, result.error["code"]) == (403, "permission_denied")


def test_restricted_identity_is_denied_the_code_assistant(make) -> None:
    gateway = make(overrides=RESTRICTED)
    result = gateway.post_chat(assistant="code")
    assert (result.status, result.error["code"]) == (403, "permission_denied")


def test_restricted_identity_is_denied_search(make) -> None:
    gateway = make(overrides=RESTRICTED)
    result = gateway.post_chat(use_documents=True)
    assert (result.status, result.error["code"]) == (403, "permission_denied")


def test_restricted_identity_cannot_list_models(make) -> None:
    gateway = make(overrides=RESTRICTED)
    response = gateway.client.get("/v1/models")
    assert response.status_code == 403
    assert response.json()["error"]["code"] == "permission_denied"


def test_restricted_bootstrap_hides_what_it_cannot_use(make) -> None:
    gateway = make(overrides=RESTRICTED)
    body = gateway.client.get("/v1/bootstrap").json()
    assert body["permissions"]["allowed_tiers"] == ["fast"]
    assert "documents" not in [item["action"] for item in body["suggestions"]]
    assert gateway.status()["inference"] is None


def test_models_lists_tiers_with_specialist_disabled(make) -> None:
    gateway = make()
    body = gateway.client.get("/v1/models").json()
    assert body["provider"] == "ollama"
    tiers = {item["tier"]: item for item in body["tiers"]}
    assert set(tiers) == {"fast", "standard", "advanced", "specialist"}
    assert tiers["specialist"]["enabled"] is False
    assert tiers["specialist"]["available"] is False
    assert tiers["standard"]["context_tokens"] == 8192
    assert tiers["advanced"]["thinking"] is True


def retriever() -> KeywordRetriever:
    return KeywordRetriever(ROOT / "data" / "sample_documents")


def test_search_ranks_the_matching_section_first() -> None:
    top = retriever().search("Who is eligible for remote work?", ["all-staff"], 4)[0]
    assert (top.document_id, top.section) == ("sample-remote-work", "Eligibility")
    top = retriever().search("sealed bid threshold", ["finance"], 4)[0]
    assert (top.document_id, top.section) == ("sample-procurement", "Thresholds")
    top = retriever().search("how long are invoices kept", ["all-staff"], 4)[0]
    assert top.section == "Financial records"


def test_filter_happens_before_ranking() -> None:
    results = retriever().search("investigator interviews complainant", ["all-staff"], 10)
    assert "sample-personnel-investigations" not in [item.document_id for item in results]
    hr = retriever().search("investigator interviews complainant", ["hr"], 10)
    assert hr[0].document_id == "sample-personnel-investigations"


def test_hidden_documents_do_not_change_scores() -> None:
    source = ROOT / "data" / "sample_documents"
    whole = KeywordRetriever(source)
    visible = whole._visible(["all-staff"])

    class WithoutHidden(KeywordRetriever):
        def __init__(self) -> None:
            self._sections = visible

    query = "records retention leave interviews"
    assert [
        (item.document_id, item.section) for item in whole.search(query, ["all-staff"], 10)
    ] == [
        (item.document_id, item.section) for item in WithoutHidden().search(query, ["all-staff"], 10)
    ]


def test_groups_are_matched_exactly_and_empty_groups_see_nothing() -> None:
    assert retriever().search("remote work", [], 10) == []
    assert retriever().search("remote work", ["all-staff-extra"], 10) == []


def test_document_results_cannot_reveal_hidden_documents_through_chat(make, fake) -> None:
    gateway = make()
    result = gateway.post_chat(
        message="investigator interviews complainant administrative leave respondent",
        use_documents=True,
    )
    assert "sample-personnel-investigations" not in result.raw
    assert "Personnel Investigations" not in result.raw
    assert "Personnel Investigations" not in str(fake.chats[0])


def test_a_query_only_hidden_documents_match_looks_like_no_match(make) -> None:
    gateway = make()
    hidden = gateway.post_chat(message="complainant respondent", use_documents=True)
    absent = gateway.post_chat(message="xylophone zebra", use_documents=True)
    assert "citations" not in hidden.names()
    assert hidden.data("start")["notices"] == absent.data("start")["notices"]


def test_sample_documents_are_labelled_fabricated() -> None:
    files = sorted((ROOT / "data" / "sample_documents").glob("*.md"))
    assert [path.name for path in files] == [
        "personnel-investigations.md",
        "procurement-thresholds.md",
        "records-retention-schedule.md",
        "remote-work-policy.md",
    ]
    for path in files:
        assert "Fabricated sample content" in path.read_text(encoding="utf-8")


def test_stored_citations_hold_only_four_fields(make) -> None:
    import sqlite3

    gateway = make()
    result = gateway.post_chat(message="remote work eligibility", use_documents=True)
    connection = sqlite3.connect(gateway.data_dir / "conversations.db")
    stored = connection.execute(
        "SELECT citations FROM messages WHERE role = 'assistant'"
    ).fetchone()[0]
    connection.close()
    import json

    assert set(json.loads(stored)[0]) == {"number", "document_id", "section", "version"}
    assert result.all("citations")


def test_citations_are_rebuilt_through_the_filter_on_read(make, tmp_path) -> None:
    data_dir = tmp_path / "shared"
    finance = make(data_dir=data_dir, overrides=identity())
    result = finance.post_chat(message="purchase threshold quotes sole source", use_documents=True)
    conversation_id = result.conversation_id
    cited = finance.conversation(conversation_id)["messages"][-1]["citations"]
    assert cited and "title" in cited[0]
    make.stop(finance)

    narrowed = make(
        data_dir=data_dir, overrides={"identity": {"user": {"groups": ["all-staff"]}}}
    )
    after = narrowed.conversation(conversation_id)["messages"][-1]["citations"]
    assert after == [{"number": item["number"], "withheld": True} for item in cited]


def test_other_users_conversation_is_not_found_everywhere(make, tmp_path, fake) -> None:
    data_dir = tmp_path / "shared"
    owner = make(data_dir=data_dir)
    conversation_id = owner.post_chat(message="private").conversation_id
    make.stop(owner)

    other = make(
        data_dir=data_dir,
        overrides={"identity": {"user": {"user_id": "someone.else"}}},
    )
    assert other.conversations() == []
    assert other.client.get(f"/v1/conversations/{conversation_id}").status_code == 404
    assert other.client.delete(f"/v1/conversations/{conversation_id}").status_code == 404
    assert other.client.delete("/v1/conversations").json() == {"deleted": 0}
    chats = len(fake.chats)
    for body in ({"message": "x"}, {"regenerate": True}):
        result = other.post_chat(conversation_id=conversation_id, **body)
        assert result.status == 404
        assert result.error["code"] == "not_found"
    assert len(fake.chats) == chats
    make.stop(other)

    again = make(data_dir=data_dir)
    assert len(again.conversations()) == 1


def test_store_protocol_requires_user_id_everywhere() -> None:
    import inspect

    from app.services.store import ConversationStore

    for name, method in inspect.getmembers(ConversationStore, inspect.isfunction):
        if not name.startswith("_"):
            assert list(inspect.signature(method).parameters)[1] == "user_id", name


def test_identifiers_are_128_bit_random(make) -> None:
    result = make().post_chat()
    assert re.fullmatch(r"[0-9a-f]{32}", result.conversation_id)


@pytest.mark.parametrize("header", ["X-Mode", "X-Client-Mode", "X-Developer-Mode"])
def test_mode_cannot_be_claimed_by_a_client(make, header) -> None:
    gateway = make(overrides=RESTRICTED)
    response = gateway.client.post(
        "/v1/chat/stream",
        json={"message": "x", "assistant": "code", "mode": "developer"},
        headers={header: "developer"},
    )
    assert response.status_code == 403


def test_mode_appears_nowhere_in_authorization_code() -> None:
    package = Path(app.__file__).parent
    pattern = re.compile(r"\b(basic|developer)\b|\bui_mode\b|\bclient_mode\b", re.IGNORECASE)
    sources = [
        package / "services" / "turn.py",
        package / "api" / "routes.py",
        package / "api" / "guard.py",
        package / "security" / "identity.py",
        package / "router" / "rules.py",
        package / "router" / "registry.py",
    ]
    for path in sources:
        assert not pattern.search(path.read_text(encoding="utf-8")), path.name


def test_conversation_locality_change_is_refused_before_any_upstream_call(
    make, fake, tmp_path
) -> None:
    from dataclasses import replace

    from harness import FAST_TUNING, wait_until

    data_dir = tmp_path / "shared"
    demo = make(
        data_dir=data_dir,
        provider="mock",
        overrides={"providers": {"mock": {"tokens_per_second": 5000}}},
    )
    conversation_id = demo.post_chat().conversation_id
    make.stop(demo)

    local = make(data_dir=data_dir, tuning=replace(FAST_TUNING, status_interval_s=3600))
    wait_until(lambda: local.services.monitor.health is not None)
    fake.requests.clear()
    for body in ({"message": "x"}, {"regenerate": True}):
        result = local.post_chat(conversation_id=conversation_id, **body)
        assert result.status == 409
        assert result.error["code"] == "conversation_locality_changed"
    assert fake.requests == []


def test_a_closed_gate_is_not_reported_as_a_locality_change(make, fake) -> None:
    gateway = make()
    conversation_id = gateway.post_chat().conversation_id
    fake.status_body = {"cloud": {"disabled": False}}
    result = gateway.post_chat(conversation_id=conversation_id)
    assert result.status == 503
    assert result.error["code"] == "model_not_permitted"


def test_an_outage_is_not_reported_as_a_locality_change(make, tmp_path, fake) -> None:
    data_dir = tmp_path / "shared"
    first = make(data_dir=data_dir)
    conversation_id = first.post_chat().conversation_id
    make.stop(first)
    second = make(data_dir=data_dir, fake_url="http://127.0.0.1:9")
    assert second.post_chat(conversation_id=conversation_id).error["code"] == "provider_unavailable"
