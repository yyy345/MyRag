"""Local-only end-to-end harness. No real provider or storage configuration."""
import asyncio
import base64
from contextlib import asynccontextmanager
from io import BytesIO
import json
import socket
import sys
from urllib.parse import urlsplit
from uuid import uuid4

import aio_pika
import httpx
from PIL import Image
from psycopg.rows import dict_row
from psycopg_pool import AsyncConnectionPool
import uvicorn

from app.main import create_app
from app.providers.embeddings import BailianEmbeddings, EmbeddingConfig
from app.providers.vision import BailianVision, VisionConfig
from app.providers.chat import ChatProvider
from app.providers.config import ChatConfig
from app.rag.generator import Generator
from app.rag.retriever import Retriever
from app.rag.extractor import Extractor
from test_generator import StreamBytes
from app.workers.consumer import Consumer
from app.workers.indexer import Indexer
from app.workers.schema import check_schema
from serve_upload_fixtures import TOKEN, csv_samples, docx_samples, pdf_samples, xlsx_samples


async def mock_model(request):
    payload = json.loads(request.content)
    if payload.get("stream") or payload.get("model") == "deepseek-v4-flash":
        assert payload["model"] == "deepseek-v4-flash"
        assert payload["thinking"] == {"type": "disabled"}
    if payload.get("stream"):
        frames = [
            {"id":"local","object":"chat.completion.chunk","created":0,"model":"qwen-plus","choices":[{"index":0,"delta":{"role":"assistant","content":"中文"},"finish_reason":None}]},
            {"id":"local","object":"chat.completion.chunk","created":0,"model":"qwen-plus","choices":[{"index":0,"delta":{"content":"🙂回答"},"finish_reason":None}]},
            {"id":"local","object":"chat.completion.chunk","created":0,"model":"qwen-plus","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}},
        ]
        for frame in frames:
            frame["model"] = payload["model"]
        return httpx.Response(200,headers={"Content-Type":"text/event-stream"},stream=StreamBytes(frames))
    if request.url.path.endswith("/embeddings"):
        # Fixed failure injection gives visible FAILED terminal status.
        if any("MOCK_EMBEDDING_FAILURE" in text for text in payload["input"]):
            return httpx.Response(400, json={"error": {"message": "private mock failure", "type": "bad_request"}})
        return httpx.Response(200, json={"object": "list", "model": "text-embedding-v3", "data": [{"object": "embedding", "index": i, "embedding": [0.01]*1024} for i in range(len(payload["input"]))], "usage": {"prompt_tokens": 1, "total_tokens": 1}})
    first = payload.get("messages",[{}])[0].get("content","")
    content = "## 固定提炼\n"+"知识要点"*60 if isinstance(first,str) and first.startswith("你是一个知识提炼专家") else "固定图片描述🙂"
    return httpx.Response(200, json={"id": "local", "object": "chat.completion", "created": 0, "model": payload["model"], "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}]})


def build_app(database, task_queue, result_queue):
    embeddings = BailianEmbeddings(EmbeddingConfig("local-mock-only", "http://mock.local/v1"), httpx.MockTransport(mock_model))
    vision = BailianVision(VisionConfig("local-mock-only", "http://mock.local/v1"), httpx.MockTransport(mock_model))
    chat = ChatProvider(ChatConfig("local-mock-only", "http://mock.local/v1", "deepseek-v4-flash", provider="deepseek"), httpx.MockTransport(mock_model))
    app = create_app(TOKEN, vision=vision,extractor=Extractor(chat))
    previous_lifespan = app.router.lifespan_context

    @asynccontextmanager
    async def lifespan(app):
        pool = AsyncConnectionPool(database, open=False, min_size=1, max_size=4, kwargs={"autocommit": True, "row_factory": dict_row})
        await pool.open(wait=True)
        app.state.rag = Generator(Retriever(pool,embeddings),chat)
        connection = await aio_pika.connect("amqp://guest:guest@127.0.0.1:55673/", timeout=5)
        consumer = Consumer(Indexer(pool, embeddings), task_queue, result_queue, retry_delay=0.01)
        async with connection:
            channel = await connection.channel(publisher_confirms=True, on_return_raises=True)
            await consumer.prepare(channel)
            async with pool.connection() as conn:
                await check_schema(conn)
            work = asyncio.create_task(consumer.run_session(connection))
            try:
                async with previous_lifespan(app):
                    yield
            finally:
                work.cancel()
                await asyncio.gather(work, return_exceptions=True)
                await channel.close()
                await embeddings.close()
                await vision.close()
                await chat.close()
                await pool.close()
    app.router.lifespan_context = lifespan
    return app


if __name__ == "__main__":
    database = sys.argv[1]
    parsed = urlsplit(database)
    if parsed.hostname != "127.0.0.1" or parsed.port != 55432 or not parsed.path.startswith("/migration_c26_"):
        raise SystemExit("harness requires an isolated generated migration_c26_ database")
    if sys.platform == "win32":
        asyncio.set_event_loop_policy(asyncio.WindowsSelectorEventLoopPolicy())
    name = "migration.c26."+uuid4().hex
    listener = socket.socket()
    listener.bind(("127.0.0.1",0))
    files = [("中文.txt", "知识库中文🙂".encode()), ("中文.md", "# 中文标题".encode("gbk")), ("chinese.pdf", pdf_samples()["chinese.PDF"]), ("table.docx", docx_samples()["table.docx"]), ("multiline.csv", csv_samples()["multiline.csv"]), ("formula.xlsx", xlsx_samples()["formula.xlsx"])]
    buffer = BytesIO()
    Image.new("RGB",(2,2),"green").save(buffer,format="PNG")
    data = {"url": f"http://127.0.0.1:{listener.getsockname()[1]}", "task_queue": name, "result_queue": name+".results", "files": [{"filename": filename, "content": base64.b64encode(content).decode()} for filename,content in files], "image": base64.b64encode(buffer.getvalue()).decode()}
    print(json.dumps(data,ensure_ascii=True),flush=True)
    app = build_app(database,name,name+".results")
    server = uvicorn.Server(uvicorn.Config(app, log_level="critical", access_log=False))
    with asyncio.Runner(loop_factory=asyncio.SelectorEventLoop if sys.platform == "win32" else asyncio.new_event_loop) as runner:
        runner.run(server.serve(sockets=[listener]))
