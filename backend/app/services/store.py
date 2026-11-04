"""Conversation storage: a protocol, and SQLite behind it."""

from __future__ import annotations

import asyncio
import json
import secrets
import sqlite3
import time
from collections.abc import Callable, Sequence
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from pathlib import Path
from typing import Protocol, TypeVar

from app.models.domain import Attachment, ConversationRecord, MessageRecord

DATABASE_FILE = "conversations.db"
T = TypeVar("T")

_SCHEMA = """
CREATE TABLE IF NOT EXISTS conversations (
    id TEXT PRIMARY KEY,
    user_id TEXT NOT NULL,
    title TEXT NOT NULL,
    assistant TEXT NOT NULL,
    locality TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    touched_ns INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS conversations_by_user ON conversations (user_id, touched_ns);
CREATE TABLE IF NOT EXISTS messages (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES conversations (id) ON DELETE CASCADE,
    seq INTEGER NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content TEXT NOT NULL,
    finish_reason TEXT,
    citations TEXT,
    route TEXT,
    usage TEXT,
    created_at TEXT NOT NULL,
    notices TEXT,
    UNIQUE (conversation_id, seq)
);
CREATE TABLE IF NOT EXISTS attachments (
    message_id TEXT NOT NULL REFERENCES messages (id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name TEXT NOT NULL,
    media_type TEXT NOT NULL,
    text TEXT NOT NULL,
    total_chars INTEGER,
    PRIMARY KEY (message_id, position)
);
"""

# Columns added after the first release, as (table, column, type). A database made
# before them gains them on open. NULL means "not recorded": no notices, and a file
# whose stored text is the whole file.
_ADDED_COLUMNS = (
    ("messages", "notices", "TEXT"),
    ("attachments", "total_chars", "INTEGER"),
)


def _migrate(connection: sqlite3.Connection) -> None:
    for table, column, kind in _ADDED_COLUMNS:
        present = {row["name"] for row in connection.execute(f"PRAGMA table_info({table})")}
        if column in present:
            continue
        try:
            connection.execute(f"ALTER TABLE {table} ADD COLUMN {column} {kind}")
        except sqlite3.OperationalError as error:
            # Another process opening the same file may have added it first.
            if "duplicate column" not in str(error):
                raise


def new_id() -> str:
    """128 bits of randomness."""
    return secrets.token_hex(16)


def utc_now() -> str:
    return datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%SZ")


class ConversationStore(Protocol):
    """Every method takes user_id, so no caller can reach across users by accident."""

    async def get_conversation(
        self, user_id: str, conversation_id: str
    ) -> ConversationRecord | None: ...

    async def list_conversations(self, user_id: str, limit: int) -> list[ConversationRecord]: ...

    async def get_messages(self, user_id: str, conversation_id: str) -> list[MessageRecord]: ...

    async def create_turn(
        self,
        user_id: str,
        *,
        conversation_id: str,
        existing: bool,
        title: str,
        assistant: str,
        locality: str,
        message_id: str,
        content: str,
        attachments: Sequence[Attachment],
    ) -> bool: ...

    async def note_assistant(self, user_id: str, conversation_id: str, assistant: str) -> bool: ...

    async def replace_attachments(
        self, user_id: str, message_id: str, attachments: Sequence[Attachment]
    ) -> None: ...

    async def finish_turn(
        self,
        user_id: str,
        *,
        conversation_id: str,
        message_id: str,
        content: str,
        finish_reason: str,
        citations: Sequence[dict],
        notices: Sequence[dict],
        route: dict,
        usage: dict,
        replace_after_seq: int | None,
    ) -> bool: ...

    async def delete_conversation(self, user_id: str, conversation_id: str) -> bool: ...

    async def delete_all(self, user_id: str) -> int: ...


class SqliteConversationStore:
    def __init__(self, path: Path) -> None:
        self._path = path
        self._executor = ThreadPoolExecutor(max_workers=1, thread_name_prefix="dc-store")
        self._connection: sqlite3.Connection | None = None

    @staticmethod
    def connect(path: Path) -> sqlite3.Connection:
        """secure_delete is per connection, so this is the only way a connection is made."""
        connection = sqlite3.connect(
            path, isolation_level=None, check_same_thread=False, timeout=5.0
        )
        connection.row_factory = sqlite3.Row
        connection.execute("PRAGMA journal_mode=WAL").fetchall()
        connection.execute("PRAGMA secure_delete=ON").fetchall()
        connection.execute("PRAGMA foreign_keys=ON")
        connection.execute("PRAGMA synchronous=NORMAL")
        # Keeps VACUUM's working copy of every conversation out of the system temp folder.
        connection.execute("PRAGMA temp_store=MEMORY")
        return connection

    async def _run(self, operation: Callable[[sqlite3.Connection], T]) -> T:
        def call() -> T:
            assert self._connection is not None
            return operation(self._connection)

        return await asyncio.get_running_loop().run_in_executor(self._executor, call)

    async def open(self) -> None:
        def start() -> None:
            self._connection = self.connect(self._path)
            self._connection.executescript(_SCHEMA)
            _migrate(self._connection)

        await asyncio.get_running_loop().run_in_executor(self._executor, start)

    async def close(self) -> None:
        def stop() -> None:
            if self._connection is not None:
                self._connection.close()
                self._connection = None

        await asyncio.get_running_loop().run_in_executor(self._executor, stop)
        self._executor.shutdown(wait=True)

    @staticmethod
    def _transaction(connection: sqlite3.Connection, body: Callable[[], T]) -> T:
        connection.execute("BEGIN IMMEDIATE")
        try:
            result = body()
        except BaseException:
            connection.execute("ROLLBACK")
            raise
        connection.execute("COMMIT")
        return result

    @staticmethod
    def _scrub(connection: sqlite3.Connection) -> None:
        """Remove deleted text from the files.

        secure_delete zeroes a deleted cell, but b-tree rebalancing leaves older copies
        of moved cells in page gaps, so VACUUM rewrites every page first.
        """
        connection.execute("VACUUM")
        connection.execute("PRAGMA wal_checkpoint(TRUNCATE)").fetchall()

    @staticmethod
    def _record(row: sqlite3.Row) -> ConversationRecord:
        return ConversationRecord(
            id=row["id"],
            title=row["title"],
            assistant=row["assistant"],
            locality=row["locality"],
            created_at=row["created_at"],
            updated_at=row["updated_at"],
            message_count=row["message_count"],
        )

    async def get_conversation(
        self, user_id: str, conversation_id: str
    ) -> ConversationRecord | None:
        def read(connection: sqlite3.Connection) -> ConversationRecord | None:
            row = connection.execute(
                "SELECT c.*, (SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id)"
                " AS message_count FROM conversations c WHERE c.id = ? AND c.user_id = ?",
                (conversation_id, user_id),
            ).fetchone()
            return self._record(row) if row else None

        return await self._run(read)

    async def list_conversations(self, user_id: str, limit: int) -> list[ConversationRecord]:
        def read(connection: sqlite3.Connection) -> list[ConversationRecord]:
            rows = connection.execute(
                "SELECT c.*, (SELECT COUNT(*) FROM messages m WHERE m.conversation_id = c.id)"
                " AS message_count FROM conversations c WHERE c.user_id = ?"
                " ORDER BY c.touched_ns DESC LIMIT ?",
                (user_id, limit),
            ).fetchall()
            return [self._record(row) for row in rows]

        return await self._run(read)

    async def get_messages(self, user_id: str, conversation_id: str) -> list[MessageRecord]:
        def read(connection: sqlite3.Connection) -> list[MessageRecord]:
            rows = connection.execute(
                "SELECT m.* FROM messages m JOIN conversations c ON c.id = m.conversation_id"
                " WHERE m.conversation_id = ? AND c.user_id = ? ORDER BY m.seq",
                (conversation_id, user_id),
            ).fetchall()
            files = connection.execute(
                "SELECT a.* FROM attachments a JOIN messages m ON m.id = a.message_id"
                " JOIN conversations c ON c.id = m.conversation_id"
                " WHERE m.conversation_id = ? AND c.user_id = ? ORDER BY a.position",
                (conversation_id, user_id),
            ).fetchall()
            by_message: dict[str, list[Attachment]] = {}
            for item in files:
                total = item["total_chars"]
                by_message.setdefault(item["message_id"], []).append(
                    Attachment(
                        item["name"],
                        item["media_type"],
                        item["text"],
                        len(item["text"]) if total is None else total,
                    )
                )
            return [
                MessageRecord(
                    id=row["id"],
                    seq=row["seq"],
                    role=row["role"],
                    content=row["content"],
                    created_at=row["created_at"],
                    finish_reason=row["finish_reason"],
                    citations=json.loads(row["citations"] or "[]"),
                    notices=json.loads(row["notices"] or "[]"),
                    route=json.loads(row["route"]) if row["route"] else None,
                    usage=json.loads(row["usage"]) if row["usage"] else None,
                    attachments=by_message.get(row["id"], []),
                )
                for row in rows
            ]

        return await self._run(read)

    @staticmethod
    def _insert_attachments(
        connection: sqlite3.Connection, message_id: str, attachments: Sequence[Attachment]
    ) -> None:
        connection.executemany(
            "INSERT INTO attachments (message_id, position, name, media_type, text, total_chars)"
            " VALUES (?, ?, ?, ?, ?, ?)",
            [
                (message_id, position, item.name, item.media_type, item.text, item.total_chars)
                for position, item in enumerate(attachments)
            ],
        )

    async def create_turn(
        self,
        user_id: str,
        *,
        conversation_id: str,
        existing: bool,
        title: str,
        assistant: str,
        locality: str,
        message_id: str,
        content: str,
        attachments: Sequence[Attachment],
    ) -> bool:
        """One transaction: the conversation if it is new, and the user message.

        False means an existing conversation was deleted meanwhile, so nothing is written
        and it does not come back.
        """
        now = utc_now()
        touched = time.time_ns()

        def write(connection: sqlite3.Connection) -> bool:
            def body() -> bool:
                exists = connection.execute(
                    "SELECT 1 FROM conversations WHERE id = ? AND user_id = ?",
                    (conversation_id, user_id),
                ).fetchone()
                if exists:
                    connection.execute(
                        "UPDATE conversations SET assistant = ?, updated_at = ?, touched_ns = ?"
                        " WHERE id = ? AND user_id = ?",
                        (assistant, now, touched, conversation_id, user_id),
                    )
                elif existing:
                    return False
                else:
                    connection.execute(
                        "INSERT INTO conversations (id, user_id, title, assistant, locality,"
                        " created_at, updated_at, touched_ns) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                        (conversation_id, user_id, title, assistant, locality, now, now, touched),
                    )
                seq = self._next_seq(connection, conversation_id)
                connection.execute(
                    "INSERT INTO messages (id, conversation_id, seq, role, content, created_at)"
                    " VALUES (?, ?, ?, 'user', ?, ?)",
                    (message_id, conversation_id, seq, content, now),
                )
                self._insert_attachments(connection, message_id, attachments)
                return True

            return self._transaction(connection, body)

        return await self._run(write)

    async def replace_attachments(
        self, user_id: str, message_id: str, attachments: Sequence[Attachment]
    ) -> None:
        """Record what a retry actually sent, in place of what the first attempt sent."""

        def write(connection: sqlite3.Connection) -> None:
            def body() -> None:
                owned = connection.execute(
                    "SELECT 1 FROM messages m JOIN conversations c ON c.id = m.conversation_id"
                    " WHERE m.id = ? AND c.user_id = ?",
                    (message_id, user_id),
                ).fetchone()
                if not owned:
                    return
                connection.execute("DELETE FROM attachments WHERE message_id = ?", (message_id,))
                self._insert_attachments(connection, message_id, attachments)

            self._transaction(connection, body)

        await self._run(write)

    @staticmethod
    def _next_seq(connection: sqlite3.Connection, conversation_id: str) -> int:
        row = connection.execute(
            "SELECT COALESCE(MAX(seq), 0) + 1 AS next FROM messages WHERE conversation_id = ?",
            (conversation_id,),
        ).fetchone()
        return row["next"]

    async def note_assistant(self, user_id: str, conversation_id: str, assistant: str) -> bool:
        """False means the conversation is gone."""

        def write(connection: sqlite3.Connection) -> bool:
            cursor = connection.execute(
                "UPDATE conversations SET assistant = ? WHERE id = ? AND user_id = ?",
                (assistant, conversation_id, user_id),
            )
            return cursor.rowcount > 0

        return await self._run(write)

    async def finish_turn(
        self,
        user_id: str,
        *,
        conversation_id: str,
        message_id: str,
        content: str,
        finish_reason: str,
        citations: Sequence[dict],
        notices: Sequence[dict],
        route: dict,
        usage: dict,
        replace_after_seq: int | None,
    ) -> bool:
        """Write the assistant row. False means the conversation is gone, so it is discarded."""
        now = utc_now()
        touched = time.time_ns()

        def write(connection: sqlite3.Connection) -> bool:
            def body() -> bool:
                exists = connection.execute(
                    "SELECT 1 FROM conversations WHERE id = ? AND user_id = ?",
                    (conversation_id, user_id),
                ).fetchone()
                if not exists:
                    return False
                if replace_after_seq is not None:
                    connection.execute(
                        "DELETE FROM messages WHERE conversation_id = ? AND role = 'assistant'"
                        " AND seq > ?",
                        (conversation_id, replace_after_seq),
                    )
                seq = self._next_seq(connection, conversation_id)
                connection.execute(
                    "INSERT INTO messages (id, conversation_id, seq, role, content, finish_reason,"
                    " citations, notices, route, usage, created_at)"
                    " VALUES (?, ?, ?, 'assistant', ?, ?, ?, ?, ?, ?, ?)",
                    (
                        message_id,
                        conversation_id,
                        seq,
                        content,
                        finish_reason,
                        json.dumps(list(citations)),
                        json.dumps(list(notices)),
                        json.dumps(route),
                        json.dumps(usage),
                        now,
                    ),
                )
                connection.execute(
                    "UPDATE conversations SET updated_at = ?, touched_ns = ? WHERE id = ?",
                    (now, touched, conversation_id),
                )
                return True

            return self._transaction(connection, body)

        return await self._run(write)

    async def delete_conversation(self, user_id: str, conversation_id: str) -> bool:
        def write(connection: sqlite3.Connection) -> bool:
            cursor = connection.execute(
                "DELETE FROM conversations WHERE id = ? AND user_id = ?",
                (conversation_id, user_id),
            )
            self._scrub(connection)
            return cursor.rowcount > 0

        return await self._run(write)

    async def delete_all(self, user_id: str) -> int:
        def write(connection: sqlite3.Connection) -> int:
            cursor = connection.execute(
                "DELETE FROM conversations WHERE user_id = ?", (user_id,)
            )
            self._scrub(connection)
            return cursor.rowcount

        return await self._run(write)

    async def purge_expired(self, retention_days: int) -> int:
        """Retention spans all users, so it is maintenance and not part of the protocol."""
        if retention_days <= 0:
            return 0
        cutoff = (datetime.now(UTC) - timedelta(days=retention_days)).strftime(
            "%Y-%m-%dT%H:%M:%SZ"
        )

        def write(connection: sqlite3.Connection) -> int:
            cursor = connection.execute(
                "DELETE FROM conversations WHERE updated_at < ?", (cutoff,)
            )
            self._scrub(connection)
            return cursor.rowcount

        return await self._run(write)
