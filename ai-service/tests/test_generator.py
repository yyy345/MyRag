import json
import asyncio
import unittest
from uuid import uuid4

import httpx
from app.api.schemas import ChatRequest
from app.providers.chat import BailianChat, ChatError
from app.providers.vision import VisionConfig
from app.rag.generator import DEFAULT_SYSTEM_PROMPT, build_prompt


class StreamBytes(httpx.AsyncByteStream):
    def __init__(self,frames):
        self.frames=frames
        self.closed=False
    async def __aiter__(self):
        raw="".join("data: "+json.dumps(frame,ensure_ascii=False)+"\n\n" for frame in self.frames)+"data: [DONE]\n\n"
        encoded=raw.encode()
        for offset in range(0,len(encoded),7):
            yield encoded[offset:offset+7]
    async def aclose(self):
        self.closed=True


class PromptTests(unittest.TestCase):
    def test_prompt_history_custom_system_and_references(self):
        payload=ChatRequest(request_id=uuid4(),user_id=uuid4(),kb_id=uuid4(),query="问题",history=[{"role":"user","content":"上一问题"},{"role":"assistant","content":"上一回答"}])
        hits=[{"entry_id":"entry","entry_title":"资料","content":"中"*199+"🙂尾"}]
        messages,refs=build_prompt(payload,hits)
        self.assertEqual(messages[0]["content"],DEFAULT_SYSTEM_PROMPT)
        self.assertEqual([message["role"] for message in messages],["system","user","assistant","user"])
        self.assertTrue(messages[-1]["content"].startswith("参考资料：\n[参考资料1]\n标题: 资料\n内容:"))
        self.assertTrue(messages[-1]["content"].endswith("\n\n用户问题：问题"))
        self.assertEqual(refs,[{"id":"entry","title":"资料","content":"中"*199}])
        payload.system_prompt="自定义系统提示"
        self.assertEqual(build_prompt(payload,[])[0][0]["content"],"自定义系统提示")


class ChatProviderTests(unittest.IsolatedAsyncioTestCase):
    async def test_missing_finish_and_truncated_completion_are_rejected(self):
        for reason in (None,"length"):
            stream=StreamBytes([{"id":"local","object":"chat.completion.chunk","created":0,"model":"qwen-plus","choices":[{"index":0,"delta":{"content":"部分回答"},"finish_reason":reason}]}])
            provider=BailianChat(VisionConfig("local","http://mock.local/v1"),httpx.MockTransport(lambda _:httpx.Response(200,headers={"Content-Type":"text/event-stream"},stream=stream)))
            try:
                with self.assertRaises(ChatError):
                    [event async for event in provider.stream([{"role":"user","content":"问题"}])]
                self.assertTrue(stream.closed,reason)
            finally:
                await provider.close()

    async def test_sdk_cancellation_closes_pending_response(self):
        entered=asyncio.Event()
        class Blocking(httpx.AsyncByteStream):
            closed=False
            async def __aiter__(self):
                entered.set()
                await asyncio.Event().wait()
                yield b"never"
            async def aclose(self):self.closed=True
        stream=Blocking()
        provider=BailianChat(VisionConfig("local","http://mock.local/v1"),httpx.MockTransport(lambda _:httpx.Response(200,headers={"Content-Type":"text/event-stream"},stream=stream)))
        source=provider.stream([{"role":"user","content":"问题"}])
        work=asyncio.create_task(anext(source))
        try:
            await asyncio.wait_for(entered.wait(),2)
            work.cancel()
            with self.assertRaises(asyncio.CancelledError):await work
            await source.aclose()
            self.assertTrue(stream.closed)
        finally:
            work.cancel()
            await asyncio.gather(work,return_exceptions=True)
            await provider.close()
    async def test_real_sdk_stream_frames_utf8_usage_and_parameters(self):
        stream=StreamBytes([
            {"id":"local","object":"chat.completion.chunk","created":0,"model":"qwen-plus","choices":[{"index":0,"delta":{"role":"assistant","content":"中文"},"finish_reason":None}]},
            {"id":"local","object":"chat.completion.chunk","created":0,"model":"qwen-plus","choices":[{"index":0,"delta":{"content":"🙂回答"},"finish_reason":None}]},
            {"id":"local","object":"chat.completion.chunk","created":0,"model":"qwen-plus","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}},
        ])
        def handler(request):
            body=json.loads(request.content)
            self.assertEqual(body["model"],"qwen-plus")
            self.assertEqual(body["temperature"],0.3)
            self.assertEqual(body["max_tokens"],2048)
            self.assertTrue(body["stream"])
            self.assertEqual(body["stream_options"],{"include_usage":True})
            return httpx.Response(200,headers={"Content-Type":"text/event-stream"},stream=stream)
        provider=BailianChat(VisionConfig("local-key","http://mock.local/v1"),httpx.MockTransport(handler),model="qwen-plus")
        try:
            events=[event async for event in provider.stream([{"role":"user","content":"问题"}])]
            self.assertEqual("".join(event.get("content","") for event in events),"中文🙂回答")
            self.assertEqual(events[-1],{"type":"done","usage":{"prompt_tokens":12,"completion_tokens":4}})
            self.assertTrue(stream.closed)
        finally:
            await provider.close()

    async def test_model_errors_are_safe_and_not_retried(self):
        for status in (429,500):
            calls=[]
            def handler(request):
                calls.append(request)
                return httpx.Response(status,json={"error":{"message":"private key document","type":"error"}})
            provider=BailianChat(VisionConfig("local-key","http://mock.local/v1"),httpx.MockTransport(handler))
            try:
                with self.assertRaises(ChatError) as raised:
                    [event async for event in provider.stream([{"role":"user","content":"问题"}])]
                self.assertNotIn("private",str(raised.exception))
                self.assertEqual(len(calls),1)
            finally:
                await provider.close()
