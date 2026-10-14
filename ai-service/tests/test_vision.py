import asyncio
import base64
from io import BytesIO
import json
import os
import unittest
from unittest.mock import patch
from uuid import uuid4

import httpx
from PIL import Image
from fastapi.testclient import TestClient

from app.main import create_app
from app.providers.vision import BailianVision, VisionConfig, VisionError, validate_image, DEFAULT_PROMPT


def image_data(format="PNG"):
    output = BytesIO()
    Image.new("RGB", (2, 2), "red").save(output, format=format)
    return output.getvalue()


class VisionInputTests(unittest.TestCase):
    def test_real_images_and_invalid_bytes(self):
        for format, media in [("PNG", "image/png"), ("JPEG", "image/jpeg")]:
            data = image_data(format)
            self.assertEqual(validate_image(base64.b64encode(data).decode(), media), data)
        for data, media in [("%%%%", "image/png"), (base64.b64encode(b"bad").decode(), "image/png"), (base64.b64encode(image_data()).decode(), "image/jpeg"), (base64.b64encode(image_data("JPEG")[:30]).decode(), "image/jpeg")]:
            with self.assertRaises(VisionError) as raised:
                validate_image(data, media)
            self.assertEqual(raised.exception.code, "invalid_image")

    def test_disabled_and_config_redaction(self):
        with patch.dict(os.environ, {"AI_PROVIDER_ENABLED": "false", "BAILIAN_API_KEY": "private-key"}):
            self.assertIsNone(VisionConfig.from_env())
        self.assertNotIn("private-key", repr(VisionConfig("private-key")))
        for env in [{"AI_PROVIDER_ENABLED": "bad"}, {"AI_PROVIDER_ENABLED": "true", "BAILIAN_API_KEY": ""}, {"AI_PROVIDER_ENABLED": "true", "BAILIAN_API_KEY": "private-key", "BAILIAN_BASE_URL": "https://user:private-key@example.test"}]:
            with patch.dict(os.environ, env):
                with self.assertRaises(ValueError) as raised:
                    VisionConfig.from_env()
                self.assertNotIn("private-key", str(raised.exception))

    def test_http_contract_validation_and_disabled(self):
        token = "local-only-" + "x" * 32
        payload = {"request_id": str(uuid4()), "image_base64": base64.b64encode(image_data()).decode(), "media_type": "image/png"}
        with patch.dict(os.environ, {"AI_PROVIDER_ENABLED": "false"}), TestClient(create_app(token)) as client:
            self.assertEqual(client.post("/internal/v1/describe-image", json=payload).status_code, 401)
            response = client.post("/internal/v1/describe-image", json=payload, headers={"Authorization": "Bearer " + token})
            self.assertEqual(response.status_code, 503)
            self.assertEqual(response.json()["detail"]["code"], "vision_disabled")


class VisionProviderTests(unittest.IsolatedAsyncioTestCase):
    async def test_langchain_wire_contract_and_context(self):
        requests = []
        async def handler(request):
            requests.append(json.loads(request.content))
            self.assertEqual(request.url.path, "/v1/chat/completions")
            self.assertEqual(request.headers["Authorization"], "Bearer mock-private-key")
            return httpx.Response(200, json={"id": "test", "object": "chat.completion", "created": 0, "model": "qwen-vl-plus", "choices": [{"index": 0, "message": {"role": "assistant", "content": "  红色方块🙂  "}, "finish_reason": "stop"}], "usage": {"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13}})
        provider = BailianVision(VisionConfig("mock-private-key", "http://mock.local/v1"), httpx.MockTransport(handler))
        try:
            encoded = base64.b64encode(image_data()).decode()
            self.assertEqual(await provider.describe(encoded, "image/png"), "红色方块🙂")
            await provider.describe(encoded, "image/png", "中" * 1499 + "🙂PRIVATE_TAIL")
            self.assertEqual(requests[0]["model"], "qwen-vl-plus")
            self.assertEqual(requests[0]["temperature"], 0.3)
            self.assertEqual(requests[0]["max_tokens"], 512)
            self.assertEqual(requests[0]["messages"][0]["content"][0]["text"], DEFAULT_PROMPT)
            self.assertEqual(requests[0]["messages"][0]["content"][1]["image_url"]["url"], "data:image/png;base64," + encoded)
            self.assertNotIn("PRIVATE_TAIL", requests[1]["messages"][0]["content"][0]["text"])
        finally:
            await provider.close()

    async def test_provider_errors_are_safe_and_not_retried(self):
        for status, expected, content in [(429, "vision_rate_limited", None), (401, "vision_unavailable", None), (500, "vision_unavailable", None), (200, "invalid_vision_response", ""), (200, "invalid_vision_response", "x\x00")]:
            calls = []
            async def handler(request):
                calls.append(request)
                if status != 200:
                    return httpx.Response(status, json={"error": {"message": "mock-private-key document secret", "type": "bad"}})
                return httpx.Response(200, json={"id": "test", "object": "chat.completion", "created": 0, "model": "qwen-vl-plus", "choices": [{"index": 0, "message": {"role": "assistant", "content": content}, "finish_reason": "stop"}]})
            provider = BailianVision(VisionConfig("mock-private-key", "http://mock.local/v1"), httpx.MockTransport(handler))
            try:
                with self.assertRaises(VisionError) as raised:
                    await provider.describe("local", "image/png")
                self.assertEqual(raised.exception.code, expected)
                self.assertNotIn("private", str(raised.exception))
                self.assertEqual(len(calls), 1)
            finally:
                await provider.close()

    async def test_timeout_and_cancellation_stop_upstream(self):
        cancelled = asyncio.Event()
        entered = asyncio.Event()
        async def handler(request):
            entered.set()
            try:
                await asyncio.Future()
            finally:
                cancelled.set()
        provider = BailianVision(VisionConfig("mock-key", "http://mock.local/v1", timeout=0.1), httpx.MockTransport(handler))
        try:
            with self.assertRaises(VisionError) as raised:
                await provider.describe("local", "image/png")
            self.assertEqual(raised.exception.code, "vision_timeout")
            self.assertTrue(cancelled.is_set())
            cancelled.clear()
            task = asyncio.create_task(provider.describe("local", "image/png"))
            await asyncio.sleep(0.02)
            task.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await task
            self.assertTrue(cancelled.is_set())
        finally:
            await provider.close()
