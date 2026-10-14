"""DeepSeek wire/configuration tests: all requests terminate in MockTransport."""
import base64
import json
import os
import unittest
from unittest.mock import patch
from uuid import uuid4

import httpx
from fastapi.testclient import TestClient

from app.api.schemas import ExtractRequest
from app.main import create_app
from app.providers.chat import ChatError, ChatProvider
from app.providers.config import ChatConfig
from app.providers.embeddings import EmbeddingConfig
from app.providers.vision import BailianVision, VisionConfig
from app.rag.extractor import Extractor
from test_generator import StreamBytes
from test_vision import image_data


ENABLED = {
    "AI_PROVIDER_ENABLED": "true", "AI_CHAT_PROVIDER": "deepseek",
    "AI_VISION_PROVIDER": "deepseek", "DEEPSEEK_API_KEY": "mock-deepseek-key",
    "BAILIAN_API_KEY": "mock-embedding-key",
}


def completion(content, model="deepseek-v4-flash", reason="stop"):
    return httpx.Response(200, json={
        "id": "local", "object": "chat.completion", "created": 0, "model": model,
        "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": reason}],
    })


class DeepSeekConfigTests(unittest.TestCase):
    def test_capabilities_keep_distinct_models_keys_and_endpoints(self):
        with patch.dict(os.environ, ENABLED, clear=True):
            chat, vision, embedding = ChatConfig.from_env(), VisionConfig.from_env(), EmbeddingConfig.from_env()
        self.assertEqual((chat.model, chat.base_url, chat.api_key), ("deepseek-v4-flash", "https://api.deepseek.com", "mock-deepseek-key"))
        self.assertFalse(chat.thinking_enabled)
        self.assertEqual((vision.model, vision.api_key), ("deepseek-flash", "mock-deepseek-key"))
        self.assertEqual((embedding.model, embedding.api_key), ("text-embedding-v3", "mock-embedding-key"))
        self.assertIn("dashscope.aliyuncs.com", embedding.base_url)
        self.assertNotIn("mock-deepseek-key", repr(chat))
        self.assertNotIn("mock-deepseek-key", repr(vision))

    def test_disabled_legacy_and_embedding_without_chat_or_vision_credentials(self):
        with patch.dict(os.environ, {"AI_PROVIDER_ENABLED": "false", "AI_CHAT_PROVIDER": "bad"}, clear=True):
            self.assertIsNone(ChatConfig.from_env())
            self.assertIsNone(VisionConfig.from_env())
            self.assertIsNone(EmbeddingConfig.from_env())
        with patch.dict(os.environ, {"AI_PROVIDER_ENABLED": "true", "BAILIAN_API_KEY": "mock-legacy-key"}, clear=True):
            self.assertEqual(ChatConfig.from_env().model, "qwen-plus")
            self.assertEqual(VisionConfig.from_env().model, "qwen-vl-plus")
        with patch.dict(os.environ, ENABLED | {"DEEPSEEK_API_KEY": "", "AI_VISION_PROVIDER": "disabled"}, clear=True):
            self.assertIsNone(VisionConfig.from_env())
            self.assertEqual(EmbeddingConfig.from_env().api_key, "mock-embedding-key")
            with self.assertRaises(ValueError):
                ChatConfig.from_env()  # Never falls back to Bailian's key/model.

    def test_overrides_and_configuration_errors_are_redacted(self):
        with patch.dict(os.environ, ENABLED | {"DEEPSEEK_LLM_MODEL": "deepseek-flash", "DEEPSEEK_THINKING_ENABLED": "true"}, clear=True):
            self.assertEqual(ChatConfig.from_env().model, "deepseek-flash")
            self.assertTrue(ChatConfig.from_env().thinking_enabled)
        for change in (
            {"AI_PROVIDER_ENABLED": "invalid"}, {"AI_CHAT_PROVIDER": "unknown"},
            {"DEEPSEEK_API_KEY": ""}, {"DEEPSEEK_API_KEY": "private key"},
            {"DEEPSEEK_BASE_URL": "https://user:private-secret@api.deepseek.com"},
            {"DEEPSEEK_BASE_URL": "https://api.deepseek.com?key=private-secret"},
            {"DEEPSEEK_BASE_URL": "https://api.deepseek.com:bad"},
            {"DEEPSEEK_LLM_MODEL": " "}, {"DEEPSEEK_THINKING_ENABLED": "maybe"},
        ):
            with self.subTest(change=tuple(change)), patch.dict(os.environ, ENABLED | change, clear=True):
                with self.assertRaises(ValueError) as raised:
                    ChatConfig.from_env()
                self.assertNotIn("private", str(raised.exception))
                self.assertNotIn("mock-deepseek-key", str(raised.exception))
        with patch.dict(os.environ, ENABLED | {"BAILIAN_API_KEY": ""}, clear=True):
            with self.assertRaises(ValueError):
                EmbeddingConfig.from_env()  # No DeepSeek embeddings endpoint fallback.


class DeepSeekProviderTests(unittest.IsolatedAsyncioTestCase):
    async def test_sdk_stream_alias_utf8_usage_and_reasoning_not_exposed(self):
        stream = StreamBytes([
            {"id": "local", "object": "chat.completion.chunk", "created": 0, "model": "deepseek-flash", "choices": [{"index": 0, "delta": {"role": "assistant", "reasoning_content": "internal-only"}, "finish_reason": None}]},
            {"id": "local", "object": "chat.completion.chunk", "created": 0, "model": "deepseek-flash", "choices": [{"index": 0, "delta": {"content": "中文🙂回答"}, "finish_reason": None}]},
            {"id": "local", "object": "chat.completion.chunk", "created": 0, "model": "deepseek-flash", "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 12, "completion_tokens": 4, "total_tokens": 16, "prompt_cache_hit_tokens": 8, "prompt_cache_miss_tokens": 4}},
        ])
        calls = []
        def handler(request):
            calls.append(request)
            self.assertEqual(str(request.url), "https://api.deepseek.com/chat/completions")
            self.assertEqual(request.headers["Authorization"], "Bearer mock-deepseek-key")
            body = json.loads(request.content)
            self.assertEqual(body["model"], "deepseek-v4-flash")
            self.assertEqual(body["thinking"], {"type": "disabled"})
            self.assertEqual(body["max_tokens"], 2048)
            self.assertEqual(body["stream_options"], {"include_usage": True})
            return httpx.Response(200, headers={"Content-Type": "text/event-stream"}, stream=stream)
        with patch.dict(os.environ, ENABLED, clear=True):
            provider = ChatProvider(ChatConfig.from_env(), httpx.MockTransport(handler))
        try:
            events = [event async for event in provider.stream([{"role": "user", "content": "问题"}])]
            self.assertEqual("".join(event.get("content", "") for event in events), "中文🙂回答")
            self.assertEqual(events[-1], {"type": "done", "usage": {"prompt_tokens": 12, "completion_tokens": 4}})
            self.assertNotIn("internal-only", json.dumps(events))
            self.assertTrue(stream.closed)
            self.assertEqual(len(calls), 1)
        finally:
            await provider.close()

    async def test_extraction_and_explicit_thinking_mode(self):
        for thinking in (False, True):
            requests = []
            def handler(request):
                body = json.loads(request.content)
                requests.append(body)
                self.assertEqual(body["model"], "deepseek-v4-flash")
                self.assertEqual(body["thinking"], {"type": "enabled" if thinking else "disabled"})
                self.assertEqual(body["max_tokens"], 2048)
                if thinking:
                    self.assertNotIn("temperature", body)
                return completion("## 标题\n" + "知识" * 120)
            with patch.dict(os.environ, ENABLED | {"DEEPSEEK_THINKING_ENABLED": str(thinking).lower()}, clear=True):
                provider = ChatProvider(ChatConfig.from_env(), httpx.MockTransport(handler))
            try:
                payload = ExtractRequest(request_id=uuid4(), history=[{"role": "user", "content": "问题一"}, {"role": "assistant", "content": "回答"}, {"role": "user", "content": "问题二"}])
                extractor = Extractor(provider)
                self.assertEqual(await extractor.extract(payload), [{"title": "标题", "content": "知识" * 120}])
                payload.mode = "scheduled"
                await extractor.extract(payload)
                if not thinking:
                    self.assertEqual([body["temperature"] for body in requests], [0.3, 0.5])
            finally:
                await provider.close()

    async def test_errors_no_retry_no_fallback_and_incomplete_response_rejected(self):
        for status in (401, 429, 500, 200):
            calls = []
            def handler(request):
                calls.append(request)
                self.assertEqual(request.url.host, "api.deepseek.com")
                if status == 200:
                    return completion("部分知识", reason="length")
                return httpx.Response(status, json={"error": {"message": "mock-deepseek-key private document", "type": "error"}})
            with patch.dict(os.environ, ENABLED, clear=True):
                provider = ChatProvider(ChatConfig.from_env(), httpx.MockTransport(handler))
            try:
                with self.assertRaises(ChatError) as raised:
                    await provider.complete([{"role": "user", "content": "问题"}])
                self.assertNotIn("private", str(raised.exception))
                self.assertNotIn("mock-deepseek-key", str(raised.exception))
                self.assertEqual(len(calls), 1)
            finally:
                await provider.close()

    async def test_deepseek_vision_model_and_inline_image(self):
        encoded = base64.b64encode(image_data()).decode()
        def handler(request):
            body = json.loads(request.content)
            self.assertEqual(str(request.url), "https://api.deepseek.com/chat/completions")
            self.assertEqual(body["model"], "deepseek-flash")
            self.assertEqual(body["thinking"], {"type": "disabled"})
            self.assertEqual(body["messages"][0]["content"][1]["image_url"]["url"], "data:image/png;base64," + encoded)
            return completion("红色方块🙂", model="deepseek-flash")
        with patch.dict(os.environ, ENABLED, clear=True):
            provider = BailianVision(VisionConfig.from_env(), httpx.MockTransport(handler))
        try:
            self.assertEqual(await provider.describe(encoded, "image/png"), "红色方块🙂")
        finally:
            await provider.close()


class DeepSeekApplicationTests(unittest.TestCase):
    def test_lifespan_connects_extraction_without_vision_or_embedding_key(self):
        calls = []
        def handler(request):
            calls.append(json.loads(request.content))
            self.assertEqual(request.url.host, "api.deepseek.com")
            return completion("## 标题\n" + "知识" * 120)
        def factory(config):
            return ChatProvider(config, httpx.MockTransport(handler))
        env = ENABLED | {"AI_VISION_PROVIDER": "disabled", "BAILIAN_API_KEY": ""}
        token = "local-token-" + "x" * 32
        with patch.dict(os.environ, env, clear=True), patch("app.providers.chat.ChatProvider", side_effect=factory), TestClient(create_app(token)) as client:
            payload = {"request_id": str(uuid4()), "history": [{"role": "user", "content": "问题一"}, {"role": "assistant", "content": "回答"}, {"role": "user", "content": "问题二"}]}
            response = client.post("/internal/v1/extract", json=payload, headers={"Authorization": "Bearer " + token})
            self.assertEqual(response.status_code, 200)
            self.assertEqual(response.json()["sections"], [{"title": "标题", "content": "知识" * 120}])
            self.assertEqual(calls[0]["model"], "deepseek-v4-flash")
            self.assertEqual(client.get("/health/live").status_code, 200)
            self.assertEqual(client.get("/health/ready").status_code, 503)
