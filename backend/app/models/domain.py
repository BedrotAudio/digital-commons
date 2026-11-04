"""Types shared across the gateway that never cross the wire as they are."""

from __future__ import annotations

from dataclasses import dataclass, field


@dataclass(frozen=True)
class Permissions:
    advanced_mode: bool
    manual_model_selection: bool
    view_diagnostics: bool
    search_documents: bool
    allowed_tiers: tuple[str, ...]


@dataclass(frozen=True)
class Identity:
    user_id: str
    display_name: str
    groups: tuple[str, ...]
    permissions: Permissions
    auth_mode: str


@dataclass(frozen=True)
class Locality:
    kind: str
    label: str
    description: str

    def wire(self) -> dict:
        return {"kind": self.kind, "label": self.label, "description": self.description}


@dataclass(frozen=True)
class TierSpec:
    tier: str
    label: str
    description: str
    enabled: bool
    model: str | None
    context: int | None
    max_output: int | None
    temperature: float | None
    think: bool


@dataclass(frozen=True)
class Attachment:
    """`text` is what is or was sent. `total_chars` is the length of the whole file."""

    name: str
    media_type: str
    text: str
    total_chars: int = -1

    def __post_init__(self) -> None:
        if self.total_chars < 0:
            object.__setattr__(self, "total_chars", len(self.text))

    @property
    def unread(self) -> bool:
        """None of a file that has text was sent, so it gets no block in the prompt."""
        return not self.text and self.total_chars > 0


@dataclass
class Passage:
    document_id: str
    title: str
    department: str
    classification: str
    version: str
    document_date: str
    source_filename: str
    section: str
    text: str
    snippet: str
    page: int | None = None
    sample: bool = False


@dataclass
class ConversationRecord:
    id: str
    title: str
    assistant: str
    locality: str
    created_at: str
    updated_at: str
    message_count: int


@dataclass
class MessageRecord:
    id: str
    seq: int
    role: str
    content: str
    created_at: str
    finish_reason: str | None = None
    citations: list[dict] = field(default_factory=list)
    notices: list[dict] = field(default_factory=list)
    route: dict | None = None
    usage: dict | None = None
    attachments: list[Attachment] = field(default_factory=list)
