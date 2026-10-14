import asyncio
from uuid import uuid4

from psycopg.types.json import Jsonb

from app.providers.embeddings import EmbeddingError, validate_vector
from app.rag.chunker import chunk_text
from app.workers.contracts import InvalidTask


class TaskBusy(Exception):
    pass


class Indexer:
    def __init__(self, pool, embeddings):
        self.pool, self.embeddings = pool, embeddings

    async def _read(self, conn, task, lock=False):
        suffix = " FOR UPDATE OF e" if lock else ""
        cursor = await conn.execute("""SELECT e.content,e.entry_version,e.kb_id,t.entry_id AS task_entry,
t.kb_id AS task_kb,t.entry_version AS task_version,t.trace_id,t.status
FROM knowledge_entry e JOIN index_task t ON t.id=%s WHERE e.id=%s""" + suffix, (task.task_id, task.entry_id))
        row = await cursor.fetchone()
        if row is None:
            # Missing task with an existing entry is a forged scope, not deletion.
            cursor = await conn.execute("SELECT 1 FROM knowledge_entry WHERE id=%s", (task.entry_id,))
            if await cursor.fetchone() is not None:
                raise InvalidTask("task_identity_mismatch")
            return None
        if row["kb_id"] != task.kb_id or row["task_entry"] != task.entry_id or row["task_kb"] != task.kb_id or row["task_version"] != task.entry_version or row["trace_id"] != task.trace_id or row["entry_version"] < task.entry_version:
            raise InvalidTask("task_identity_mismatch")
        return row

    async def _receipt(self, conn, task):
        cursor = await conn.execute("SELECT result FROM index_receipt WHERE task_id=%s", (task.task_id,))
        row = await cursor.fetchone()
        return row["result"] if row else None

    async def _save(self, conn, task, chunks=None, vectors=None, error=None):
        async with conn.transaction():
            row = await self._read(conn, task, lock=True)
            if row is None:
                return task.result("STALE")
            receipt = await self._receipt(conn, task)
            if receipt is not None:
                return receipt
            if row["entry_version"] > task.entry_version:
                result = task.result("STALE")
            elif error:
                result = task.result("FAILED", error=error)
            else:
                # A newer edit may have happened while the model was running;
                # its version check above precedes any DELETE/INSERT.
                await conn.execute("DELETE FROM knowledge_chunk WHERE entry_id=%s", (task.entry_id,))
                records = [(uuid4(), task.entry_id, chunk["index"], chunk["content"], "[" + ",".join(str(value) for value in vector) + "]") for chunk, vector in zip(chunks, vectors, strict=True)]
                async with conn.cursor() as cursor:
                    await cursor.executemany("INSERT INTO knowledge_chunk(id,entry_id,chunk_index,content,embedding) VALUES(%s,%s,%s,%s,%s::vector)", records)
                result = task.result("COMPLETED", len(records))
            await conn.execute("INSERT INTO index_receipt(task_id,result) VALUES(%s,%s)", (task.task_id, Jsonb(result)))
            return result

    async def process(self, task, terminal_error=None):
        async with self.pool.connection() as conn:
            cursor = await conn.execute("SELECT pg_try_advisory_lock(hashtextextended(%s,1974)) AS locked", (str(task.task_id),))
            if not (await cursor.fetchone())["locked"]:
                raise TaskBusy()
            try:
                row = await self._read(conn, task)
                if row is None:
                    return task.result("STALE")
                receipt = await self._receipt(conn, task)
                if receipt is not None:
                    return receipt
                if row["entry_version"] > task.entry_version:
                    return await self._save(conn, task, error="superseded")
                if terminal_error is not None:
                    return await self._save(conn, task, error=terminal_error)
                try:
                    chunks = chunk_text(row["content"])
                    if not chunks:
                        raise EmbeddingError("empty_indexing_text")
                    async with asyncio.timeout(15*60):
                        vectors = await self.embeddings.embed([chunk["content"] for chunk in chunks])
                    if len(vectors) != len(chunks):
                        raise EmbeddingError("invalid_embedding_response")
                    vectors = [validate_vector(vector) for vector in vectors]
                except TimeoutError:
                    raise EmbeddingError("embedding_timeout", True) from None
                except EmbeddingError as error:
                    if error.retryable:
                        raise
                    return await self._save(conn, task, error=error.code)
                except (ValueError, UnicodeError):
                    return await self._save(conn, task, error="invalid_indexing_text")
                return await self._save(conn, task, chunks=chunks, vectors=vectors)
            finally:
                # Cancellation/connection loss releases the session lock when
                # the pooled connection is discarded or this explicit unlock runs.
                await conn.execute("SELECT pg_advisory_unlock(hashtextextended(%s,1974))", (str(task.task_id),))
