import asyncio
import json

from fastapi import HTTPException
from starlette.responses import StreamingResponse

from app.providers.chat import ChatError
from app.providers.embeddings import EmbeddingError
from app.rag.retriever import RetrievalScopeError


async def chat_response(request,payload,generator):
    if generator is None:
        raise HTTPException(503,detail="chat_disabled")
    try:
        await generator.authorize(payload)
    except RetrievalScopeError:
        raise HTTPException(403,detail="unauthorized_retrieval_scope") from None
    except Exception:
        raise HTTPException(503,detail="retrieval_unavailable") from None

    async def stream():
        source=generator.stream(payload)
        work=None
        async def disconnected():
            while True:
                if (await request.receive())["type"]=="http.disconnect":
                    return
        disconnect=asyncio.create_task(disconnected())
        try:
            while True:
                work=asyncio.create_task(anext(source,None))
                done,_=await asyncio.wait([work,disconnect],return_when=asyncio.FIRST_COMPLETED)
                if work not in done:
                    break
                event=work.result()
                if event is None:
                    break
                yield "data: "+json.dumps(event,ensure_ascii=False)+"\n\n"
        except (ChatError,EmbeddingError) as error:
            yield "data: "+json.dumps({"type":"error","request_id":str(payload.request_id),"code":error.code})+"\n\n"
        except Exception:
            yield "data: "+json.dumps({"type":"error","request_id":str(payload.request_id),"code":"chat_unavailable"})+"\n\n"
        finally:
            if work is not None:
                work.cancel()
            disconnect.cancel()
            await asyncio.gather(*([work] if work else []),disconnect,return_exceptions=True)
            await source.aclose()
    return StreamingResponse(stream(),media_type="text/event-stream",headers={"Cache-Control":"no-cache","X-Accel-Buffering":"no"})
