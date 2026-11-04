"""Canary strings stay out of logs, and leave the disk files on delete."""

from __future__ import annotations

import asyncio
import logging
import os
import random
import subprocess
import sys
import time
from pathlib import Path

import httpx
import pytest

from app.config.settings import ROOT
from app.models.domain import Attachment
from app.services.store import SqliteConversationStore, new_id
from harness import chat_chunk, final_chunk, wait_until

CANARY = "CANARYxq7Zt9"


def free_port() -> int:
    import socket

    with socket.socket() as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


def exercise(client: httpx.Client) -> str:
    """Put the canary everywhere a person could put text, valid or not."""
    body = {
        "message": f"First line {CANARY} message",
        "attachments": [
            {
                "name": f"{CANARY}-file.txt",
                "media_type": "text/plain",
                "text": f"file text {CANARY}",
            }
        ],
    }
    with client.stream("POST", "/v1/chat/stream", json=body) as response:
        raw = response.read().decode("utf-8")
    assert response.status_code == 200
    conversation_id = raw.split('"conversation_id":"')[1].split('"')[0]
    client.get(f"/v1/conversations/{conversation_id}")
    client.get(f"/v1/conversations/{CANARY}.bad")
    client.get(f"/v1/{CANARY}")
    client.post("/v1/chat/stream", json={"message": 5, CANARY: CANARY})
    client.post("/v1/chat/stream", json={"tier": CANARY})
    client.post(
        "/v1/chat/stream",
        content=f'{{"message": "{CANARY}'.encode(),
        headers={"Content-Type": "application/json"},
    )
    client.post("/v1/chat/stream", json={"attachments": [{"name": f"{CANARY}.exe", "text": "x"}]})
    client.post(
        "/v1/chat/stream", json={"attachments": [{"name": f"{CANARY}/a.txt", "text": CANARY}]}
    )
    client.post("/v1/chat/stream", json={"conversation_id": CANARY})
    client.request("PUT", f"/v1/{CANARY}")
    return conversation_id


def test_canary_reaches_no_log_output_of_a_real_process(tmp_path) -> None:
    config = tmp_path / "gateway.yaml"
    base = (ROOT / "config" / "development.yaml").read_text(encoding="utf-8")
    config.write_text(base.replace("level: INFO", "level: DEBUG"), encoding="utf-8")
    port = free_port()
    data_dir = tmp_path / "data"
    env = {
        **os.environ,
        "DC_CONFIG": str(config),
        "DC_DATA_DIR": str(data_dir),
        "DC_PROVIDER": "mock",
        "PYTHONUNBUFFERED": "1",
    }
    env.pop("DC_MODELS_CONFIG", None)
    output_path = tmp_path / "gateway.log"
    with output_path.open("wb") as output:
        process = subprocess.Popen(
            [sys.executable, "-m", "app", "--port", str(port)],
            cwd=ROOT / "backend",
            env=env,
            stdout=output,
            stderr=subprocess.STDOUT,
            stdin=subprocess.DEVNULL,
        )
        try:
            base_url = f"http://127.0.0.1:{port}"

            def healthy() -> bool:
                try:
                    return httpx.get(f"{base_url}/healthz", trust_env=False).status_code == 200
                except httpx.HTTPError:
                    return False

            wait_until(healthy, timeout=20, step=0.1)
            token = (data_dir / "local-token").read_text().strip()
            with httpx.Client(
                base_url=base_url,
                trust_env=False,
                timeout=60,
                headers={"Authorization": f"Bearer {token}"},
            ) as client:
                exercise(client)
        finally:
            process.terminate()
            process.wait(timeout=15)
    log = output_path.read_bytes()
    assert CANARY.encode() not in log
    assert b"/v1/chat/stream" in log, "the request log must be present for this test to mean anything"
    assert b"route=/v1/conversations/{conversation_id}" in log
    assert b"validation failed" in log


def test_canary_reaches_no_log_through_the_ollama_path(make, fake, caplog) -> None:
    fake.chunks = [chat_chunk(f"reply {CANARY}"), final_chunk()]
    with caplog.at_level(logging.DEBUG):
        gateway = make()
        exercise(gateway.client)
        fake.chat_script.append((500, {"error": CANARY}))
        gateway.post_chat(message=CANARY)
        fake.chunks = [chat_chunk(CANARY), {"error": CANARY}]
        gateway.post_chat(message=CANARY)
    # The test's own client logs through httpx in this process. The real process above
    # has that logger silenced, and proves it.
    records = "\n".join(
        f"{record.name} {record.getMessage()} {record.exc_text or ''}"
        for record in caplog.records
        if not record.name.startswith(("httpx", "httpcore"))
    )
    assert CANARY not in records
    assert "route=/v1/chat/stream" in records


def test_request_log_has_only_the_listed_fields(make, caplog) -> None:
    gateway = make(provider="mock")
    with caplog.at_level(logging.INFO, logger="digital_commons.request"):
        gateway.client.get("/healthz")
        wait_until(lambda: any("route=/healthz" in r.getMessage() for r in caplog.records))
    line = next(r.getMessage() for r in caplog.records if "route=/healthz" in r.getMessage())
    assert line.startswith("request method=GET route=/healthz status=200 duration_ms=")
    assert "request_id=" in line


def test_unhandled_exception_text_is_not_logged(make, caplog, monkeypatch) -> None:
    gateway = make(provider="mock")

    async def boom(*args, **kwargs):
        raise RuntimeError(CANARY)

    monkeypatch.setattr(gateway.services.store, "list_conversations", boom)
    with caplog.at_level(logging.DEBUG):
        assert gateway.client.get("/v1/conversations").status_code == 500
    assert CANARY not in caplog.text
    assert "RuntimeError" in caplog.text


def db_bytes(data_dir: Path) -> bytes:
    return b"".join(
        path.read_bytes()
        for path in data_dir.iterdir()
        if path.name.startswith("conversations.db")
    )


@pytest.mark.parametrize("delete_all", [False, True])
def test_canary_bytes_leave_the_database_files_on_delete(make, fake, delete_all) -> None:
    fake.chunks = [chat_chunk(f"reply {CANARY}"), final_chunk()]
    gateway = make()
    result = gateway.post_chat(
        message=f"message {CANARY}",
        attachments=[{"name": f"{CANARY}.txt", "text": f"attached {CANARY} " * 2000}],
    )
    fake.chunks = [chat_chunk("unrelated reply"), final_chunk()]
    keeper = gateway.post_chat(message="unrelated")
    assert CANARY.encode() in db_bytes(gateway.data_dir), "the control must find it first"

    if delete_all:
        gateway.client.delete("/v1/conversations")
    else:
        gateway.client.delete(f"/v1/conversations/{result.conversation_id}")
        assert [item["id"] for item in gateway.conversations()] == [keeper.conversation_id]

    assert CANARY.encode() not in db_bytes(gateway.data_dir)
    wal = gateway.data_dir / "conversations.db-wal"
    assert not wal.exists() or wal.stat().st_size == 0


@pytest.mark.parametrize("seed", [2, 3, 5, 12])
def test_deleted_text_is_gone_after_rows_have_moved_between_pages(tmp_path, seed) -> None:
    """These sizes and seeds leave stale cells in page gaps unless the store runs VACUUM."""
    marker = "CANARY"  # The same length as "keeper", so both kinds of row lay out alike.

    async def scenario() -> None:
        store = SqliteConversationStore(tmp_path / "conversations.db")
        await store.open()
        rng = random.Random(seed)
        conversations = []
        for index in range(120):
            doomed = index % 2 == 0
            tag = marker if doomed else "keeper"
            size = rng.choice([50, 300, 2000, 9000])
            conversation_id = new_id()
            conversations.append((conversation_id, doomed))
            await store.create_turn(
                "u",
                conversation_id=conversation_id,
                existing=False,
                title=f"title {tag} {index}",
                assistant="general",
                locality="demo",
                message_id=new_id(),
                content=f"message {tag} " * (size // 12),
                attachments=[Attachment(f"{tag}.txt", "text/plain", f"file {tag} " * (size // 8))]
                if index % 3 == 0
                else [],
            )
            await store.finish_turn(
                "u",
                conversation_id=conversation_id,
                message_id=new_id(),
                content=f"reply {tag} " * (size // 10),
                finish_reason="stop",
                citations=[],
                notices=[],
                route={},
                usage={},
                replace_after_seq=None,
            )
        rng.shuffle(conversations)
        for conversation_id, doomed in conversations:
            if doomed:
                await store.delete_conversation("u", conversation_id)
        await store.close()

    asyncio.run(scenario())
    assert marker.encode() not in db_bytes(tmp_path)
    assert b"keeper" in db_bytes(tmp_path)


def test_regenerate_leaves_no_copy_of_the_replaced_answer(make, fake) -> None:
    fake.chunks = [chat_chunk(f"old {CANARY}"), final_chunk()]
    gateway = make()
    conversation_id = gateway.post_chat().conversation_id
    fake.chunks = [chat_chunk("new"), final_chunk()]
    gateway.post_chat(conversation_id=conversation_id, regenerate=True)
    other = gateway.post_chat().conversation_id
    gateway.client.delete(f"/v1/conversations/{other}")
    assert CANARY.encode() not in db_bytes(gateway.data_dir)


def test_retention_purge_truncates_the_wal(make, tmp_path) -> None:
    import sqlite3
    from datetime import UTC, datetime, timedelta

    data_dir = tmp_path / "shared"
    first = make(data_dir=data_dir)
    old = first.post_chat(message=f"old {CANARY}").conversation_id
    make.stop(first)
    stamp = (datetime.now(UTC) - timedelta(days=90)).strftime("%Y-%m-%dT%H:%M:%SZ")
    connection = sqlite3.connect(data_dir / "conversations.db")
    connection.execute("UPDATE conversations SET updated_at = ? WHERE id = ?", (stamp, old))
    connection.commit()
    connection.close()
    make(data_dir=data_dir)
    assert CANARY.encode() not in db_bytes(data_dir)


def test_slow_stream_time_is_logged_once_it_ends(make, fake, caplog) -> None:
    fake.chunk_delay = 0.05
    with caplog.at_level(logging.INFO, logger="digital_commons.request"):
        gateway = make()
        began = time.monotonic()
        gateway.post_chat()
        assert time.monotonic() - began >= 0.1
        wait_until(lambda: "route=/v1/chat/stream" in caplog.text)
