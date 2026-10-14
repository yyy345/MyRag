import os
import secrets
import asyncio
from contextlib import asynccontextmanager
from typing import Annotated
from uuid import UUID
from fastapi import APIRouter, Depends, FastAPI, File, Form, Header, HTTPException, UploadFile, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from starlette.concurrency import run_in_threadpool
from app.api.schemas import ChatRequest, DescribeRequest, ExtractRequest
from app.api.body_limit import RequestBodyLimitMiddleware
from app.ingestion.parser import MAX_DOCUMENT_BYTES, ParseError, parse_document
from app.providers.vision import VisionProvider, VisionConfig, VisionError, validate_image
from app.api.streaming import chat_response


def create_app(token: str | None = None, vision=None, rag=None, extractor=None) -> FastAPI:
    service_token = token if token is not None else os.environ.get("AI_SERVICE_TOKEN", "")
    if len(service_token) < 32 or any(not 33 <= ord(char) <= 126 for char in service_token):
        raise ValueError("AI_SERVICE_TOKEN must contain at least 32 printable non-space ASCII characters")
    @asynccontextmanager
    async def lifespan(app):
        from app.providers.config import ChatConfig
        from app.providers.chat import ChatProvider
        from app.providers.embeddings import BailianEmbeddings, EmbeddingConfig
        from app.rag.extractor import Extractor
        vision_config = VisionConfig.from_env() if vision is None else None
        chat_config = ChatConfig.from_env() if rag is None or extractor is None else None
        database = os.environ.get("DATABASE_URL")
        configure_rag = chat_config is not None and rag is None and bool(database)
        retrieval_mode = os.environ.get("AI_RETRIEVAL_MODE", "hybrid").strip().lower()
        if configure_rag and retrieval_mode not in ("hybrid", "fulltext"):
            raise ValueError("AI_RETRIEVAL_MODE must be hybrid or fulltext")
        embedding_config = EmbeddingConfig.from_env() if configure_rag and retrieval_mode == "hybrid" else None
        pool = embeddings = chat_model = provider = None
        try:
            if vision_config is not None:
                provider = VisionProvider(vision_config)
                app.state.vision = provider
            if chat_config is not None:
                chat_model = ChatProvider(chat_config)
                if extractor is None:
                    app.state.extractor = Extractor(chat_model)
            if configure_rag:
                from psycopg.rows import dict_row
                from psycopg_pool import AsyncConnectionPool
                from app.rag.retriever import Retriever
                from app.rag.generator import Generator
                from app.workers.schema import check_schema
                pool = AsyncConnectionPool(database,open=False,min_size=1,max_size=4,timeout=10,kwargs={"autocommit":True,"row_factory":dict_row,"connect_timeout":5,"options":"-c statement_timeout=30000"})
                await pool.open(wait=True,timeout=10)
                async with pool.connection() as conn:
                    await check_schema(conn)
                if embedding_config is not None:
                    embeddings = BailianEmbeddings(embedding_config)
                app.state.rag = Generator(Retriever(pool,embeddings),chat_model)
            yield
        finally:
            if provider is not None:
                await provider.close()
            if chat_model is not None:
                await chat_model.close()
            if embeddings is not None:
                await embeddings.close()
            if pool is not None:
                await pool.close()

    app = FastAPI(title="AI Library internal API", docs_url=None, redoc_url=None, openapi_url=None, lifespan=lifespan)
    app.state.vision = vision
    app.state.rag = rag
    app.state.extractor = extractor
    # File content remains limited to 20 MiB; allow 1 MiB for multipart fields
    # and headers while bounding total bytes before multipart spooling.
    app.add_middleware(RequestBodyLimitMiddleware, max_bytes=MAX_DOCUMENT_BYTES + 1024 * 1024)

    def authenticate(authorization: str = Header(default="")) -> None:
        if not secrets.compare_digest(authorization.encode(), f"Bearer {service_token}".encode()):
            raise HTTPException(status_code=401, detail="invalid service credentials")

    @app.exception_handler(RequestValidationError)
    async def validation_error(_request, error):
        # FastAPI's default validation details echo the input document; avoid that.
        return JSONResponse(status_code=422, content={"detail": [{"loc": list(item["loc"]), "type": item["type"]} for item in error.errors()]})

    @app.get("/health/live")
    def live():
        return {"service": "ai-api", "status": "alive"}

    @app.get("/health/ready")
    async def ready():
        if app.state.vision is None or app.state.rag is None or app.state.extractor is None:
            raise HTTPException(status_code=503,detail="providers_disabled_or_unavailable")
        try:
            async with asyncio.timeout(2):
                async with app.state.rag.retriever.pool.connection() as conn:
                    await conn.execute("SELECT 1")
        except Exception:
            raise HTTPException(status_code=503,detail="database_unavailable") from None
        return {"service":"ai-api","status":"ready"}

    router = APIRouter(prefix="/internal/v1", dependencies=[Depends(authenticate)])

    @router.post("/parse")
    async def parse(file: Annotated[UploadFile, File()], request_id: Annotated[UUID, Form()]):
        try:
            content = await file.read(MAX_DOCUMENT_BYTES + 1)
            text = await run_in_threadpool(parse_document, content, file.filename or "")
            return {"request_id": str(request_id), "text": text, "metadata": {"filename": file.filename, "characters": len(text)}}
        except ParseError as error:
            raise HTTPException(status_code=error.status, detail={"code": error.code, "message": str(error)}) from None
        finally:
            await file.close()

    @router.post("/describe-image")
    async def describe(payload: DescribeRequest, request: Request):
        try:
            await run_in_threadpool(validate_image, payload.image_base64, payload.media_type)
            if app.state.vision is None:
                raise VisionError("vision_disabled", 503)
            async def disconnected():
                while True:
                    if (await request.receive())["type"]=="http.disconnect":
                        return

            work = asyncio.create_task(app.state.vision.describe(payload.image_base64, payload.media_type, payload.context))
            disconnect = asyncio.create_task(disconnected())
            try:
                done, _ = await asyncio.wait([work, disconnect], return_when=asyncio.FIRST_COMPLETED)
                if work not in done:
                    raise VisionError("request_cancelled", 499)
                return {"request_id": str(payload.request_id), "description": work.result()}
            finally:
                work.cancel()
                disconnect.cancel()
                await asyncio.gather(work, disconnect, return_exceptions=True)
        except VisionError as error:
            raise HTTPException(status_code=error.status, detail={"code": error.code}) from None

    @router.post("/chat/stream")
    async def chat(payload: ChatRequest, request: Request):
        return await chat_response(request,payload,app.state.rag)

    @router.post("/extract")
    async def extract(payload: ExtractRequest, request: Request):
        from app.providers.chat import ChatError
        if app.state.extractor is None:
            raise HTTPException(503,detail="extraction_disabled")
        async def disconnected():
            while True:
                if (await request.receive())["type"]=="http.disconnect":
                    return
        work=asyncio.create_task(app.state.extractor.extract(payload))
        disconnect=asyncio.create_task(disconnected())
        try:
            done,_=await asyncio.wait([work,disconnect],return_when=asyncio.FIRST_COMPLETED)
            if work not in done:
                raise HTTPException(499,detail="request_cancelled")
            return {"request_id":str(payload.request_id),"sections":work.result()}
        except ChatError as error:
            raise HTTPException(503,detail={"code":error.code}) from None
        finally:
            work.cancel()
            disconnect.cancel()
            await asyncio.gather(work,disconnect,return_exceptions=True)

    app.include_router(router)
    return app
