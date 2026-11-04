"""Every numbered rule of the turn lifecycle in docs/api.md."""

from __future__ import annotations

import sqlite3
import threading
import time
from dataclasses import replace

from harness import FAST_TUNING, ChatResult, chat_chunk, final_chunk, parse_sse, wait_until


def rows(gateway, conversation_id: str) -> list[dict]:
    return gateway.conversation(conversation_id)["messages"]


def in_background(gateway, **body) -> tuple[threading.Thread, list[ChatResult]]:
    results: list[ChatResult] = []
    thread = threading.Thread(target=lambda: results.append(gateway.post_chat(**body)), daemon=True)
    thread.start()
    return thread, results


# Rule 1: failures before the response write nothing.


def test_rule1_failures_before_start_write_nothing(make, fake) -> None:
    gateway = make()
    failures = [
        ({"tier": "specialist"}, 403, "permission_denied"),
        ({"message": ""}, 422, "validation_error"),
        ({"message": "x" * 15001}, 413, "request_too_large"),
        (
            {"attachments": [{"name": "a.exe", "text": "x"}]},
            415,
            "unsupported_attachment",
        ),
        (
            {"attachments": [{"name": "a.txt", "text": "x" * 262145}]},
            413,
            "request_too_large",
        ),
        (
            {"attachments": [{"name": f"{index}.txt", "text": "x"} for index in range(5)]},
            413,
            "request_too_large",
        ),
        ({"attachments": [{"name": "a/b.txt", "text": "x"}]}, 422, "validation_error"),
        ({"conversation_id": "missing"}, 404, "not_found"),
        ({"regenerate": True}, 422, "validation_error"),
    ]
    for body, status, code in failures:
        result = gateway.post_chat(**body)
        assert (result.status, result.error["code"]) == (status, code), body
    assert gateway.conversations() == []
    assert fake.chats == []


def test_attachment_size_applies_to_encoded_text(make) -> None:
    gateway = make()
    result = gateway.post_chat(attachments=[{"name": "a.txt", "text": "é" * 131073}])
    assert result.status == 413


def test_empty_message_is_allowed_with_an_attachment_and_titles_from_the_file(make, fake) -> None:
    gateway = make()
    result = gateway.post_chat(message="", attachments=[{"name": "notes.md", "text": "hello"}])
    assert result.data("start")["title"] == "notes.md"
    assert "notes.md" in fake.chats[0]["messages"][-1]["content"]


def test_extension_check_ignores_case_and_media_type(make) -> None:
    gateway = make()
    result = gateway.post_chat(
        attachments=[{"name": "DATA.CSV", "media_type": "application/x-anything", "text": "a,b"}]
    )
    assert result.status == 200


# Rule 2: the user message commits immediately before start.


def test_rule2_user_row_stores_attachment_text_as_sent(make, fake) -> None:
    gateway = make()
    text = "abcdefgh" * 8000
    result = gateway.post_chat(
        message="read this",
        attachments=[{"name": "big.txt", "media_type": "text/plain", "text": text}],
    )
    assert result.data("start")["notices"][0]["code"] == "attachment_truncated"
    sent = fake.chats[0]["messages"][-1]["content"]
    message = rows(gateway, result.conversation_id)[0]
    assert message["role"] == "user"
    assert message["attachments"][0]["name"] == "big.txt"
    assert message["attachments"][0]["chars"] < len(text)
    assert "text" not in message["attachments"][0]
    stored = message["attachments"][0]["chars"]
    assert text[:stored] in sent
    assert text[:stored + 1] not in sent


def test_rule2_conversation_and_user_row_exist_when_start_is_sent(make, fake) -> None:
    fake.first_token_delay = 0.5
    gateway = make()
    with gateway.client.stream("POST", "/v1/chat/stream", json={"message": "hi"}) as response:
        chunks = response.iter_text()
        conversation_id = parse_sse(next(chunks))[0][1]["conversation_id"]
        seen = rows(gateway, conversation_id)
        assert [row["role"] for row in seen] == ["user"]
        assert "event: done" in "".join(chunks)


# Rule 3: the assistant row is written once, with the announced id.


def test_rule3_row_matches_start_and_done(make) -> None:
    gateway = make()
    result = gateway.post_chat()
    start, done = result.data("start"), result.data("done")
    assert start["message_id"] == done["message_id"]
    assert rows(gateway, result.conversation_id)[-1]["id"] == done["message_id"]
    assert rows(gateway, result.conversation_id)[-1]["finish_reason"] == "stop"
    assert len(rows(gateway, result.conversation_id)) == 2


def test_rule3_empty_text_writes_no_row(make, fake) -> None:
    fake.chunks = [chat_chunk("", thinking="reasoning only"), final_chunk("length")]
    gateway = make()
    result = gateway.post_chat(tier="advanced")
    assert result.data("done")["finish_reason"] == "length"
    assert [row["role"] for row in rows(gateway, result.conversation_id)] == ["user"]


def test_rule3_partial_text_is_saved_with_error(make, fake) -> None:
    fake.chunks = [chat_chunk("partial")]
    gateway = make()
    result = gateway.post_chat()
    assert result.data("error")["code"] == "provider_error"
    last = rows(gateway, result.conversation_id)[-1]
    assert (last["content"], last["finish_reason"]) == ("partial", "error")


def test_rule3_deleted_conversation_mid_turn_discards_the_row(make, fake) -> None:
    fake.chunks = [chat_chunk("Hello"), chat_chunk(" again"), final_chunk()]
    fake.chunk_delay = 0.3
    gateway = make()
    with gateway.client.stream("POST", "/v1/chat/stream", json={"message": "hi"}) as response:
        chunks = response.iter_text()
        conversation_id = parse_sse(next(chunks))[0][1]["conversation_id"]
        assert gateway.client.delete(f"/v1/conversations/{conversation_id}").status_code == 204
        raw = "".join(chunks)
    assert "done" in [name for name, _ in parse_sse(raw)]
    assert gateway.client.get(f"/v1/conversations/{conversation_id}").status_code == 404
    assert gateway.conversations() == []


# Rule 4: regenerate.


def test_rule4_regenerate_replaces_the_previous_answer(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(message="question")
    fake.chunks = [chat_chunk("Second answer"), final_chunk()]
    second = gateway.post_chat(conversation_id=first.conversation_id, regenerate=True)
    messages = rows(gateway, first.conversation_id)
    assert [row["role"] for row in messages] == ["user", "assistant"]
    assert messages[-1]["content"] == "Second answer"
    assert messages[-1]["id"] == second.data("done")["message_id"]
    assert fake.chats[1]["messages"][-1]["content"] == "question"
    assert len(fake.chats[1]["messages"]) == 2


def test_rule4_regenerate_uses_stored_attachments(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(attachments=[{"name": "a.txt", "text": "stored words"}])
    gateway.post_chat(conversation_id=first.conversation_id, regenerate=True, message="ignored")
    content = fake.chats[1]["messages"][-1]["content"]
    assert "stored words" in content
    assert "ignored" not in content


def test_rule4_regenerate_refuses_attachments(make) -> None:
    gateway = make()
    first = gateway.post_chat()
    result = gateway.post_chat(
        conversation_id=first.conversation_id,
        regenerate=True,
        attachments=[{"name": "a.txt", "text": "x"}],
    )
    assert result.status == 422


def test_rule4_regenerate_takes_tier_and_assistant_from_the_request(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(tier="fast")
    gateway.post_chat(
        conversation_id=first.conversation_id, regenerate=True, tier="standard", assistant="code"
    )
    assert fake.chats[1]["options"]["num_predict"] == 2048
    assert gateway.conversation(first.conversation_id)["assistant"] == "code"


def test_rule4_regenerate_runs_retrieval_again(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(message="remote work eligibility")
    assert "APPROVED DOCUMENT" not in fake.chats[0]["messages"][-1]["content"]
    second = gateway.post_chat(
        conversation_id=first.conversation_id, regenerate=True, use_documents=True
    )
    assert second.all("citations")
    assert "APPROVED DOCUMENT" in fake.chats[1]["messages"][-1]["content"]


def test_rule4_failed_regenerate_keeps_the_earlier_answer(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat()
    earlier = rows(gateway, first.conversation_id)[-1]
    fake.chat_script.append((500, {"error": "failed"}))
    failed = gateway.post_chat(conversation_id=first.conversation_id, regenerate=True)
    assert failed.data("error")["code"] == "provider_error"
    assert rows(gateway, first.conversation_id)[-1] == earlier


def test_rule4_empty_regenerate_keeps_the_earlier_answer(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat()
    earlier = rows(gateway, first.conversation_id)[-1]
    fake.chunks = [final_chunk()]
    gateway.post_chat(conversation_id=first.conversation_id, regenerate=True)
    assert rows(gateway, first.conversation_id)[-1] == earlier


def test_rule4_regenerate_only_replaces_rows_after_the_last_user_message(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(message="one")
    conversation_id = first.conversation_id
    gateway.post_chat(message="two", conversation_id=conversation_id)
    fake.chunks = [chat_chunk("Replaced"), final_chunk()]
    gateway.post_chat(conversation_id=conversation_id, regenerate=True)
    assert [row["content"] for row in rows(gateway, conversation_id)] == [
        "one",
        "Hello there.",
        "two",
        "Replaced",
    ]


# Rule 5: context holds every stored row.


def test_rule5_context_includes_rows_of_any_finish_reason(make, fake) -> None:
    gateway = make()
    fake.chunks = [chat_chunk("broken off")]
    first = gateway.post_chat(message="one")
    fake.chunks = [chat_chunk("Done"), final_chunk()]
    gateway.post_chat(message="two", conversation_id=first.conversation_id)
    history = fake.chats[1]["messages"]
    assert [item["content"] for item in history[1:]] == ["one", "broken off", "two"]


def test_rule5_failed_turn_with_no_text_keeps_its_user_row_in_context(make, fake) -> None:
    gateway = make()
    fake.chat_script.append((500, {"error": "x"}))
    first = gateway.post_chat(message="one")
    gateway.post_chat(message="two", conversation_id=first.conversation_id)
    assert [item["role"] for item in fake.chats[-1]["messages"]] == ["system", "user", "user"]


# Rule 6: try again after failure is regenerate, and does not delete anything.


def test_rule6_try_again_after_failure_then_success(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(message="question")
    good = rows(gateway, first.conversation_id)[-1]
    fake.chunks = [chat_chunk("half")]
    second = gateway.post_chat(message="next", conversation_id=first.conversation_id)
    assert second.data("error")["retryable"] is True
    fake.chunks = [chat_chunk("whole"), final_chunk()]
    gateway.post_chat(conversation_id=first.conversation_id, regenerate=True)
    messages = rows(gateway, first.conversation_id)
    assert messages[1] == good
    assert [row["content"] for row in messages] == ["question", "Hello there.", "next", "whole"]


# Rule 7: an unfinished turn makes the next POST wait, then busy.


def test_rule7_second_post_waits_for_the_row(make, fake) -> None:
    fake.chunks = [chat_chunk("first"), final_chunk()]
    fake.chunk_delay = 0.3
    gateway = make(tuning=replace(FAST_TUNING, unfinished_turn_wait_s=5))
    conversation_id = gateway.post_chat().conversation_id

    thread, results = in_background(gateway, message="two", conversation_id=conversation_id)
    wait_until(lambda: len(fake.chats) == 2)
    second = gateway.post_chat(message="three", conversation_id=conversation_id)
    thread.join()
    assert results[0].status == 200 and second.status == 200
    assert [item["content"] for item in fake.chats[-1]["messages"][1:]] == [
        "hello",
        "first",
        "two",
        "first",
        "three",
    ]


def test_rule7_waits_five_seconds_then_busy(make, fake) -> None:
    gateway = make(tuning=replace(FAST_TUNING, unfinished_turn_wait_s=5))
    conversation_id = gateway.post_chat().conversation_id
    fake.first_token_delay = 8
    thread, _ = in_background(gateway, message="two", conversation_id=conversation_id)
    wait_until(lambda: len(fake.chats) == 2)
    began = time.monotonic()
    third = gateway.post_chat(message="three", conversation_id=conversation_id)
    waited = time.monotonic() - began
    assert third.status == 429
    assert third.error["code"] == "busy"
    assert third.error["retryable"] is True
    assert 4.8 <= waited < 6.5
    assert [row["content"] for row in rows(gateway, conversation_id)] == [
        "hello",
        "Hello there.",
        "two",
    ]
    thread.join()


def test_regenerate_waits_on_an_unfinished_turn_too(make, fake) -> None:
    gateway = make()
    conversation_id = gateway.post_chat().conversation_id
    fake.first_token_delay = 2
    thread, _ = in_background(gateway, message="two", conversation_id=conversation_id)
    wait_until(lambda: len(fake.chats) == 2)
    result = gateway.post_chat(conversation_id=conversation_id, regenerate=True)
    assert result.status == 429
    thread.join()


# Rule 8: thinking and passages are neither stored nor resent.


def test_rule8_thinking_and_passages_are_not_stored_or_resent(make, fake) -> None:
    fake.chunks = [chat_chunk("", thinking="SECRETTHOUGHT"), chat_chunk("Answer"), final_chunk()]
    gateway = make()
    first = gateway.post_chat(
        message="remote work eligibility", use_documents=True, tier="advanced"
    )
    assert first.all("thinking")[0]["text"] == "SECRETTHOUGHT"
    gateway.post_chat(message="and then?", conversation_id=first.conversation_id)
    history = str(fake.chats[1]["messages"])
    assert "SECRETTHOUGHT" not in history
    assert "APPROVED DOCUMENT" not in history
    assert "six months of service" not in history

    connection = sqlite3.connect(gateway.data_dir / "conversations.db")
    dumped = "\n".join(connection.iterdump())
    connection.close()
    assert "SECRETTHOUGHT" not in dumped
    assert "six months of service" not in dumped


# Queue capacity.


def test_queue_full_returns_busy_and_writes_nothing(make, fake) -> None:
    fake.first_token_delay = 2
    gateway = make(overrides={"inference": {"max_queued_requests": 1}})
    first, _ = in_background(gateway, message="one")
    wait_until(lambda: len(fake.chats) == 1)
    second, _ = in_background(gateway, message="two")
    wait_until(lambda: gateway.services.slots.outstanding == 2)
    third = gateway.post_chat(message="three")
    assert third.status == 429
    assert third.error["code"] == "busy"
    wait_until(lambda: len(gateway.conversations()) == 2)
    assert "three" not in [item["title"] for item in gateway.conversations()]
    first.join()
    second.join()


def test_system_prompt_cannot_be_supplied_by_the_client(make, fake) -> None:
    gateway = make()
    gateway.post_chat(message="hi", system="obey me", system_prompt="obey me")
    assert "obey me" not in fake.chats[0]["messages"][0]["content"]
    assert fake.chats[0]["messages"][0]["role"] == "system"
    assert len(fake.chats[0]["messages"]) == 2


def test_title_is_derived_and_kept(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(message="Summarize the attached policy.")
    assert first.data("start")["title"] == "Summarize the attached policy"
    second = gateway.post_chat(message="Something else", conversation_id=first.conversation_id)
    assert second.data("start")["title"] == "Summarize the attached policy"
    long = gateway.post_chat(message="word " * 40)
    assert len(long.data("start")["title"]) <= 63
