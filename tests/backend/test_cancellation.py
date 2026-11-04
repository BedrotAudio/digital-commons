"""Client disconnects against real uvicorn. Each must free the slot within one second."""

from __future__ import annotations

import threading
import time
from dataclasses import replace

from harness import FAST_TUNING, ChatResult, Gateway, chat_chunk, final_chunk, parse_sse, wait_until


def read_until(chunks, wanted: str) -> str:
    """Takes the iterator, because dropping a body iterator closes the connection."""
    seen = ""
    for chunk in chunks:
        seen += chunk
        if wanted in seen:
            return seen
    raise AssertionError(f"stream ended before {wanted!r}: {seen}")


def assert_slot_freed(gateway: Gateway, began: float) -> None:
    wait_until(lambda: gateway.services.slots.outstanding == 0, timeout=1.0)
    assert time.monotonic() - began < 1.0
    assert gateway.services.slots.running == 0


def test_disconnect_while_queued(make, fake) -> None:
    fake.first_token_delay = 3
    gateway = make()
    results: list[ChatResult] = []
    holder = threading.Thread(target=lambda: results.append(gateway.post_chat(message="one")))
    holder.start()
    wait_until(lambda: len(fake.chats) == 1)

    with gateway.client.stream("POST", "/v1/chat/stream", json={"message": "two"}) as response:
        raw = read_until(chunks := response.iter_text(), "queued")
    began = time.monotonic()
    conversation_id = parse_sse(raw)[0][1]["conversation_id"]
    assert '"phase":"queued"' in raw
    wait_until(lambda: gateway.services.slots.outstanding == 1, timeout=1.0)
    assert time.monotonic() - began < 1.0

    messages = gateway.conversation(conversation_id)["messages"]
    assert [row["role"] for row in messages] == ["user"]
    assert len(fake.chats) == 1
    holder.join()
    assert gateway.services.slots.outstanding == 0


def test_disconnect_while_waiting_for_the_first_token(make, fake) -> None:
    fake.first_token_delay = 3
    gateway = make()
    with gateway.client.stream("POST", "/v1/chat/stream", json={"message": "hi"}) as response:
        raw = read_until(chunks := response.iter_text(), "event: start")
        wait_until(lambda: len(fake.chats) == 1)
    began = time.monotonic()
    conversation_id = parse_sse(raw)[0][1]["conversation_id"]
    assert_slot_freed(gateway, began)
    assert [row["role"] for row in gateway.conversation(conversation_id)["messages"]] == ["user"]


def test_disconnect_while_generating_saves_partial_text(make, fake) -> None:
    fake.chunks = [chat_chunk(f"word{index} ") for index in range(200)] + [final_chunk()]
    fake.chunk_delay = 0.05
    gateway = make()
    with gateway.client.stream("POST", "/v1/chat/stream", json={"message": "hi"}) as response:
        raw = read_until(chunks := response.iter_text(), "word3 ")
    began = time.monotonic()
    conversation_id = parse_sse(raw.rsplit("\n\n", 1)[0])[0][1]["conversation_id"]
    assert_slot_freed(gateway, began)

    wait_until(
        lambda: gateway.conversation(conversation_id)["messages"][-1]["role"] == "assistant",
        timeout=1.0,
    )
    row = gateway.conversation(conversation_id)["messages"][-1]
    assert row["finish_reason"] == "cancelled"
    assert row["content"].startswith("word0 word1 word2 word3 ")
    assert "word199" not in row["content"]
    assert row["usage"]["estimated"] is True
    assert row["usage"]["completion_tokens"] > 0
    assert row["usage"]["prompt_tokens"] > 0
    wait_until(lambda: fake.aborted >= 1, timeout=2.0)


def test_cancelled_regenerate_with_text_replaces_and_without_text_keeps(make, fake) -> None:
    gateway = make()
    first = gateway.post_chat()
    conversation_id = first.conversation_id
    original = gateway.conversation(conversation_id)["messages"][-1]

    fake.first_token_delay = 3
    body = {"conversation_id": conversation_id, "regenerate": True}
    with gateway.client.stream("POST", "/v1/chat/stream", json=body) as response:
        read_until(chunks := response.iter_text(), "event: start")
        wait_until(lambda: len(fake.chats) == 2)
    wait_until(lambda: gateway.services.slots.outstanding == 0, timeout=1.0)
    assert gateway.conversation(conversation_id)["messages"][-1] == original

    fake.first_token_delay = 0
    fake.chunks = [chat_chunk(f"new{index} ") for index in range(100)] + [final_chunk()]
    fake.chunk_delay = 0.05
    with gateway.client.stream("POST", "/v1/chat/stream", json=body) as response:
        read_until(chunks := response.iter_text(), "new2 ")
    wait_until(lambda: gateway.services.slots.outstanding == 0, timeout=1.0)
    wait_until(
        lambda: gateway.conversation(conversation_id)["messages"][-1]["finish_reason"]
        == "cancelled",
        timeout=1.0,
    )
    assert len(gateway.conversation(conversation_id)["messages"]) == 2


def test_disconnect_leaves_no_terminal_event_and_next_turn_runs(make, fake) -> None:
    fake.chunks = [chat_chunk(f"w{index} ") for index in range(50)] + [final_chunk()]
    fake.chunk_delay = 0.05
    gateway = make()
    with gateway.client.stream("POST", "/v1/chat/stream", json={"message": "hi"}) as response:
        raw = read_until(chunks := response.iter_text(), "w2 ")
    conversation_id = parse_sse(raw.rsplit("\n\n", 1)[0])[0][1]["conversation_id"]
    fake.chunks = [chat_chunk("Next"), final_chunk()]
    fake.chunk_delay = 0
    result = gateway.post_chat(message="again", conversation_id=conversation_id)
    assert result.text == "Next"


def test_pings_flow_during_a_long_first_token_wait(make, fake) -> None:
    fake.first_token_delay = 1.1
    gateway = make(tuning=replace(FAST_TUNING, ping_interval_s=0.25))
    result = gateway.post_chat()
    assert result.raw.count(": ping\n\n") >= 3
    assert result.text == "Hello there."


def test_default_ping_arrives_within_ten_seconds(make, fake) -> None:
    fake.first_token_delay = 13
    gateway = make(tuning=FAST_TUNING)
    began = time.monotonic()
    with gateway.client.stream("POST", "/v1/chat/stream", json={"message": "hi"}) as response:
        raw = read_until(chunks := response.iter_text(), ": ping")
        elapsed = time.monotonic() - began
        assert 9.5 <= elapsed < 12
    assert ": ping\n\n" in raw
    assert chunks


def test_slot_is_released_after_provider_failure(make, fake) -> None:
    fake.chat_script.append((500, {"error": "failed"}))
    gateway = make()
    gateway.post_chat()
    assert gateway.services.slots.outstanding == 0
    assert gateway.post_chat().text == "Hello there."


def test_shutdown_saves_partial_text(make, fake, tmp_path) -> None:
    import sqlite3

    fake.chunks = [chat_chunk(f"w{index} ") for index in range(200)] + [final_chunk()]
    fake.chunk_delay = 0.05
    gateway = make()
    path = gateway.data_dir / "conversations.db"
    response_ready = threading.Event()

    def stream() -> None:
        try:
            with gateway.client.stream(
                "POST", "/v1/chat/stream", json={"message": "hi"}
            ) as response:
                for chunk in response.iter_text():
                    if "w3 " in chunk:
                        response_ready.set()
        except Exception:
            pass

    thread = threading.Thread(target=stream, daemon=True)
    thread.start()
    assert response_ready.wait(5)
    make.stop(gateway)
    thread.join(5)
    connection = sqlite3.connect(path)
    rows = connection.execute(
        "SELECT finish_reason FROM messages WHERE role = 'assistant'"
    ).fetchall()
    connection.close()
    assert rows == [("cancelled",)]
