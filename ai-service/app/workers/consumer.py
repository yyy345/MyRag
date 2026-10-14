import asyncio
import json
from datetime import timedelta

import aio_pika
from pamqp.commands import Basic

from app.providers.embeddings import EmbeddingError
from app.workers.contracts import IndexTask, InvalidTask
from app.workers.indexer import TaskBusy

TASK_QUEUE = "index.tasks.v1"
RESULT_QUEUE = "index.results.v1"
MAX_RETRIES = 3


class Consumer:
    def __init__(self, indexer, task_queue=TASK_QUEUE, result_queue=RESULT_QUEUE, retry_delay=1.0):
        self.indexer = indexer
        self.tasks, self.results, self.retry_delay = task_queue, result_queue, retry_delay

    async def prepare(self, channel):
        await channel.set_qos(prefetch_count=1)
        await channel.declare_queue(self.tasks, durable=True)
        await channel.declare_queue(self.tasks+".dead", durable=True)
        await channel.declare_queue(self.tasks+".retry", durable=True, arguments={"x-dead-letter-exchange": "", "x-dead-letter-routing-key": self.tasks})
        await channel.declare_queue(self.results+".dead", durable=True)
        await channel.declare_queue(self.results, durable=True, arguments={"x-dead-letter-exchange": "", "x-dead-letter-routing-key": self.results+".dead"})

    async def publish(self, channel, routing_key, body, task=None, headers=None, delay=None):
        message = aio_pika.Message(body, content_type="application/json", delivery_mode=aio_pika.DeliveryMode.PERSISTENT, message_id=str(task.task_id) if task else None, correlation_id=task.trace_id if task else None, headers=headers or {}, expiration=timedelta(seconds=delay) if delay else None)
        confirmed = await channel.default_exchange.publish(message, routing_key=routing_key, mandatory=True, timeout=5)
        if not isinstance(confirmed, Basic.Ack):
            raise RuntimeError("publication_unconfirmed")

    async def handle(self, message, channel):
        task = None
        try:
            task = IndexTask.decode(message.body)
            attempt = (message.headers or {}).get("index_attempt", 0)
            if type(attempt) is not int or attempt < 0 or attempt > MAX_RETRIES:
                raise InvalidTask("invalid_retry_header")
            try:
                result = await self.indexer.process(task)
            except TaskBusy:
                await self.publish(channel, self.tasks+".retry", message.body, task, {"index_attempt": attempt}, self.retry_delay)
                await message.ack()
                return
            except EmbeddingError as error:
                if error.retryable and attempt < MAX_RETRIES:
                    await self.publish(channel, self.tasks+".retry", message.body, task, {"index_attempt": attempt+1}, self.retry_delay*(2**attempt))
                    await message.ack()
                    return
                result = await self.indexer.process(task, terminal_error=error.code)
            if result["status"] == "FAILED":
                await self.publish(channel, self.tasks+".dead", message.body, task, {"failure_code": result["error_code"], "index_attempt": attempt})
            await self.publish(channel, self.results, json.dumps(result, ensure_ascii=False).encode(), task)
            await message.ack()
        except InvalidTask:
            await self.publish(channel, self.tasks+".dead", message.body, task, {"failure_code": "invalid_index_task"})
            await message.ack()
        # Infrastructure failures propagate, closing this connection session.
        # Original unacked delivery is requeued, with no hot retry loop.

    async def run_session(self, connection):
        channel = await connection.channel(publisher_confirms=True, on_return_raises=True)
        async with channel:
            await self.prepare(channel)
            queue = await channel.get_queue(self.tasks)
            async with queue.iterator() as iterator:
                async for message in iterator:
                    await self.handle(message, channel)
