"""Computation and persistence ports, independent of FastAPI and provider SDKs."""
from collections.abc import AsyncIterator, Sequence
from dataclasses import dataclass
from typing import Protocol


@dataclass(frozen=True)
class IndexTask:
    schema_version: int
    task_id: str
    entry_id: str
    kb_id: str
    entry_version: int
    trace_id: str


@dataclass(frozen=True)
class Chunk:
    content: str
    embedding: Sequence[float]


class ModelProvider(Protocol):
    async def embed(self, texts: Sequence[str]) -> Sequence[Sequence[float]]: ...
    def stream(self, prompt: str) -> AsyncIterator[str]: ...
    async def describe(self, image: bytes, media_type: str) -> str: ...


class DocumentParser(Protocol):
    async def parse(self, content: bytes, filename: str) -> str: ...


class ChunkRepository(Protocol):
    async def read_authorized_content(self, task: IndexTask) -> str: ...
    # Lock entry and recheck version in the SAME transaction as chunk replacement.
    # Return False for deleted/stale tasks; never change business entry status.
    async def replace_if_current(self, task: IndexTask, chunks: Sequence[Chunk]) -> bool: ...


class ResultPublisher(Protocol):
    # Confirm result publication before acknowledging the original task.
    async def publish_confirmed(self, task: IndexTask, status: str, chunk_count: int, error_code: str | None) -> None: ...
