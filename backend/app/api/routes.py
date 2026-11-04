"""The routes from docs/api.md. All paths are written in full so logs show the template."""

from __future__ import annotations

from dataclasses import dataclass
from typing import Annotated, Any

from fastapi import APIRouter, Path, Query, Request, Response
from starlette.responses import StreamingResponse
from starlette.types import Receive, Scope, Send

from app import __version__
from app.config.settings import Settings, Tuning
from app.models.domain import Identity, MessageRecord
from app.models.errors import ApiError
from app.models.wire import ID_PATTERN, ChatRequest, InferenceRequestBody
from app.providers.base import InferenceProvider
from app.router.registry import ModelRegistry
from app.security.identity import Authenticator
from app.services.privacy import PrivacyService
from app.services.retriever import KeywordRetriever, citation_wire
from app.services.slots import InferenceSlots
from app.services.status import StatusMonitor
from app.services.store import SqliteConversationStore
from app.services.turn import ClientClosed, EventPump, TurnService, public_route
from app.services.warmup import WarmupService

router = APIRouter()
service_router = APIRouter()

ConversationId = Annotated[str, Path(pattern=ID_PATTERN)]


@dataclass
class Services:
    settings: Settings
    tuning: Tuning
    authenticator: Authenticator
    provider: InferenceProvider
    registry: ModelRegistry
    retriever: KeywordRetriever
    store: SqliteConversationStore
    slots: InferenceSlots
    monitor: StatusMonitor
    warmup: WarmupService
    privacy: PrivacyService
    turns: TurnService


def _services(request: Request) -> Services:
    return request.app.state.services


def _identity(request: Request) -> Identity:
    return request.state.identity


class EventStream(StreamingResponse):
    """Also cancels on the paths where the generator's own finally never runs.

    A disconnect before the first iteration leaves the generator unstarted, and one
    during send leaves it suspended at a yield.
    """

    def __init__(self, pump: EventPump) -> None:
        super().__init__(
            pump.events(),
            media_type="text/event-stream",
            headers={"X-Accel-Buffering": "no"},
        )
        self._pump = pump

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        try:
            await super().__call__(scope, receive, send)
        finally:
            self._pump.cancel()


@router.get("/healthz")
async def healthz() -> dict:
    return {"status": "ok"}


@router.get("/v1/bootstrap")
async def bootstrap(request: Request) -> dict:
    services = _services(request)
    identity = _identity(request)
    settings = services.settings
    permissions = identity.permissions
    await services.monitor.wait_first(3.0)
    suggestions = [
        suggestion.model_dump()
        for suggestion in settings.suggestions
        if suggestion.action != "documents" or permissions.search_documents
    ]
    return {
        "application": {
            "name": settings.application.name,
            "version": __version__,
            "stage": settings.application.stage,
            "notice": settings.application.notice,
        },
        "identity": {
            "user_id": identity.user_id,
            "display_name": identity.display_name,
            "groups": list(identity.groups),
            "auth_mode": identity.auth_mode,
        },
        "permissions": {
            "advanced_mode": permissions.advanced_mode,
            "manual_model_selection": permissions.manual_model_selection,
            "view_diagnostics": permissions.view_diagnostics,
            "search_documents": permissions.search_documents,
            "allowed_tiers": [
                spec.tier for spec in services.registry.permitted(permissions.allowed_tiers)
            ],
        },
        "locality": services.monitor.locality().wire(),
        "suggestions": suggestions,
        "limits": settings.limits.model_dump(),
    }


@router.get("/v1/status")
async def status(request: Request) -> dict:
    return _services(request).monitor.view(_identity(request))


@router.get("/v1/models")
async def models(request: Request) -> dict:
    services = _services(request)
    identity = _identity(request)
    if not identity.permissions.manual_model_selection:
        raise ApiError("permission_denied", "manual_model_selection is required")
    health = services.monitor.health
    tiers = []
    for spec in services.registry.all():
        if spec.enabled and spec.tier not in identity.permissions.allowed_tiers:
            continue
        available = services.registry.is_usable(spec, health)
        reason = None
        if not spec.enabled:
            reason = "Not offered yet"
        elif not available:
            reason = "Not installed"
        tiers.append(
            {
                "tier": spec.tier,
                "label": spec.label,
                "description": spec.description,
                "enabled": spec.enabled,
                "available": available,
                "unavailable_reason": reason,
                "model": spec.model,
                "context_tokens": spec.context,
                "max_output_tokens": spec.max_output,
                "temperature": spec.temperature,
                "thinking": spec.think,
            }
        )
    return {"provider": services.provider.name, "tiers": tiers}


@router.get("/v1/privacy")
async def privacy(request: Request) -> dict:
    return await _services(request).privacy.build(_identity(request))


CLIENT_CLOSED = 499


@router.post("/v1/chat/stream")
async def chat_stream(body: ChatRequest, request: Request) -> Response:
    services = _services(request)
    try:
        turn = await services.turns.begin(
            _identity(request), body, request.state.request_id, request.is_disconnected
        )
    except ClientClosed:
        # Nobody is left to read a reply. The status is only for the request log.
        return Response(status_code=CLIENT_CLOSED)
    return EventStream(turn)


def _summary(record: Any) -> dict:
    return {
        "id": record.id,
        "title": record.title,
        "assistant": record.assistant,
        "locality": record.locality,
        "created_at": record.created_at,
        "updated_at": record.updated_at,
        "message_count": record.message_count,
    }


def _message(services: Services, identity: Identity, row: MessageRecord) -> dict:
    citations = []
    for stored in row.citations:
        passage = services.retriever.get_section(
            identity.groups, stored["document_id"], stored["section"]
        )
        if passage is None:
            citations.append({"number": stored["number"], "withheld": True})
        else:
            citations.append(citation_wire(stored["number"], passage, stored["version"]))
    return {
        "id": row.id,
        "role": row.role,
        "content": row.content,
        "created_at": row.created_at,
        "attachments": [
            {"name": item.name, "media_type": item.media_type, "chars": len(item.text)}
            for item in row.attachments
        ],
        "citations": citations,
        "notices": row.notices,
        "route": public_route(row.route, identity) if row.route else None,
        "usage": row.usage,
        "finish_reason": row.finish_reason,
    }


@router.get("/v1/conversations")
async def list_conversations(
    request: Request, limit: Annotated[int, Query(ge=1, le=500)] = 100
) -> dict:
    services = _services(request)
    records = await services.store.list_conversations(_identity(request).user_id, limit)
    return {"conversations": [_summary(record) for record in records]}


@router.get("/v1/conversations/{conversation_id}")
async def get_conversation(conversation_id: ConversationId, request: Request) -> dict:
    services = _services(request)
    identity = _identity(request)
    record = await services.store.get_conversation(identity.user_id, conversation_id)
    if record is None:
        raise ApiError("not_found")
    rows = await services.store.get_messages(identity.user_id, conversation_id)
    return {**_summary(record), "messages": [_message(services, identity, row) for row in rows]}


@router.delete("/v1/conversations/{conversation_id}", status_code=204)
async def delete_conversation(conversation_id: ConversationId, request: Request) -> Response:
    services = _services(request)
    if not await services.store.delete_conversation(_identity(request).user_id, conversation_id):
        raise ApiError("not_found")
    return Response(status_code=204)


@router.delete("/v1/conversations")
async def delete_conversations(request: Request) -> dict:
    return {"deleted": await _services(request).store.delete_all(_identity(request).user_id)}


@service_router.post("/v1/inference/stream")
async def inference_stream(body: InferenceRequestBody, request: Request) -> Response:
    return EventStream(await _services(request).turns.begin_service(body))
