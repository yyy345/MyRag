"""Explicit worker entry point; never starts alongside the HTTP API."""
import asyncio
import logging
import os
import signal
import sys

import aio_pika
from psycopg.rows import dict_row
from psycopg_pool import AsyncConnectionPool

from app.providers.embeddings import BailianEmbeddings, EmbeddingConfig
from app.workers.consumer import Consumer
from app.workers.indexer import Indexer
from app.workers.schema import check_schema


async def main():
    config = EmbeddingConfig.from_env()
    if config is None:
        raise ValueError("worker requires explicit AI_PROVIDER_ENABLED=true")
    database, broker = os.environ.get("DATABASE_URL", ""), os.environ.get("RABBITMQ_URL", "")
    if not database or not broker:
        raise ValueError("worker requires DATABASE_URL and RABBITMQ_URL")
    pool = AsyncConnectionPool(database, open=False, min_size=1, max_size=4, timeout=10, kwargs={"autocommit": True, "row_factory": dict_row, "connect_timeout": 5, "options": "-c statement_timeout=30000 -c lock_timeout=10000"})
    embeddings = BailianEmbeddings(config)
    running = asyncio.current_task()
    loop = asyncio.get_running_loop()
    for signum in (signal.SIGINT, signal.SIGTERM):
        try:
            loop.add_signal_handler(signum, running.cancel)
        except NotImplementedError:
            signal.signal(signum, lambda _signum, _frame: loop.call_soon_threadsafe(running.cancel))
    try:
        await pool.open(wait=True, timeout=10)
        async with pool.connection() as conn:
            await check_schema(conn)
        consumer = Consumer(Indexer(pool, embeddings))
        failures = 0
        while True:
            try:
                connection = await aio_pika.connect(broker, timeout=5)
                async with connection:
                    await consumer.run_session(connection)
                failures = 0
            except asyncio.CancelledError:
                raise
            except Exception:
                failures += 1
                logging.warning("index worker session failed; unacknowledged tasks remain pending")
                await asyncio.sleep(min(30, 2**min(failures, 5)))
    finally:
        await embeddings.close()
        await pool.close()


if __name__ == "__main__":
    if sys.platform == "win32":
        asyncio.set_event_loop_policy(asyncio.WindowsSelectorEventLoopPolicy())
    try:
        asyncio.run(main())
    except (KeyboardInterrupt, asyncio.CancelledError):
        pass
    except Exception:
        raise SystemExit("worker startup failed; verify explicit configuration and migration") from None
