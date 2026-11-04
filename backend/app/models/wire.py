"""Request bodies. Unknown fields are ignored, so a key name never reaches a log."""

from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field, field_validator

ID_PATTERN = r"^[A-Za-z0-9_-]{1,64}$"


class _Request(BaseModel):
    model_config = ConfigDict(hide_input_in_errors=True, extra="ignore")


class AttachmentIn(_Request):
    name: str = Field(min_length=1, max_length=255)
    media_type: str = Field(default="text/plain", max_length=127)
    text: str


class ChatRequest(_Request):
    conversation_id: str | None = Field(default=None, pattern=ID_PATTERN)
    message: str = ""
    attachments: list[AttachmentIn] = Field(default_factory=list)
    tier: Literal["auto", "fast", "standard", "advanced", "specialist"] = "auto"
    assistant: Literal["general", "code"] = "general"
    use_documents: bool = False
    regenerate: bool = False

    @field_validator("message", mode="before")
    @classmethod
    def _null_message(cls, value: object) -> object:
        return "" if value is None else value


class InferenceMessage(_Request):
    role: Literal["system", "user", "assistant"]
    content: str


class InferenceRequestBody(_Request):
    tier: Literal["fast", "standard", "advanced", "specialist"]
    messages: list[InferenceMessage] = Field(min_length=1)
