import asyncio
import json
import os
from pathlib import Path
import sys
import unittest
from uuid import uuid4

import aio_pika
from psycopg import AsyncConnection, sql
from psycopg.rows import dict_row
from psycopg_pool import AsyncConnectionPool

from app.providers.embeddings import EmbeddingError
from app.workers.contracts import IndexTask, InvalidTask
from app.workers.consumer import Consumer
from app.workers.indexer import Indexer, TaskBusy
from app.workers.schema import check_schema

if sys.platform == "win32":
    asyncio.set_event_loop_policy(asyncio.WindowsSelectorEventLoopPolicy())

ENDPOINT = "postgresql://postgres:migration-test-only@127.0.0.1:55432/"
BROKER = "amqp://guest:guest@127.0.0.1:55673/"


def body(task):
    return task.model_dump_json().encode()


class ContractTests(unittest.TestCase):
    def test_task_strict_contract_and_duplicates(self):
        payload = {"schema_version": 1, "task_id": str(uuid4()), "entry_id": str(uuid4()), "kb_id": str(uuid4()), "entry_version": 1, "trace_id": "本地🙂"}
        self.assertEqual(IndexTask.decode(json.dumps(payload).encode()).trace_id, "本地🙂")
        for change in ({"schema_version": True}, {"entry_version": True}, {"entry_version": 0}, {"entry_version": 2**63}, {"kb_id": "invalid"}, {"trace_id": ""}, {"extra": 1}):
            with self.assertRaises(InvalidTask):
                IndexTask.decode(json.dumps(payload | change).encode())
        for value in (b'{"schema_version":1,"schema_version":1}', b"\xff", b"x"*65537):
            with self.assertRaises(InvalidTask):
                IndexTask.decode(value)


class MockEmbeddings:
    def __init__(self):
        self.calls = 0
        self.error = None
        self.entered = None
        self.release = None

    async def embed(self, texts):
        self.calls += 1
        if self.entered:
            self.entered.set()
            await self.release.wait()
        if self.error:
            raise self.error
        return [[0.01] * 1024 for _ in texts]


@unittest.skipUnless(os.environ.get("RUN_DATABASE_TESTS") == "1", "requires isolated PostgreSQL")
class WorkerIntegrationTests(unittest.IsolatedAsyncioTestCase):
    async def asyncSetUp(self):
        self.name = "migration_python_worker_" + uuid4().hex
        self.admin = await AsyncConnection.connect(ENDPOINT+"postgres", autocommit=True)
        await self.admin.execute(sql.SQL("CREATE DATABASE {}").format(sql.Identifier(self.name)))
        self.pool = AsyncConnectionPool(ENDPOINT+self.name, min_size=1, max_size=4, open=False, kwargs={"autocommit": True, "row_factory": dict_row})
        await self.pool.open(wait=True)
        async with self.pool.connection() as conn:
            for migration in sorted((Path(__file__).resolve().parents[2]/"server/prisma/migrations").glob("*/migration.sql")):
                await conn.execute(migration.read_text(encoding="utf-8"))
            self.user, self.kb = uuid4(), uuid4()
            await conn.execute('INSERT INTO "user"(id,username,email,password_hash,updated_at) VALUES(%s,\'worker\',\'worker@example.test\',\'unused\',now())', (self.user,))
            await conn.execute("INSERT INTO knowledge_base(id,user_id,name,updated_at) VALUES(%s,%s,'worker',now())", (self.kb,self.user))
        self.embeddings = MockEmbeddings()
        self.indexer = Indexer(self.pool,self.embeddings)

    async def asyncTearDown(self):
        await self.pool.close()
        await self.admin.execute(sql.SQL("DROP DATABASE {} WITH (FORCE)").format(sql.Identifier(self.name)))
        await self.admin.close()

    async def task(self, text="中"*4000):
        payload = {"schema_version": 1, "task_id": str(uuid4()), "entry_id": str(uuid4()), "kb_id": str(self.kb), "entry_version": 1, "trace_id": "local-worker"}
        task = IndexTask.decode(json.dumps(payload).encode())
        async with self.pool.connection() as conn:
            await conn.execute("INSERT INTO knowledge_entry(id,kb_id,title,content,updated_at) VALUES(%s,%s,'worker',%s,now())", (task.entry_id,self.kb,text))
            await conn.execute("INSERT INTO index_task(id,entry_id,kb_id,entry_version,trace_id) VALUES(%s,%s,%s,1,%s)", (task.task_id,task.entry_id,task.kb_id,task.trace_id))
        return task

    async def fetch(self, query, args=()):
        async with self.pool.connection() as conn:
            cursor = await conn.execute(query,args)
            return await cursor.fetchall()

    async def test_atomic_chunks_receipt_and_duplicate(self):
        task = await self.task()
        result = await self.indexer.process(task)
        self.assertEqual((result["status"],result["chunk_count"]),("COMPLETED",3))
        chunks = await self.fetch("SELECT id,chunk_index,content,vector_dims(embedding) AS dimensions FROM knowledge_chunk ORDER BY chunk_index")
        self.assertEqual([row["chunk_index"] for row in chunks],[0,1,2])
        self.assertTrue(all(row["dimensions"]==1024 for row in chunks))
        self.assertEqual(await self.indexer.process(task),result)
        self.assertEqual(self.embeddings.calls,1)
        self.assertEqual(await self.fetch("SELECT id,chunk_index,content,vector_dims(embedding) AS dimensions FROM knowledge_chunk ORDER BY chunk_index"),chunks)
        self.assertEqual((await self.fetch("SELECT processing_status::text AS status FROM knowledge_entry"))[0]["status"],"PENDING")
        self.assertEqual((await self.fetch("SELECT status FROM index_task"))[0]["status"],"PENDING")

    async def test_startup_schema_and_forged_scope_rejected_before_model(self):
        task = await self.task("short")
        async with self.pool.connection() as conn:
            await check_schema(conn)
            async with conn.transaction(force_rollback=True):
                await conn.execute("ALTER TABLE index_receipt ALTER COLUMN result DROP NOT NULL")
                with self.assertRaises(ValueError):
                    await check_schema(conn)
        payload = task.model_dump(mode="json") | {"kb_id": str(uuid4())}
        with self.assertRaises(InvalidTask):
            await self.indexer.process(IndexTask.decode(json.dumps(payload).encode()))
        self.assertEqual(self.embeddings.calls,0)

    async def test_old_version_and_deleted_during_embedding(self):
        for scenario in ("edited","deleted"):
            task=await self.task("short")
            self.embeddings.entered,self.embeddings.release=asyncio.Event(),asyncio.Event()
            work=asyncio.create_task(self.indexer.process(task))
            await self.embeddings.entered.wait()
            async with self.pool.connection() as conn:
                if scenario=="edited":
                    await conn.execute("UPDATE knowledge_entry SET entry_version=2,content='newer' WHERE id=%s",(task.entry_id,))
                else:
                    await conn.execute("DELETE FROM knowledge_entry WHERE id=%s",(task.entry_id,))
            self.embeddings.release.set()
            self.assertEqual((await work)["status"],"STALE")
            self.assertEqual(await self.fetch("SELECT 1 FROM knowledge_chunk WHERE entry_id=%s",(task.entry_id,)),[])

    async def test_concurrent_task_skips_embedding_and_cancel_releases_lock(self):
        task=await self.task("short")
        self.embeddings.entered,self.embeddings.release=asyncio.Event(),asyncio.Event()
        work=asyncio.create_task(self.indexer.process(task))
        await self.embeddings.entered.wait()
        with self.assertRaises(TaskBusy):
            await self.indexer.process(task)
        work.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await work
        self.embeddings.entered=None
        self.assertEqual((await self.indexer.process(task))["status"],"COMPLETED")

    async def test_chunk_insert_rollback_and_permanent_failure(self):
        task=await self.task()
        async with self.pool.connection() as conn:
            await conn.execute("INSERT INTO knowledge_chunk(id,entry_id,chunk_index,content,embedding) VALUES(%s,%s,99,'previous',array_fill(0.01::real,ARRAY[1024])::vector)",(uuid4(),task.entry_id))
            await conn.execute("ALTER TABLE knowledge_chunk ADD CONSTRAINT local_block CHECK(chunk_index<>1)")
        with self.assertRaises(Exception):
            await self.indexer.process(task)
        rows=await self.fetch("SELECT chunk_index,content FROM knowledge_chunk")
        self.assertEqual(rows,[{"chunk_index":99,"content":"previous"}])
        self.assertEqual(await self.fetch("SELECT 1 FROM index_receipt"),[])
        async with self.pool.connection() as conn:
            await conn.execute("ALTER TABLE knowledge_chunk DROP CONSTRAINT local_block")
        self.embeddings.error=EmbeddingError("invalid_embedding_response")
        result=await self.indexer.process(task)
        self.assertEqual(result["status"],"FAILED")
        self.assertEqual(result["error_code"],"invalid_embedding_response")
        self.assertEqual(await self.fetch("SELECT chunk_index,content FROM knowledge_chunk"),rows)

    @unittest.skipUnless(os.environ.get("RUN_RABBITMQ_TESTS")=="1", "requires isolated RabbitMQ")
    async def test_broker_confirm_retry_dead_letter_and_receipt_redelivery(self):
        connection=await aio_pika.connect(BROKER,timeout=5)
        async with connection:
            channel=await connection.channel(publisher_confirms=True,on_return_raises=True)
            name="migration.worker.test."+uuid4().hex
            consumer=Consumer(self.indexer,name,name+".results",retry_delay=0.01)
            await consumer.prepare(channel)
            tasks=await channel.get_queue(name)
            results=await channel.get_queue(name+".results")
            dead=await channel.get_queue(name+".dead")
            try:
                task=await self.task("short")
                await consumer.publish(channel,name,body(task),task)
                self.embeddings.error=EmbeddingError("embedding_rate_limited",True)
                for attempt in range(4):
                    message=await tasks.get(timeout=5,fail=False)
                    while message is None:
                        await asyncio.sleep(0.01)
                        message=await tasks.get(timeout=5,fail=False)
                    await consumer.handle(message,channel)
                result_message=await results.get(timeout=5)
                self.assertEqual(json.loads(result_message.body)["status"],"FAILED")
                await result_message.ack()
                dead_message=await dead.get(timeout=5)
                self.assertEqual(dead_message.headers["failure_code"],"embedding_rate_limited")
                await dead_message.ack()
                self.assertEqual(self.embeddings.calls,4)
                self.embeddings.error=None
                task=await self.task("another")
                await consumer.publish(channel,name,body(task),task)
                message=await tasks.get(timeout=5)
                # Simulate crash after SQL commit but before result publication.
                saved=await self.indexer.process(task)
                before=self.embeddings.calls
                await message.nack(requeue=True)
                redelivery=await tasks.get(timeout=5)
                await consumer.handle(redelivery,channel)
                self.assertEqual(self.embeddings.calls,before)
                result_message=await results.get(timeout=5)
                self.assertEqual(json.loads(result_message.body),saved)
                await result_message.ack()
                await consumer.publish(channel,name,b'{"invalid":true}')
                await consumer.handle(await tasks.get(timeout=5),channel)
                invalid=await dead.get(timeout=5)
                self.assertEqual(invalid.headers["failure_code"],"invalid_index_task")
                await invalid.ack()
            finally:
                for queue in (name,name+".retry",name+".dead",name+".results",name+".results.dead"):
                    await channel.queue_delete(queue)
