"""Context budget arithmetic from docs/api.md."""

from __future__ import annotations

import math

import pytest

from app.models.domain import Attachment, Passage
from app.models.errors import ApiError
from app.services.context import (
    ContextInputs,
    assemble_context,
    attachment_block,
    estimate_tokens,
    share_budget,
)

SYSTEM = "s" * 40  # 26 tokens


def passage(text: str, section: str = "Section") -> Passage:
    return Passage(
        document_id="d",
        title="T",
        department="D",
        classification="public",
        version="1",
        document_date="2026-01-01",
        source_filename="d.md",
        section=section,
        text=text,
        snippet=text[:20],
    )


def turns(count: int, size: int = 400) -> list[dict[str, str]]:
    history = []
    for index in range(count):
        history.append({"role": "user", "content": f"{index}".ljust(size, "u")})
        history.append({"role": "assistant", "content": f"{index}".ljust(size, "a")})
    return history


def test_estimate_is_ceil_of_chars_over_four_plus_sixteen() -> None:
    assert estimate_tokens("") == 16
    assert estimate_tokens("abcd") == 17
    assert estimate_tokens("abcde") == 18
    assert estimate_tokens("x" * 4001) == math.ceil(4001 / 4) + 16


def test_budget_is_context_minus_max_output_on_the_wire(make) -> None:
    gateway = make()
    start = gateway.post_chat(tier="fast").data("start")
    assert start["context"]["budget_tokens"] == 8192 - 1024
    start = gateway.post_chat(tier="advanced").data("start")
    assert start["context"]["budget_tokens"] == 8192 - 4096


def test_estimated_tokens_adds_up() -> None:
    inputs = ContextInputs(SYSTEM, "m" * 8, history=[{"role": "user", "content": "h" * 4}])
    context = assemble_context(inputs, 1000)
    assert context.estimated_tokens == 26 + 18 + 17
    assert context.budget_tokens == 1000


def test_message_alone_over_budget_is_context_too_large() -> None:
    with pytest.raises(ApiError) as caught:
        assemble_context(ContextInputs(SYSTEM, "m" * 400), 100)
    assert caught.value.code == "context_too_large"
    assert caught.value.status == 422


def test_context_too_large_reaches_the_client_before_start(make) -> None:
    small = {"models": {"catalog": {"ollama": {"advanced": {"max_output": 7192}}}}}
    gateway = make(overrides=small)
    result = gateway.post_chat(message="m" * 15000, tier="advanced")
    assert result.status == 422
    assert result.error["code"] == "context_too_large"
    assert gateway.conversations() == []


def test_message_exactly_filling_budget_passes() -> None:
    message = "m" * ((100 - 26 - 16) * 4)
    assert assemble_context(ContextInputs(SYSTEM, message), 100).estimated_tokens == 100


@pytest.mark.parametrize(
    "sizes,available,expected",
    [
        ([10, 10], 100, [10, 10]),
        ([100, 100], 100, [50, 50]),
        ([10, 100], 100, [10, 90]),
        ([100, 10, 100], 100, [45, 10, 45]),
        ([100, 100, 100], 10, [3, 3, 4]),
        ([5], 0, [0]),
        ([], 10, []),
    ],
)
def test_share_budget_passes_unused_share_along(sizes, available, expected) -> None:
    assert share_budget(sizes, available) == expected


def test_attachments_share_equally_and_unused_share_passes() -> None:
    small = Attachment("small.txt", "text/plain", "s" * 40)
    large = Attachment("large.txt", "text/plain", "l" * 8000)
    budget = 1000
    context = assemble_context(ContextInputs(SYSTEM, "m", [small, large]), budget)
    kept = {item.name: item for item in context.attachments}
    assert kept["small.txt"].text == small.text
    assert kept["large.txt"].text == large.text[: len(kept["large.txt"].text)]
    assert context.estimated_tokens <= budget
    assert context.estimated_tokens >= budget - 2
    assert [item["code"] for item in context.notices] == ["attachment_truncated"]
    assert "large.txt" in context.notices[0]["message"]
    assert "%" in context.notices[0]["message"]


def test_two_large_attachments_get_equal_room() -> None:
    first = Attachment("a.txt", "text/plain", "a" * 8000)
    second = Attachment("b.txt", "text/plain", "b" * 8000)
    context = assemble_context(ContextInputs(SYSTEM, "m", [first, second]), 1000)
    assert abs(len(context.attachments[0].text) - len(context.attachments[1].text)) <= 4
    assert len(context.notices) == 2


def test_text_is_cut_from_the_tail() -> None:
    item = Attachment("a.txt", "text/plain", "HEAD" + "x" * 8000 + "TAIL")
    context = assemble_context(ContextInputs(SYSTEM, "m", [item]), 500)
    assert context.attachments[0].text.startswith("HEAD")
    assert "TAIL" not in context.attachments[0].text


def test_attachment_with_no_room_is_named_in_a_notice() -> None:
    item = Attachment("a.txt", "text/plain", "x" * 8000)
    context = assemble_context(ContextInputs(SYSTEM, "m", [item]), 26 + 17 + 3)
    # Nothing is sent, and the record keeps the file with no text, so a reopened chat
    # and Try again still say it was not read.
    assert "ATTACHED FILE" not in context.messages[-1]["content"]
    assert context.attachments == [Attachment("a.txt", "text/plain", "", 8000)]
    assert "was not read" in context.notices[0]["message"]


def test_attachment_block_goes_in_the_user_message_never_system() -> None:
    item = Attachment("a.txt", "text/plain", "MARKER")
    context = assemble_context(ContextInputs(SYSTEM, "m", [item]), 1000)
    assert "MARKER" not in context.messages[0]["content"]
    assert context.messages[0]["role"] == "system"
    assert context.messages[-1]["role"] == "user"
    assert attachment_block("a.txt", "MARKER") in context.messages[-1]["content"]


def test_passages_go_in_the_user_message_never_system() -> None:
    context = assemble_context(ContextInputs(SYSTEM, "m", passages=[passage("MARKER")]), 1000)
    assert "MARKER" not in context.messages[0]["content"]
    assert "MARKER" in context.messages[-1]["content"]


def test_priority_attachments_before_passages_before_history() -> None:
    item = Attachment("a.txt", "text/plain", "x" * 3000)
    inputs = ContextInputs(
        SYSTEM, "m", [item], [passage("p" * 400)], turns(4, 400)
    )
    context = assemble_context(inputs, 900)
    assert context.attachments == [item]
    assert len(context.passages) == 0
    assert context.omitted_messages == 8


def test_passage_room_keeps_a_ranked_prefix() -> None:
    passages = [passage("a" * 40), passage("b" * 4000), passage("c" * 40)]
    context = assemble_context(ContextInputs(SYSTEM, "m", passages=passages), 200)
    assert [item.text[0] for item in context.passages] == ["a"]


def test_history_that_fits_is_kept_whole() -> None:
    context = assemble_context(ContextInputs(SYSTEM, "m", history=turns(4)), 1000)
    assert context.omitted_messages == 0
    assert len(context.messages) == 10
    assert context.notices == []


def test_trim_drops_oldest_turns_to_70_percent_of_budget() -> None:
    history = turns(10)  # each message 116 tokens, 20 messages
    budget = 1000
    context = assemble_context(ContextInputs(SYSTEM, "m", history=history), budget)
    assert context.estimated_tokens <= budget * 0.7
    assert context.estimated_tokens > budget * 0.7 - 2 * 116
    assert context.omitted_messages % 2 == 0
    assert context.messages[1]["content"].startswith(str(context.omitted_messages // 2))
    assert [item["code"] for item in context.notices] == ["history_trimmed"]


def test_trim_removes_a_whole_turn() -> None:
    history = [
        {"role": "user", "content": "u" * 2000},
        {"role": "assistant", "content": "a" * 2000},
        {"role": "assistant", "content": "b" * 2000},
        {"role": "user", "content": "kept"},
        {"role": "assistant", "content": "kept"},
    ]
    context = assemble_context(ContextInputs(SYSTEM, "m", history=history), 1000)
    assert context.omitted_messages == 3
    assert context.messages[1]["content"] == "kept"


def file_history(*later_files: str) -> list[dict]:
    return [
        {"role": "user", "content": "u" * 2000, "files": ["policy.txt"]},
        {"role": "assistant", "content": "a" * 2000},
        {"role": "user", "content": "kept", "files": list(later_files)},
        {"role": "assistant", "content": "kept"},
    ]


def test_a_trimmed_turn_names_the_file_it_carried() -> None:
    context = assemble_context(ContextInputs(SYSTEM, "m", history=file_history()), 1000)
    assert context.omitted_messages == 2
    codes = [item["code"] for item in context.notices]
    assert codes == ["history_trimmed", "attachment_left_out"]
    assert "policy.txt" in context.notices[1]["message"]
    assert all(set(item) == {"role", "content"} for item in context.messages)


def test_a_file_still_sent_is_not_called_left_out() -> None:
    later = assemble_context(ContextInputs(SYSTEM, "m", history=file_history("policy.txt")), 1000)
    attached = Attachment("policy.txt", "text/plain", "body")
    current = assemble_context(ContextInputs(SYSTEM, "m", [attached], history=file_history()), 1000)
    for context in (later, current):
        assert context.omitted_messages == 2
        assert [item["code"] for item in context.notices] == ["history_trimmed"]


def test_consecutive_user_rows_trim_cleanly() -> None:
    history = [{"role": "user", "content": "u" * 2000} for _ in range(5)]
    context = assemble_context(ContextInputs(SYSTEM, "m", history=history), 1000)
    assert context.estimated_tokens <= 700


def test_history_trimmed_notice_on_the_wire(make, fake) -> None:
    gateway = make()
    conversation_id = gateway.post_chat(message="x" * 14000).conversation_id
    for _ in range(2):
        result = gateway.post_chat(message="y" * 14000, conversation_id=conversation_id)
    start = result.data("start")
    assert start["context"]["omitted_messages"] > 0
    assert "history_trimmed" in [item["code"] for item in start["notices"]]
    assert start["context"]["estimated_tokens"] <= start["context"]["budget_tokens"]
