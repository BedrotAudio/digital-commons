"""Conversation endpoints, retention and storage settings."""

from __future__ import annotations

import sqlite3
import sys
from datetime import UTC, datetime, timedelta

import pytest


def test_list_shape_and_order(make) -> None:
    gateway = make()
    first = gateway.post_chat(message="first").conversation_id
    second = gateway.post_chat(message="second").conversation_id
    gateway.post_chat(message="again", conversation_id=first)
    listed = gateway.conversations()
    assert [item["id"] for item in listed] == [first, second]
    assert set(listed[0]) == {
        "id",
        "title",
        "assistant",
        "locality",
        "created_at",
        "updated_at",
        "message_count",
    }
    assert listed[0]["message_count"] == 4
    assert listed[0]["created_at"].endswith("Z")


def test_list_limit(make) -> None:
    gateway = make()
    for index in range(3):
        gateway.post_chat(message=f"m{index}")
    assert len(gateway.client.get("/v1/conversations", params={"limit": 2}).json()["conversations"]) == 2


def test_assistant_is_the_latest_turns_value(make) -> None:
    gateway = make()
    conversation_id = gateway.post_chat().conversation_id
    gateway.post_chat(message="two", conversation_id=conversation_id, assistant="code")
    assert gateway.conversation(conversation_id)["assistant"] == "code"


def test_detail_shape(make) -> None:
    gateway = make()
    result = gateway.post_chat(attachments=[{"name": "a.txt", "text": "file words"}])
    body = gateway.conversation(result.conversation_id)
    assert {"id", "title", "assistant", "locality", "messages"} <= set(body)
    user, assistant = body["messages"]
    assert set(user) == {
        "id",
        "role",
        "content",
        "created_at",
        "attachments",
        "citations",
        "notices",
        "route",
        "usage",
        "finish_reason",
    }
    assert user["notices"] == [] and assistant["notices"] == []
    assert user["attachments"] == [{"name": "a.txt", "media_type": "text/plain", "chars": 10}]
    assert "file words" not in str(body)
    assert user["finish_reason"] is None and user["route"] is None
    assert assistant["usage"]["total_tokens"] == 27
    assert assistant["route"]["tier"] in ("fast", "standard")


def test_delete_one_and_all(make) -> None:
    gateway = make()
    first = gateway.post_chat().conversation_id
    gateway.post_chat()
    gateway.post_chat()
    response = gateway.client.delete(f"/v1/conversations/{first}")
    assert response.status_code == 204
    assert response.content == b""
    assert gateway.client.delete(f"/v1/conversations/{first}").status_code == 404
    assert gateway.client.delete("/v1/conversations").json() == {"deleted": 2}
    assert gateway.conversations() == []


def test_retention_removes_old_conversations_at_startup(make, tmp_path) -> None:
    data_dir = tmp_path / "shared"
    first = make(data_dir=data_dir)
    old = first.post_chat(message="old").conversation_id
    recent = first.post_chat(message="recent").conversation_id
    make.stop(first)

    stamp = (datetime.now(UTC) - timedelta(days=31)).strftime("%Y-%m-%dT%H:%M:%SZ")
    connection = sqlite3.connect(data_dir / "conversations.db")
    connection.execute("UPDATE conversations SET updated_at = ? WHERE id = ?", (stamp, old))
    connection.commit()
    connection.close()

    second = make(data_dir=data_dir)
    assert [item["id"] for item in second.conversations()] == [recent]


def test_retention_zero_keeps_everything(make, tmp_path) -> None:
    data_dir = tmp_path / "shared"
    first = make(data_dir=data_dir)
    old = first.post_chat(message="old").conversation_id
    make.stop(first)
    stamp = (datetime.now(UTC) - timedelta(days=4000)).strftime("%Y-%m-%dT%H:%M:%SZ")
    connection = sqlite3.connect(data_dir / "conversations.db")
    connection.execute("UPDATE conversations SET updated_at = ? WHERE id = ?", (stamp, old))
    connection.commit()
    connection.close()
    second = make(data_dir=data_dir, overrides={"conversations": {"retention_days": 0}})
    assert len(second.conversations()) == 1


def test_connection_pragmas(make) -> None:
    from app.services.store import SqliteConversationStore

    gateway = make()
    connection = SqliteConversationStore.connect(gateway.data_dir / "conversations.db")
    assert connection.execute("PRAGMA journal_mode").fetchone()[0] == "wal"
    assert connection.execute("PRAGMA secure_delete").fetchone()[0] == 1
    assert connection.execute("PRAGMA foreign_keys").fetchone()[0] == 1
    connection.close()


def test_bootstrap_shape(make) -> None:
    gateway = make()
    body = gateway.client.get("/v1/bootstrap").json()
    assert body["application"] == {
        "name": "Digital Commons",
        "version": "0.1.0",
        "stage": "beta",
        "notice": "Beta software. Not approved for production use or sensitive data.",
    }
    assert body["identity"] == {
        "user_id": "local.developer",
        "display_name": "Local Developer",
        "groups": ["all-staff", "finance"],
        "auth_mode": "local",
    }
    assert body["permissions"]["allowed_tiers"] == ["fast", "standard", "advanced"]
    assert body["locality"]["kind"] == "this_device"
    assert body["limits"] == {
        "max_message_chars": 15000,
        "max_attachments": 4,
        "max_attachment_bytes": 262144,
        "attachment_extensions": [".txt", ".md", ".csv", ".tsv"],
    }
    assert {item["action"] for item in body["suggestions"]} == {
        "attach",
        "attach_two",
        "prompt",
        "documents",
    }


def test_local_token_file_is_used(make) -> None:
    gateway = make()
    assert (gateway.data_dir / "local-token").is_file()
    assert gateway.client.get("/v1/bootstrap").status_code == 200


def test_shared_token_mode(make) -> None:
    token = "s" * 40
    gateway = make(
        overrides={"identity": {"mode": "shared_token"}},
        env={"DC_GATEWAY_TOKEN": token},
        token=token,
    )
    assert gateway.client.get("/v1/bootstrap").json()["identity"]["auth_mode"] == "shared_token"
    assert not (gateway.data_dir / "local-token").exists()


@pytest.mark.skipif(sys.platform == "win32", reason="POSIX modes")
def test_token_file_mode_on_posix(make) -> None:
    gateway = make()
    assert (gateway.data_dir / "local-token").stat().st_mode & 0o777 == 0o600
