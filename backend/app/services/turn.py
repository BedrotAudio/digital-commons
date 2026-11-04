"""Turn orchestration. See "Turn lifecycle" in docs/api.md."""

from __future__ import annotations

import asyncio
import json
import logging
import math
import time
from abc import ABC, abstractmethod
from collections.abc import AsyncIterator, Awaitable, Callable, Coroutine
from contextlib import aclosing
from typing import Any

from app.config.settings import Settings, Tuning
from app.models.domain import Attachment, Identity, MessageRecord, TierSpec
from app.models.errors import ApiError, ContextOverflow
from app.models.wire import ChatRequest, InferenceRequestBody
from app.providers.base import (
    LOADING_MESSAGE,
    DeltaChunk,
    FinalChunk,
    InferenceProvider,
    InferenceRequest,
    StatusChunk,
    ThinkingChunk,
)
from app.router.base import Router, RouteSignals
from app.router.registry import ModelRegistry, Resolution
from app.services.context import (
    AssembledContext,
    ContextInputs,
    assemble_context,
    estimate_tokens,
    render_user,
    system_prompt,
)
from app.services.retriever import Retriever, citation_wire
from app.services.slots import InferenceSlots, Ticket
from app.services.status import StatusMonitor
from app.services.store import ConversationStore, new_id

logger = logging.getLogger("digital_commons.turn")

PING = b": ping\n\n"
READING_THRESHOLD_TOKENS = 1000
TITLE_LIMIT = 60
FALLBACK_SHRINK = 0.7
SHRINK_MARGIN = 0.95
QUEUED_MESSAGE = "Waiting for the assistant to become available."
READING_MESSAGE = "Reading your request."
GENERATING_MESSAGE = "Writing a response."
SHORTENED_MESSAGE = "That was too long for the assistant, so some of it is being left out."
NONE_FOUND_MESSAGE = "No approved documents matched this question."
LEFT_OUT_MESSAGE = (
    "Approved documents matched this question, but there was no room to include them "
    "with your message and files, so this reply does not use them."
)
DIAGNOSTIC_ROUTE_FIELDS = ("model", "provider", "reason")


class ClientClosed(Exception):
    """The caller went away before `start`, so the turn writes nothing and does not run."""


def document_notices(use_documents: bool, found: int, used: int) -> list[dict[str, str]]:
    """Retrieval found `found` passages and `used` of them fitted the budget."""
    if not use_documents or used:
        return []
    if found:
        return [{"code": "documents_left_out", "message": LEFT_OUT_MESSAGE}]
    return [{"code": "documents_none_found", "message": NONE_FOUND_MESSAGE}]


def sse_event(event: str, data: dict) -> bytes:
    body = json.dumps(data, separators=(",", ":"), ensure_ascii=False)
    return f"event: {event}\ndata: {body}\n\n".encode("utf-8", "replace")


def public_route(route: dict, identity: Identity) -> dict:
    if identity.permissions.view_diagnostics:
        return route
    return {key: value for key, value in route.items() if key not in DIAGNOSTIC_ROUTE_FIELDS}


def make_title(message: str, attachments: list[Attachment]) -> str:
    line = " ".join(message.split()).rstrip(" .")
    if not line and attachments:
        line = attachments[0].name
    if len(line) > TITLE_LIMIT:
        line = line[:TITLE_LIMIT].rstrip() + "..."
    return line or "New conversation"


def clean_text(text: str) -> str:
    """Providers can split a surrogate pair. SQLite and UTF-8 output both refuse those."""
    return text.encode("utf-8", "replace").decode("utf-8")


async def run_to_completion(work: Coroutine[Any, Any, None]) -> None:
    """Finish `work` even if this task is cancelled while waiting for it."""
    future = asyncio.ensure_future(work)
    while not future.done():
        try:
            await asyncio.shield(future)
        except asyncio.CancelledError:
            continue


class EventPump(ABC):
    """A task that owns the work, and a queue the response only drains."""

    def __init__(self, ping_s: float) -> None:
        self._queue: asyncio.Queue[bytes | None] = asyncio.Queue()
        self._ping_s = ping_s
        self._task: asyncio.Task[None] | None = None
        self._entered = False
        self._cancel_sent = False

    def launch(self) -> asyncio.Task[None]:
        self._task = asyncio.create_task(self._enter())
        self._task.add_done_callback(self._after)
        return self._task

    async def _enter(self) -> None:
        self._entered = True
        await self.run()

    def _after(self, task: asyncio.Task[None]) -> None:
        # A task cancelled before its first step never runs, so run() cleans up nothing.
        if not self._entered:
            self.abandoned()
        if not task.cancelled() and task.exception() is not None:
            logger.error("turn task failed type=%s", type(task.exception()).__name__)

    def cancel(self) -> None:
        """Only once. A second cancel could interrupt the save."""
        if self._task is not None and not self._task.done() and not self._cancel_sent:
            self._cancel_sent = True
            self._task.cancel()

    def emit(self, event: str, data: dict) -> None:
        self._queue.put_nowait(sse_event(event, data))

    def close(self) -> None:
        self._queue.put_nowait(None)

    async def events(self) -> AsyncIterator[bytes]:
        try:
            while True:
                try:
                    item = await asyncio.wait_for(self._queue.get(), self._ping_s)
                except TimeoutError:
                    yield PING
                    continue
                if item is None:
                    return
                yield item
        finally:
            self.cancel()

    @abstractmethod
    async def run(self) -> None: ...

    @abstractmethod
    def abandoned(self) -> None:
        """Clean up for a task cancelled before its first step."""


def build_usage(
    final: FinalChunk | None,
    *,
    estimated_prompt: int,
    produced_chars: int,
    dispatched_at: float | None,
    first_token_at: float | None,
) -> dict:
    now = time.monotonic()
    total_ms = int((now - dispatched_at) * 1000) if dispatched_at is not None else 0
    first_ms = (
        int((first_token_at - dispatched_at) * 1000)
        if first_token_at is not None and dispatched_at is not None
        else None
    )
    prompt = final.prompt_tokens if final else None
    completion = final.completion_tokens if final else None
    estimated = prompt is None or completion is None
    if estimated:
        prompt = estimated_prompt
        completion = math.ceil(produced_chars / 4)
    rate = final.tokens_per_second if final else None
    if rate is None and first_token_at is not None and now > first_token_at:
        rate = round(completion / (now - first_token_at), 2)
    return {
        "prompt_tokens": prompt,
        "completion_tokens": completion,
        "total_tokens": prompt + completion,
        "first_token_ms": first_ms,
        "total_ms": total_ms,
        "load_ms": final.load_ms if final else None,
        "tokens_per_second": rate,
        "estimated": estimated,
    }


class Turn(EventPump):
    def __init__(
        self,
        service: TurnService,
        *,
        identity: Identity,
        request_id: str,
        conversation_id: str,
        title: str,
        spec: TierSpec,
        route: dict,
        inputs: ContextInputs,
        context: AssembledContext,
        ticket: Ticket,
        replace_after_seq: int | None,
        user_message_id: str,
        turn_notices: list[dict[str, str]],
        use_documents: bool,
    ) -> None:
        super().__init__(service.tuning.ping_interval_s)
        self.service = service
        self.identity = identity
        self.request_id = request_id
        self.conversation_id = conversation_id
        self.message_id = new_id()
        self.title = title
        self.spec = spec
        self.route = route
        self.inputs = inputs
        self.context = context
        self.ticket = ticket
        self.replace_after_seq = replace_after_seq
        self.user_message_id = user_message_id
        self.turn_notices = turn_notices
        self.use_documents = use_documents
        self.notices = self._notices_for(context)
        self.parts: list[str] = []
        self.thinking_chars = 0
        self.finish_reason: str | None = None
        self.failure: ApiError | None = None
        self.final: FinalChunk | None = None
        self.dispatched_at: float | None = None
        self.first_token_at: float | None = None
        self._released = False

    def _status(self, phase: str, message: str) -> None:
        self.emit("status", {"phase": phase, "message": message})

    def _release_conversation(self) -> None:
        """Once per turn. A later turn may already hold the same conversation."""
        if not self._released:
            self._released = True
            self.service.release_conversation(self.conversation_id)

    def _start_payload(self) -> dict:
        return {
            "request_id": self.request_id,
            "conversation_id": self.conversation_id,
            "message_id": self.message_id,
            "title": self.title,
            "route": public_route(self.route, self.identity),
            "context": {
                "estimated_tokens": self.context.estimated_tokens,
                "budget_tokens": self.context.budget_tokens,
                "omitted_messages": self.context.omitted_messages,
            },
            "notices": self.notices,
        }

    def _notices_for(self, context: AssembledContext) -> list[dict[str, str]]:
        """What the turn knows, then what assembly found, then what retrieval came to."""
        found = len(self.inputs.passages)
        return (
            self.turn_notices
            + context.notices
            + document_notices(self.use_documents, found, len(context.passages))
        )

    def _citations(self) -> list[dict]:
        return [
            citation_wire(number, passage)
            for number, passage in enumerate(self.context.passages, 1)
        ]

    async def _adopt(self, context: AssembledContext) -> None:
        """Make what is said and stored match the smaller context, before any text.

        `notices` replaces start.notices, and a second `citations` replaces the first
        when the sources changed. The user message keeps the file text actually sent.
        """
        before, attachments = self._citations(), self.context.attachments
        self.context = context
        self.notices = self._notices_for(context)
        if context.attachments != attachments:
            await self.service.store.replace_attachments(
                self.identity.user_id, self.user_message_id, context.attachments
            )
        self.emit("notices", {"notices": self.notices})
        after = self._citations()
        if after != before:
            self.emit("citations", {"citations": after})

    def _note_first_token(self) -> None:
        if self.first_token_at is None:
            self.first_token_at = time.monotonic()
            self._status("generating", GENERATING_MESSAGE)
            warmup = self.service.monitor.warmup
            if warmup is not None:
                warmup.note_model_loaded(self.spec.model)

    async def _generate(self, context: AssembledContext) -> None:
        request = InferenceRequest(self.spec, context.messages)
        async with aclosing(self.service.provider.stream(request)) as chunks:
            async for chunk in chunks:
                if isinstance(chunk, StatusChunk):
                    self._status(chunk.phase, chunk.message)
                elif isinstance(chunk, ThinkingChunk):
                    self._note_first_token()
                    self.thinking_chars += len(chunk.text)
                    self.emit("thinking", {"text": clean_text(chunk.text)})
                elif isinstance(chunk, DeltaChunk):
                    self._note_first_token()
                    text = clean_text(chunk.text)
                    self.parts.append(text)
                    self.emit("delta", {"text": text})
                elif isinstance(chunk, FinalChunk):
                    self.final = chunk
                    self.finish_reason = chunk.finish_reason

    def _shrink(self, overflow: ContextOverflow) -> AssembledContext:
        budget = self.context.budget_tokens
        if overflow.reported_tokens:
            scale = self.context.estimated_tokens / overflow.reported_tokens * SHRINK_MARGIN
            if overflow.context_tokens and self.spec.max_output:
                usable = overflow.context_tokens - self.spec.max_output
                if 0 < usable < budget:
                    budget = usable
        else:
            scale = FALLBACK_SHRINK
        return assemble_context(self.inputs, int(budget * min(scale, FALLBACK_SHRINK)))

    async def _drive(self) -> None:
        service = self.service
        self.emit("start", self._start_payload())
        citations = self._citations()
        if citations:
            self.emit("citations", {"citations": citations})
        if not service.slots.can_start():
            self._status("queued", service.queue_message())
        await service.slots.acquire(self.ticket)
        self.dispatched_at = time.monotonic()
        if self.context.estimated_tokens > READING_THRESHOLD_TOKENS:
            self._status("reading", READING_MESSAGE)
        try:
            await self._generate(self.context)
        except ContextOverflow as overflow:
            if self.parts or self.thinking_chars:
                raise ApiError("context_too_large", overflow.detail) from None
            self._status("reading", SHORTENED_MESSAGE)
            await self._adopt(self._shrink(overflow))
            try:
                await self._generate(self.context)
            except ContextOverflow as again:
                raise ApiError("context_too_large", again.detail) from None

    async def run(self) -> None:
        try:
            await self._drive()
        except asyncio.CancelledError:
            self.finish_reason = "cancelled"
        except ApiError as error:
            self.failure = error
            self.finish_reason = "error"
        except Exception as error:
            logger.error("turn failed type=%s", type(error).__name__)
            self.failure = ApiError("internal")
            self.finish_reason = "error"
        finally:
            self.service.slots.release(self.ticket)
        try:
            await run_to_completion(self._finish())
        finally:
            self._release_conversation()
            self.close()

    async def _finish(self) -> None:
        service = self.service
        text = "".join(self.parts)
        reason = self.finish_reason or "stop"
        usage = build_usage(
            self.final,
            estimated_prompt=self.context.estimated_tokens,
            produced_chars=len(text) + self.thinking_chars,
            dispatched_at=self.dispatched_at,
            first_token_at=self.first_token_at,
        )
        if self.final is not None and self.final.model:
            self.route = {**self.route, "model": self.final.model}
        # A reply of only spaces and newlines is no reply: it is not stored, and on
        # regenerate the previous answer stays.
        if text.strip():
            try:
                await service.store.finish_turn(
                    self.identity.user_id,
                    conversation_id=self.conversation_id,
                    message_id=self.message_id,
                    content=text,
                    finish_reason=reason,
                    citations=[
                        {
                            "number": number,
                            "document_id": passage.document_id,
                            "section": passage.section,
                            "version": passage.version,
                        }
                        for number, passage in enumerate(self.context.passages, 1)
                    ],
                    notices=self.notices,
                    route=self.route,
                    usage=usage,
                    replace_after_seq=self.replace_after_seq,
                )
            except Exception as error:
                logger.error("turn save failed type=%s", type(error).__name__)
                self.failure = self.failure or ApiError("internal")
        self._release_conversation()
        if reason != "cancelled":
            if self.failure is not None:
                detail = self.identity.permissions.view_diagnostics
                self.emit("error", self.failure.body(include_detail=detail))
            else:
                self.emit(
                    "done",
                    {"message_id": self.message_id, "finish_reason": reason, "usage": usage},
                )

    def abandoned(self) -> None:
        self.service.slots.release(self.ticket)
        self._release_conversation()
        self.close()


class ServiceTurn(EventPump):
    """One stateless model host request. Stores nothing."""

    def __init__(
        self,
        service: TurnService,
        *,
        spec: TierSpec,
        messages: list[dict[str, str]],
        estimated_tokens: int,
        ticket: Ticket,
    ) -> None:
        super().__init__(service.tuning.ping_interval_s)
        self.service = service
        self.spec = spec
        self.messages = messages
        self.estimated_tokens = estimated_tokens
        self.ticket = ticket

    def _status(self, phase: str, message: str) -> None:
        self.emit("status", {"phase": phase, "message": message})

    async def _drive(self) -> None:
        service = self.service
        if not service.slots.can_start():
            self._status("queued", service.queue_message())
        await service.slots.acquire(self.ticket)
        dispatched = time.monotonic()
        first_token: float | None = None
        final: FinalChunk | None = None
        produced = 0
        request = InferenceRequest(self.spec, self.messages)
        async with aclosing(service.provider.stream(request)) as chunks:
            async for chunk in chunks:
                if isinstance(chunk, StatusChunk):
                    self._status(chunk.phase, chunk.message)
                elif isinstance(chunk, FinalChunk):
                    final = chunk
                else:
                    first_token = first_token or time.monotonic()
                    produced += len(chunk.text)
                    event = "thinking" if isinstance(chunk, ThinkingChunk) else "delta"
                    self.emit(event, {"text": clean_text(chunk.text)})
        usage = build_usage(
            final,
            estimated_prompt=self.estimated_tokens,
            produced_chars=produced,
            dispatched_at=dispatched,
            first_token_at=first_token,
        )
        self.emit(
            "done",
            {
                "finish_reason": final.finish_reason if final else "stop",
                "usage": usage,
                "model": (final.model if final else None) or self.spec.model,
            },
        )

    async def run(self) -> None:
        try:
            await self._drive()
        except asyncio.CancelledError:
            pass
        except ApiError as error:
            self.emit("error", error.body(include_detail=False))
        except Exception as error:
            logger.error("service turn failed type=%s", type(error).__name__)
            self.emit("error", ApiError("internal").body(include_detail=False))
        finally:
            self.service.slots.release(self.ticket)
            self.close()

    def abandoned(self) -> None:
        self.service.slots.release(self.ticket)
        self.close()


class TurnService:
    def __init__(
        self,
        settings: Settings,
        tuning: Tuning,
        provider: InferenceProvider,
        registry: ModelRegistry,
        router: Router,
        retriever: Retriever,
        store: ConversationStore,
        slots: InferenceSlots,
        monitor: StatusMonitor,
    ) -> None:
        self.settings = settings
        self.tuning = tuning
        self.provider = provider
        self.registry = registry
        self.router = router
        self.retriever = retriever
        self.store = store
        self.slots = slots
        self.monitor = monitor
        self._unfinished: dict[str, asyncio.Event] = {}
        self._tasks: set[asyncio.Task[None]] = set()
        self._pumps: set[EventPump] = set()

    def queue_message(self) -> str:
        warmup = self.monitor.warmup
        return LOADING_MESSAGE if warmup and warmup.state == "loading" else QUEUED_MESSAGE

    def _launch(self, pump: EventPump) -> None:
        task = pump.launch()
        self._pumps.add(pump)
        self._tasks.add(task)

        def forget(done: asyncio.Task[None]) -> None:
            self._tasks.discard(done)
            self._pumps.discard(pump)

        task.add_done_callback(forget)

    async def shutdown(self) -> None:
        for pump in list(self._pumps):
            pump.cancel()
        if self._tasks:
            await asyncio.gather(*self._tasks, return_exceptions=True)

    def _validate(self, body: ChatRequest) -> None:
        limits = self.settings.limits
        if body.regenerate:
            if not body.conversation_id:
                raise ApiError("validation_error", "regenerate requires conversation_id")
            if body.attachments:
                raise ApiError("validation_error", "regenerate takes no attachments")
            return
        if len(body.message) > limits.max_message_chars:
            raise ApiError("request_too_large", "message exceeds max_message_chars")
        if len(body.attachments) > limits.max_attachments:
            raise ApiError("request_too_large", "too many attachments")
        extensions = {item.lower() for item in limits.attachment_extensions}
        for item in body.attachments:
            if any(mark in item.name for mark in ("/", "\\", "\n", "\r", "\0")):
                raise ApiError("validation_error", "attachment name is not a plain file name")
            stem, dot, suffix = item.name.rpartition(".")
            if not dot or not stem or f".{suffix.lower()}" not in extensions:
                raise ApiError("unsupported_attachment", "attachment extension not accepted")
            if len(item.text.encode("utf-8")) > limits.max_attachment_bytes:
                raise ApiError("request_too_large", "attachment exceeds max_attachment_bytes")
        if not body.message.strip() and not body.attachments:
            raise ApiError("validation_error", "message or attachment required")

    @staticmethod
    def _authorize(identity: Identity, body: ChatRequest) -> None:
        permissions = identity.permissions
        if body.tier != "auto":
            if not permissions.manual_model_selection:
                raise ApiError("permission_denied", "manual_model_selection is required")
            if body.tier not in permissions.allowed_tiers:
                raise ApiError("permission_denied", "tier is outside allowed_tiers")
        if body.assistant == "code" and not permissions.advanced_mode:
            raise ApiError("permission_denied", "the code assistant requires advanced_mode")
        if body.use_documents and not permissions.search_documents:
            raise ApiError("permission_denied", "search_documents is required")

    async def _claim_conversation(self, conversation_id: str) -> None:
        """Wait for an unfinished turn. Nothing awaits between the check and the claim."""
        deadline = time.monotonic() + self.tuning.unfinished_turn_wait_s
        while (running := self._unfinished.get(conversation_id)) is not None:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise ApiError("busy", "a previous turn has not finished")
            try:
                await asyncio.wait_for(running.wait(), remaining)
            except TimeoutError:
                raise ApiError("busy", "a previous turn has not finished") from None
        self._unfinished[conversation_id] = asyncio.Event()

    def release_conversation(self, conversation_id: str) -> None:
        running = self._unfinished.pop(conversation_id, None)
        if running is not None:
            running.set()

    async def _resolve_route(
        self,
        identity: Identity,
        body: ChatRequest,
        message: str,
        attachment_count: int,
    ) -> tuple[Resolution, str]:
        """Policy first: the router sees only tiers this identity is allowed."""
        health = await self.provider.preflight()
        permitted = self.registry.permitted(identity.permissions.allowed_tiers)
        if body.tier != "auto":
            return self.registry.resolve(body.tier, permitted, health), "Manual selection"
        decision = self.router.choose(
            RouteSignals(message, attachment_count, body.use_documents, body.assistant), permitted
        )
        if decision is None:
            raise ApiError("model_not_installed", "no permitted tier suits automatic routing")
        return self.registry.resolve(decision.tier, permitted, health), decision.reason

    @staticmethod
    def _history(rows: list[MessageRecord]) -> list[dict]:
        history: list[dict] = []
        for row in rows:
            if row.role != "user":
                history.append({"role": row.role, "content": row.content})
                continue
            history.append(
                {
                    "role": row.role,
                    "content": render_user(row.content, row.attachments),
                    "files": [item.name for item in row.attachments if not item.unread],
                }
            )
        return history

    async def begin(
        self,
        identity: Identity,
        body: ChatRequest,
        request_id: str,
        client_gone: Callable[[], Awaitable[bool]] | None = None,
    ) -> Turn:
        """Everything before `start`. Any failure here writes nothing.

        `client_gone` is asked just before the first write, because closing the
        connection does not cancel this work, and a caller that left before `start`
        treats the turn as never sent.
        """
        self._validate(body)
        locality = self.monitor.provider_locality().kind
        existing_id = body.conversation_id
        if existing_id is not None:
            conversation = await self.store.get_conversation(identity.user_id, existing_id)
            if conversation is None:
                raise ApiError("not_found")
            if conversation.locality != locality:
                raise ApiError(
                    "conversation_locality_changed",
                    f"conversation {conversation.locality}, provider {locality}",
                )
        self._authorize(identity, body)

        conversation_id = existing_id or new_id()
        await self._claim_conversation(conversation_id)
        ticket: Ticket | None = None
        try:
            ticket = self.slots.reserve()
            turn = await self._plan(
                identity, body, request_id, conversation_id, ticket, locality, client_gone
            )
            self._launch(turn)
            return turn
        except BaseException:
            if ticket is not None:
                self.slots.release(ticket)
            self.release_conversation(conversation_id)
            raise

    async def _plan(
        self,
        identity: Identity,
        body: ChatRequest,
        request_id: str,
        conversation_id: str,
        ticket: Ticket,
        locality: str,
        client_gone: Callable[[], Awaitable[bool]] | None,
    ) -> Turn:
        user_id = identity.user_id
        rows: list[MessageRecord] = []
        title = ""
        if body.conversation_id is not None:
            conversation = await self.store.get_conversation(user_id, conversation_id)
            if conversation is None:
                raise ApiError("not_found")
            title = conversation.title
            rows = await self.store.get_messages(user_id, conversation_id)

        replace_after: int | None = None
        attachments = [
            Attachment(item.name, item.media_type, item.text) for item in body.attachments
        ]
        message = body.message
        user_message_id = new_id()
        if body.regenerate:
            last_user = next((row for row in reversed(rows) if row.role == "user"), None)
            if last_user is None:
                raise ApiError("not_found")
            replace_after = last_user.seq
            user_message_id = last_user.id
            message = last_user.content
            attachments = list(last_user.attachments)
            rows = [row for row in rows if row.seq < last_user.seq]

        resolution, reason = await self._resolve_route(identity, body, message, len(attachments))
        spec = resolution.spec
        notices: list[dict[str, str]] = []
        if resolution.fallback_from is not None:
            wanted = self.registry.get(resolution.fallback_from)
            notices.append(
                {
                    "code": "tier_fallback",
                    # Stored with the reply and shown again on reopen, so past tense.
                    "message": f"{wanted.label if wanted else 'That tier'} wasn't available, "
                    f"so {spec.label} was used.",
                }
            )

        passages = (
            self.retriever.search(
                message, identity.groups, self.settings.documents.max_results
            )
            if body.use_documents
            else []
        )
        inputs = ContextInputs(
            system_prompt=system_prompt(body.assistant),
            message=message,
            attachments=attachments,
            passages=passages,
            history=self._history(rows),
        )
        context = assemble_context(inputs, (spec.context or 0) - (spec.max_output or 0))

        title = title or make_title(message, attachments)
        route = {
            "requested_tier": body.tier,
            "tier": spec.tier,
            "locality": locality,
            "fallback_from": resolution.fallback_from,
            "model": spec.model,
            "provider": self.provider.name,
            "reason": reason,
        }
        if client_gone is not None and await client_gone():
            raise ClientClosed()
        if body.regenerate:
            present = await self.store.note_assistant(user_id, conversation_id, body.assistant)
            if present and context.attachments != attachments:
                await self.store.replace_attachments(user_id, user_message_id, context.attachments)
        else:
            present = await self.store.create_turn(
                user_id,
                conversation_id=conversation_id,
                existing=body.conversation_id is not None,
                title=title,
                assistant=body.assistant,
                locality=locality,
                message_id=user_message_id,
                content=message,
                attachments=context.attachments,
            )
        if not present:
            # Deleted while the turn was being planned. It stays deleted.
            raise ApiError("not_found")
        return Turn(
            self,
            identity=identity,
            request_id=request_id,
            conversation_id=conversation_id,
            title=title,
            spec=spec,
            route=route,
            inputs=inputs,
            context=context,
            ticket=ticket,
            replace_after_seq=replace_after,
            user_message_id=user_message_id,
            turn_notices=notices,
            use_documents=body.use_documents,
        )

    async def begin_service(self, body: InferenceRequestBody) -> ServiceTurn:
        spec = self.registry.get(body.tier)
        if spec is None or not spec.enabled:
            raise ApiError("model_not_installed", f"tier {body.tier} is not served here")
        messages = [{"role": item.role, "content": item.content} for item in body.messages]
        estimated = sum(estimate_tokens(item["content"]) for item in messages)
        if estimated > (spec.context or 0) - (spec.max_output or 0):
            raise ContextOverflow()
        ticket = self.slots.reserve()
        try:
            health = await self.provider.preflight()
            if not self.registry.is_usable(spec, health):
                raise ApiError("model_not_installed", f"tier {body.tier} has no usable model")
            turn = ServiceTurn(
                self, spec=spec, messages=messages, estimated_tokens=estimated, ticket=ticket
            )
            self._launch(turn)
            return turn
        except BaseException:
            self.slots.release(ticket)
            raise
