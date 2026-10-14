import json
from typing import Literal
from uuid import UUID

from pydantic import BaseModel, ConfigDict, Field, ValidationError


class InvalidTask(Exception):
    pass


class IndexTask(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)
    schema_version: Literal[1]
    task_id: UUID
    entry_id: UUID
    kb_id: UUID
    entry_version: int = Field(ge=1, le=2**63-1)
    trace_id: str = Field(min_length=1, max_length=128)

    @classmethod
    def decode(cls, body: bytes):
        def pairs(items):
            result = {}
            for key, value in items:
                if key in result:
                    raise ValueError()
                result[key] = value
            return result
        try:
            if len(body) > 65536:
                raise ValueError()
            parsed = json.loads(body.decode("utf-8"), object_pairs_hook=pairs)
            if not isinstance(parsed, dict) or type(parsed.get("schema_version")) is not int:
                raise ValueError()
            task = cls.model_validate_json(body)
            if "\x00" in task.trace_id:
                raise ValueError()
            return task
        except (ValueError, ValidationError, UnicodeError):
            raise InvalidTask("invalid_index_task") from None

    def result(self, status: str, count: int = 0, error: str | None = None):
        return {"schema_version": 1, "task_id": str(self.task_id), "entry_id": str(self.entry_id), "entry_version": self.entry_version, "trace_id": self.trace_id, "status": status, "chunk_count": count, "error_code": error}
