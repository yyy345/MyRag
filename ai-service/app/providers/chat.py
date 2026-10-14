import asyncio
import math
import os
from contextlib import aclosing

import httpx
from langchain_openai import ChatOpenAI
from langsmith import tracing_context
from openai import APITimeoutError, RateLimitError

from app.providers.vision import VisionConfig
from app.providers.config import ChatConfig
from app.rag.chunker import utf16_length


class ChatError(Exception):
    def __init__(self,code):
        super().__init__(code)
        self.code=code


class ChatProvider:
    def __init__(self,config: ChatConfig | VisionConfig,transport=None,model=None):
        self._config,self._transport=config,transport
        self._model_name=model or (config.model if isinstance(config,ChatConfig) else os.environ.get("BAILIAN_LLM_MODEL","qwen-plus"))
        self._timeout=config.timeout if isinstance(config,ChatConfig) else 120
        self._thinking=getattr(config,"thinking_enabled",False)
        self._extra_body={"max_tokens":2048}
        if getattr(config,"provider","bailian")=="deepseek":
            self._extra_body["thinking"]={"type":"enabled" if self._thinking else "disabled"}
        self._http=httpx.Client(trust_env=False,follow_redirects=False,timeout=self._timeout)
        self._async_http=httpx.AsyncClient(trust_env=False,follow_redirects=False,timeout=self._timeout,transport=transport)
        self._model=self._build_model(self._async_http)
        self._slots=asyncio.Semaphore(2)

    def _build_model(self,client):
        return ChatOpenAI(model=self._model_name,api_key=self._config.api_key,base_url=self._config.base_url,temperature=None if self._thinking else 0.3,extra_body=self._extra_body,max_retries=0,timeout=self._timeout,http_client=self._http,http_async_client=client,use_responses_api=False)

    async def close(self):
        self._http.close()
        await self._async_http.aclose()

    async def complete(self,messages,temperature=0.3):
        try:
            async with asyncio.timeout(self._timeout):
                async with self._slots:
                    with tracing_context(enabled=False):
                        model=self._model if self._thinking else self._model.bind(temperature=temperature)
                        result=await model.ainvoke(messages)
            text=result.content
            if not isinstance(text,str) or "\x00" in text or len(text)>200_000 or result.response_metadata.get("finish_reason")!="stop":
                raise ChatError("invalid_extraction_response")
            return text
        except (TimeoutError,APITimeoutError):
            raise ChatError("chat_timeout") from None
        except RateLimitError:
            raise ChatError("chat_rate_limited") from None
        except ChatError:
            raise
        except Exception:
            raise ChatError("chat_unavailable") from None

    async def stream(self,messages):
        content=""
        usage=None
        finished=False
        responses=[]
        async def capture(response):
            responses.append(response)
        # Own the HTTP response for this stream. LangChain's nested async
        # generators can defer closing it when validation stops iteration early.
        client=httpx.AsyncClient(trust_env=False,follow_redirects=False,timeout=self._timeout,transport=self._transport,event_hooks={"response":[capture]})
        model=self._build_model(client)
        try:
            async with asyncio.timeout(self._timeout):
                async with self._slots:
                    with tracing_context(enabled=False):
                        async with aclosing(model.astream(messages,stream_options={"include_usage":True})) as source:
                            async for chunk in source:
                                reason=chunk.response_metadata.get("finish_reason")
                                if reason is not None:
                                    if reason!="stop":
                                        raise ChatError("incomplete_chat_response")
                                    finished=True
                                text=chunk.content
                                if isinstance(text,list):
                                    text="".join(item.get("text","") for item in text if isinstance(item,dict) and isinstance(item.get("text",""),str))
                                if not isinstance(text,str) or "\x00" in text:
                                    raise ChatError("invalid_chat_response")
                                if text:
                                    content+=text
                                    if len(content)>200_000:
                                        raise ChatError("chat_response_limit")
                                    yield {"type":"chunk","content":text}
                                if chunk.usage_metadata:
                                    raw=chunk.usage_metadata
                                    incoming,outgoing=raw.get("input_tokens"),raw.get("output_tokens")
                                    if type(incoming) is int and type(outgoing) is int and incoming>=0 and outgoing>=0:
                                        usage={"prompt_tokens":incoming,"completion_tokens":outgoing}
        except (TimeoutError,APITimeoutError):
            raise ChatError("chat_timeout") from None
        except RateLimitError:
            raise ChatError("chat_rate_limited") from None
        except ChatError:
            raise
        except Exception:
            raise ChatError("chat_unavailable") from None
        finally:
            for response in responses:
                await response.aclose()
            await client.aclose()
        if not content.strip() or not finished:
            raise ChatError("invalid_chat_response")
        if usage is None:
            usage={"prompt_tokens":math.ceil(sum(utf16_length(message["content"]) for message in messages)/2),"completion_tokens":math.ceil(utf16_length(content)/2)}
        yield {"type":"done","usage":usage}


# Compatibility for existing local fixtures and earlier imports.
BailianChat = ChatProvider
