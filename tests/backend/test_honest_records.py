"""What the gateway says and stores has to match what the model received, and what is
left has to match what the person did: delete, stop, or try again."""

from __future__ import annotations

import asyncio
import json
import re
import socket
import sqlite3
import threading
from pathlib import Path

import pytest

from app.models.domain import Attachment
from app.services.context import ContextInputs, assemble_context
from app.services.store import SqliteConversationStore
from harness import chat_chunk, final_chunk, wait_until


def overflow(reported: int) -> dict:
    return {"error": f"request ({reported} tokens) exceeds the available context size (8192 tokens)"}


def percent(notice: dict) -> int:
    return int(re.search(r"about (\d+)%", notice["message"]).group(1))


def file_notices(notices: list[dict]) -> list[dict]:
    return [item for item in notices if item["code"] == "attachment_truncated"]


def attached_text(fake, index: int) -> str:
    content = fake.chats[index]["messages"][-1]["content"]
    return content.split(">>>\n", 1)[1].split("\n<<<END OF ATTACHED FILE", 1)[0]


# The provider refuses the prompt, and the gateway retries with less.


def test_retry_corrects_the_notice_and_the_stored_file(make, fake) -> None:
    fake.chat_script.append((400, overflow(9400)))
    gateway = make()
    text = "1234,5678,9012\n" * 2800
    result = gateway.post_chat(
        message="Analyze this", attachments=[{"name": "data.csv", "text": text}]
    )
    assert result.data("done")

    first = file_notices(result.data("start")["notices"])
    corrected = file_notices(result.data("notices")["notices"])
    assert percent(corrected[0]) < percent(first[0])

    assert result.names().index("notices") < result.names().index("delta")
    sent = attached_text(fake, 1)
    assert len(sent) < len(attached_text(fake, 0))
    assert f"{len(sent):,} of {len(text):,}" in corrected[0]["message"]

    stored = gateway.conversation(result.conversation_id)["messages"][0]["attachments"][0]
    assert stored["chars"] == len(sent)


def test_retry_reports_a_file_that_had_seemed_to_fit(make, fake) -> None:
    fake.chat_script.append((400, overflow(11000)))
    gateway = make()
    text = "1234,5678,9012\n" * 1000
    result = gateway.post_chat(attachments=[{"name": "data.csv", "text": text}])

    assert not file_notices(result.data("start")["notices"])
    assert file_notices(result.data("notices")["notices"])
    assert (
        gateway.conversation(result.conversation_id)["messages"][0]["attachments"][0]["chars"]
        == len(attached_text(fake, 1))
    )


def test_retry_corrects_the_sources(make, fake) -> None:
    fake.chat_script.append((400, overflow(9400)))
    gateway = make()
    result = gateway.post_chat(message="remote work eligibility", use_documents=True)

    first, final = result.all("citations")
    sent = fake.chats[1]["messages"][-1]["content"].count("<<<APPROVED DOCUMENT")
    assert len(first["citations"]) > sent >= 1
    assert len(final["citations"]) == sent

    reply = gateway.conversation(result.conversation_id)["messages"][-1]
    assert [item["number"] for item in reply["citations"]] == [
        item["number"] for item in final["citations"]
    ]
    assert result.names().index("citations") < result.names().index("delta")


def test_retry_that_leaves_no_room_for_documents_says_so(make, fake) -> None:
    fake.chat_script.append((400, overflow(9000)))
    gateway = make()
    result = gateway.post_chat(
        message="remote work eligibility",
        use_documents=True,
        attachments=[{"name": "data.csv", "text": "1234,5678,9012\n" * 1300}],
    )
    assert result.all("citations")[-1]["citations"] == []
    assert "APPROVED DOCUMENT" not in fake.chats[1]["messages"][-1]["content"]
    assert "documents_left_out" in [item["code"] for item in result.data("notices")["notices"]]


def test_a_retry_that_changes_nothing_sends_no_second_citations(make, fake) -> None:
    gateway = make()
    question = {"message": "remote work eligibility", "use_documents": True}
    estimated = gateway.post_chat(**question).data("start")["context"]["estimated_tokens"]
    fake.chat_script.append((400, overflow(estimated + 50)))
    result = gateway.post_chat(**question)
    assert len(fake.chats) == 3
    assert result.data("notices")["notices"] == result.data("start")["notices"]
    assert len(result.all("citations")) == 1
    assert result.data("done")


def test_retried_turn_keeps_the_notices_only_the_turn_knows(make, fake) -> None:
    # The file is what a retry can cut. A bare message has nothing to cut, so the same
    # report would end the turn with context_too_large and no second attempt.
    fake.chat_script.append((400, overflow(9400)))
    fake.tags = [{"name": "another:1", "model": "another:1"}]
    gateway = make(
        overrides={"models": {"catalog": {"ollama": {"fast": {"name": "another:1"}}}}}
    )
    result = gateway.post_chat(
        tier="standard",
        message="hello",
        attachments=[{"name": "data.csv", "text": "1234,5678,9012\n" * 1000}],
    )
    assert [item["code"] for item in result.data("start")["notices"]] == ["tier_fallback"]
    assert [item["code"] for item in result.data("notices")["notices"]] == [
        "tier_fallback",
        "attachment_truncated",
    ]


def test_a_retry_with_nothing_to_cut_ends_as_too_long(make, fake) -> None:
    fake.chat_script.append((400, overflow(9400)))
    result = make().post_chat(message="hello")
    assert result.data("error")["code"] == "context_too_large"
    assert "notices" not in result.names()
    assert len(fake.chats) == 1


# Documents that matched and did not fit.


def test_matches_with_no_room_are_not_reported_as_no_match(make) -> None:
    gateway = make()
    question = "remote work eligibility"
    alone = gateway.post_chat(message=question, use_documents=True, tier="standard")
    assert alone.all("citations")

    crowded = gateway.post_chat(
        message=question,
        use_documents=True,
        tier="standard",
        attachments=[{"name": "notes.txt", "text": "n" * 51000}],
    )
    codes = [item["code"] for item in crowded.data("start")["notices"]]
    assert "documents_none_found" not in codes
    assert "documents_left_out" in codes
    assert "matched" in next(
        item["message"]
        for item in crowded.data("start")["notices"]
        if item["code"] == "documents_left_out"
    )


# A partly read file stays on record.


def test_try_again_repeats_the_partial_read_notice(make) -> None:
    gateway = make()
    first = gateway.post_chat(attachments=[{"name": "handbook.txt", "text": "x" * 200000}])
    notice = file_notices(first.data("start")["notices"])[0]
    assert "200,000" in notice["message"]

    again = gateway.post_chat(conversation_id=first.conversation_id, regenerate=True)
    assert file_notices(again.data("start")["notices"]) == [notice]


def test_a_smaller_try_again_records_what_it_sent(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(
        tier="standard", attachments=[{"name": "handbook.txt", "text": "x" * 200000}]
    )
    again = gateway.post_chat(
        conversation_id=first.conversation_id, regenerate=True, tier="advanced"
    )
    sent = attached_text(fake, -1)
    assert len(sent) < len(attached_text(fake, 0))
    stored = gateway.conversation(first.conversation_id)["messages"][0]["attachments"][0]
    assert stored["chars"] == len(sent)
    notice = file_notices(again.data("start")["notices"])[0]["message"]
    assert f"{len(sent):,} of 200,000" in notice


def test_a_reopened_chat_keeps_the_partial_read_notice(make) -> None:
    gateway = make()
    first = gateway.post_chat(attachments=[{"name": "handbook.txt", "text": "x" * 200000}])
    user, assistant = gateway.conversation(first.conversation_id)["messages"]
    assert assistant["notices"] == file_notices(first.data("start")["notices"])
    assert user["notices"] == []


def test_a_whole_file_has_no_notice_on_try_again_or_reopen(make) -> None:
    gateway = make()
    first = gateway.post_chat(attachments=[{"name": "note.txt", "text": "short"}])
    again = gateway.post_chat(conversation_id=first.conversation_id, regenerate=True)
    assert again.data("start")["notices"] == []
    assert gateway.conversation(first.conversation_id)["messages"][-1]["notices"] == []


def test_a_reopened_fallback_notice_does_not_speak_for_the_present(make, fake) -> None:
    # A stored notice is read again later, so it says what happened, not what is.
    fake.tags = [{"name": "another:1", "model": "another:1"}]
    gateway = make(
        overrides={"models": {"catalog": {"ollama": {"fast": {"name": "another:1"}}}}}
    )
    first = gateway.post_chat(tier="standard", message="hello")
    stored = gateway.conversation(first.conversation_id)["messages"][-1]["notices"]
    assert [item["code"] for item in stored] == ["tier_fallback"]
    assert "right now" not in stored[0]["message"]
    assert "wasn't available" in stored[0]["message"]


def test_a_file_that_did_not_fit_stays_on_record() -> None:
    whole = Attachment("big.txt", "text/plain", "x" * 4000)
    inputs = ContextInputs("system", "question", attachments=[whole])
    first = assemble_context(inputs, 40)
    assert "was not read" in first.notices[0]["message"]
    assert "ATTACHED FILE" not in first.messages[-1]["content"]

    stored = first.attachments[0]
    assert (stored.name, stored.text, stored.total_chars) == ("big.txt", "", 4000)

    again = assemble_context(ContextInputs("system", "question", attachments=[stored]), 6000)
    assert "was not read" in again.notices[0]["message"]
    assert "ATTACHED FILE" not in again.messages[-1]["content"]


def test_a_second_cut_counts_from_the_whole_file() -> None:
    cut = Attachment("big.txt", "text/plain", "x" * 2000, 8000)
    context = assemble_context(ContextInputs("system", "question", attachments=[cut]), 300)
    assert "of 8,000 characters" in context.notices[0]["message"]
    assert context.attachments[0].total_chars == 8000


def test_an_empty_file_is_still_sent() -> None:
    empty = Attachment("empty.txt", "text/plain", "")
    context = assemble_context(ContextInputs("system", "q", attachments=[empty]), 6000)
    assert context.notices == []
    assert "ATTACHED FILE: empty.txt" in context.messages[-1]["content"]


def test_an_older_database_gains_the_new_column(tmp_path: Path) -> None:
    path = tmp_path / "conversations.db"
    old_database(path)

    async def read() -> list:
        store = SqliteConversationStore(path)
        await store.open()
        try:
            return await store.get_messages("u", "c")
        finally:
            await store.close()

    attachment = asyncio.run(read())[0].attachments[0]
    assert (attachment.text, attachment.total_chars) == ("body", 4)


def test_an_older_database_takes_new_rows(tmp_path: Path) -> None:
    path = tmp_path / "conversations.db"
    old_database(path)
    notice = {"code": "attachment_truncated", "message": "Only part of b.txt was read."}

    async def write_then_read() -> list:
        store = SqliteConversationStore(path)
        await store.open()
        try:
            assert await store.create_turn(
                "u",
                conversation_id="c",
                existing=True,
                title="t",
                assistant="general",
                locality="demo",
                message_id="m2",
                content="again",
                attachments=[Attachment("b.txt", "text/plain", "bo", 4)],
            )
            assert await store.finish_turn(
                "u",
                conversation_id="c",
                message_id="m3",
                content="reply",
                finish_reason="stop",
                citations=[],
                notices=[notice],
                route={},
                usage={},
                replace_after_seq=None,
            )
            return await store.get_messages("u", "c")
        finally:
            await store.close()

    first, second, reply = asyncio.run(write_then_read())
    assert (first.notices, reply.notices) == ([], [notice])
    assert (second.attachments[0].text, second.attachments[0].total_chars) == ("bo", 4)


def old_database(path: Path) -> None:
    """The schema as the first release made it."""
    connection = sqlite3.connect(path)
    connection.executescript(
        """
        CREATE TABLE conversations (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, title TEXT NOT NULL,
            assistant TEXT NOT NULL, locality TEXT NOT NULL, created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL, touched_ns INTEGER NOT NULL);
        CREATE TABLE messages (id TEXT PRIMARY KEY, conversation_id TEXT NOT NULL, seq INTEGER NOT NULL,
            role TEXT NOT NULL, content TEXT NOT NULL, finish_reason TEXT, citations TEXT, route TEXT,
            usage TEXT, created_at TEXT NOT NULL);
        CREATE TABLE attachments (message_id TEXT NOT NULL, position INTEGER NOT NULL,
            name TEXT NOT NULL, media_type TEXT NOT NULL, text TEXT NOT NULL,
            PRIMARY KEY (message_id, position));
        INSERT INTO conversations VALUES ('c', 'u', 't', 'general', 'demo', 'now', 'now', 1);
        INSERT INTO messages (id, conversation_id, seq, role, content, created_at)
            VALUES ('m', 'c', 1, 'user', 'q', 'now');
        INSERT INTO attachments VALUES ('m', 0, 'a.txt', 'text/plain', 'body');
        """
    )
    connection.close()


# A file that history no longer holds.


def test_a_file_lost_from_history_is_named(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(
        message="Summarize this document",
        attachments=[{"name": "policy.txt", "text": "x" * 60000}],
    )
    follow_up = gateway.post_chat(
        conversation_id=first.conversation_id,
        message="Compare the eligibility rules in this document with the leave rules.",
        tier="standard",
    )
    start = follow_up.data("start")
    assert start["context"]["omitted_messages"] == 2
    assert "ATTACHED FILE" not in json.dumps(fake.chats[-1]["messages"])
    notice = next(item for item in start["notices"] if item["code"] == "attachment_left_out")
    assert "policy.txt" in notice["message"]


def test_history_sent_to_the_provider_has_only_role_and_content(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(attachments=[{"name": "a.txt", "text": "body"}])
    gateway.post_chat(conversation_id=first.conversation_id, message="And then?")
    assert all(set(item) == {"role", "content"} for item in fake.chats[-1]["messages"])


# Delete, stop and try again before the turn has started.


class Paused:
    """Holds every preflight until released, as a slow Ollama would."""

    def __init__(self, gateway) -> None:
        self.reached = threading.Event()
        self.go = threading.Event()
        provider = gateway.services.turns.provider
        real = provider.preflight

        async def preflight():
            self.reached.set()
            while not self.go.is_set():
                await asyncio.sleep(0.01)
            return await real()

        provider.preflight = preflight


def close_during_begin(gateway, body: dict, paused: Paused) -> None:
    payload = json.dumps(body).encode()
    request = (
        f"POST /v1/chat/stream HTTP/1.1\r\nHost: 127.0.0.1\r\n"
        f"Authorization: Bearer {gateway.token}\r\nContent-Type: application/json\r\n"
        f"Content-Length: {len(payload)}\r\n\r\n"
    ).encode()
    with socket.create_connection(("127.0.0.1", gateway.server.port)) as client:
        client.sendall(request + payload)
        assert paused.reached.wait(5)
    # Give the server time to see the close, then let the request carry on.
    threading.Event().wait(0.2)
    paused.reached.clear()
    paused.go.set()


def settled(gateway) -> bool:
    return not gateway.services.turns._unfinished and gateway.services.slots.outstanding == 0


def test_delete_during_a_follow_up_does_not_bring_the_chat_back(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(message="CANARY first")
    paused = Paused(gateway)
    results: list = []
    worker = threading.Thread(
        target=lambda: results.append(
            gateway.post_chat(conversation_id=first.conversation_id, message="CANARY second")
        )
    )
    worker.start()
    assert paused.reached.wait(5)
    assert gateway.client.delete(f"/v1/conversations/{first.conversation_id}").status_code == 204
    paused.go.set()
    worker.join(10)

    assert results[0].status == 404
    assert gateway.conversations() == []
    assert len(fake.chats) == 1


def test_delete_during_a_try_again_does_not_run_it(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(message="first")
    paused = Paused(gateway)
    results: list = []
    worker = threading.Thread(
        target=lambda: results.append(
            gateway.post_chat(conversation_id=first.conversation_id, regenerate=True)
        )
    )
    worker.start()
    assert paused.reached.wait(5)
    assert gateway.client.delete(f"/v1/conversations/{first.conversation_id}").status_code == 204
    paused.go.set()
    worker.join(10)

    assert results[0].status == 404
    assert gateway.conversations() == []
    assert len(fake.chats) == 1


def test_delete_all_during_a_follow_up_does_not_bring_the_chat_back(make) -> None:
    gateway = make()
    first = gateway.post_chat(message="first")
    paused = Paused(gateway)
    worker = threading.Thread(
        target=lambda: gateway.post_chat(conversation_id=first.conversation_id, message="second")
    )
    worker.start()
    assert paused.reached.wait(5)
    assert gateway.client.delete("/v1/conversations").json() == {"deleted": 1}
    paused.go.set()
    worker.join(10)
    assert gateway.conversations() == []


def test_closing_before_start_stores_no_new_chat(make, fake) -> None:
    gateway = make()
    paused = Paused(gateway)
    close_during_begin(gateway, {"message": "pasted the wrong thing"}, paused)
    wait_until(lambda: settled(gateway))
    assert gateway.conversations() == []
    assert fake.chats == []


def test_closing_before_start_stores_no_follow_up(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat(message="first")
    paused = Paused(gateway)
    close_during_begin(
        gateway, {"message": "second", "conversation_id": first.conversation_id}, paused
    )
    wait_until(lambda: settled(gateway))
    assert [row["content"] for row in gateway.conversation(first.conversation_id)["messages"]] == [
        "first",
        "Hello there.",
    ]
    assert len(fake.chats) == 1

    # Try again in the terminal then resends, and the message appears once.
    paused.go.set()
    gateway.post_chat(conversation_id=first.conversation_id, message="second")
    rows = gateway.conversation(first.conversation_id)["messages"]
    assert [row["content"] for row in rows if row["role"] == "user"] == ["first", "second"]


# A blank reply is no reply.


@pytest.mark.parametrize("blank", ["\n\n", "   ", " \n\t "])
def test_a_blank_regenerate_keeps_the_answer(make, fake, blank) -> None:
    gateway = make()
    first = gateway.post_chat(message="question")
    fake.chunks = [chat_chunk(blank), final_chunk()]
    gateway.post_chat(conversation_id=first.conversation_id, regenerate=True)
    rows = gateway.conversation(first.conversation_id)["messages"]
    assert [row["content"] for row in rows] == ["question", "Hello there."]


def test_a_blank_first_reply_stores_nothing(make, fake) -> None:
    fake.chunks = [chat_chunk("\n"), final_chunk()]
    gateway = make()
    result = gateway.post_chat(message="question")
    assert [row["role"] for row in gateway.conversation(result.conversation_id)["messages"]] == [
        "user"
    ]
