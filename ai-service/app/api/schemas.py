from typing import Annotated, Literal
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field, StringConstraints


Text = Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=200_000)]


class InternalRequest(BaseModel):
    model_config = ConfigDict(extra="forbid")
    request_id: UUID


class Message(BaseModel):
    model_config = ConfigDict(extra="forbid")
    role: Literal["user", "assistant"]
    content: Text


class ChatRequest(InternalRequest):
    user_id: UUID
    kb_id: UUID
    query: Annotated[str, StringConstraints(strip_whitespace=True, min_length=1, max_length=10_000)]
    history: list[Message] = Field(default_factory=list, max_length=500)
    system_prompt: str | None = Field(default=None, max_length=20_000)


class ExtractRequest(InternalRequest):
    history: list[Message] = Field(min_length=1, max_length=500)
    mode: Literal["manual","scheduled"] = "manual"


class DescribeRequest(InternalRequest):
    image_base64: str = Field(min_length=4, max_length=7 * 1024 * 1024)
    media_type: Literal["image/png", "image/jpeg"]
    context: str | None = Field(default=None, max_length=20_000)
