import asyncio
import json
import unittest

import httpx
from app.providers.embeddings import BailianEmbeddings, EmbeddingConfig, EmbeddingError, validate_vector


class EmbeddingTests(unittest.IsolatedAsyncioTestCase):
    async def test_batches_wire_contract_and_response_order(self):
        requests = []
        async def handler(request):
            body = json.loads(request.content)
            self.assertEqual(request.url.path, "/v1/embeddings")
            self.assertEqual(request.headers["Authorization"], "Bearer mock-private-key")
            self.assertEqual(body["model"], "text-embedding-v3")
            self.assertEqual(body["dimensions"], 1024)
            self.assertEqual(body["encoding_format"], "float")
            requests.append(body["input"])
            items = [{"index": i, "embedding": [float(int(text.split()[0])+1)] * 1024, "object": "embedding"} for i, text in enumerate(body["input"])]
            return httpx.Response(200, json={"object": "list", "model": "text-embedding-v3", "data": items[::-1], "usage": {"prompt_tokens": 1, "total_tokens": 1}})
        provider = BailianEmbeddings(EmbeddingConfig("mock-private-key", "http://mock.local/v1"), httpx.MockTransport(handler))
        try:
            texts = [f"{i}\n中文🙂\r\n" for i in range(23)]
            vectors = await provider.embed(texts)
            self.assertEqual([len(batch) for batch in requests], [10, 10, 3])
            self.assertEqual(requests[0][0], "0 中文🙂\r ")
            self.assertEqual([vector[0] for vector in vectors], list(range(1, 24)))
            self.assertEqual(await provider.query("5 query"), [6.0] * 1024)
            before = len(requests)
            self.assertEqual(await provider.embed([]), [])
            self.assertEqual(len(requests), before)
        finally:
            await provider.close()

    async def test_bad_dimensions_indices_count_and_numbers(self):
        samples = [[], [{"index": 1, "embedding": [1.0]*1024}], [{"index": 0, "embedding": [1.0]*3}], [{"index": 0, "embedding": [0.0]*1024}], [{"index": 0, "embedding": [True]*1024}], [{"index": 0, "embedding": [1e100]*1024}]]
        for data in samples:
            async def handler(_request):
                return httpx.Response(200, json={"object": "list", "model": "local", "data": data, "usage": {"prompt_tokens": 1, "total_tokens": 1}})
            provider = BailianEmbeddings(EmbeddingConfig("mock-key", "http://mock.local/v1"), httpx.MockTransport(handler))
            try:
                with self.assertRaises(EmbeddingError) as raised:
                    await provider.embed(["local"])
                self.assertEqual(raised.exception.code, "invalid_embedding_response")
            finally:
                await provider.close()
        for values in ([float("nan")]*1024, [float("inf")]*1024):
            with self.assertRaises(EmbeddingError):
                validate_vector(values)

    async def test_safe_errors_no_automatic_retries(self):
        for status, code, retryable in [(429, "embedding_rate_limited", True), (503, "embedding_unavailable", True), (401, "embedding_unavailable", False)]:
            calls = []
            async def handler(request):
                calls.append(request)
                return httpx.Response(status, json={"error": {"message": "mock-private-key", "type": "provider"}})
            provider = BailianEmbeddings(EmbeddingConfig("mock-key", "http://mock.local/v1"), httpx.MockTransport(handler))
            try:
                with self.assertRaises(EmbeddingError) as raised:
                    await provider.embed(["local"])
                self.assertEqual((raised.exception.code, raised.exception.retryable), (code, retryable))
                self.assertEqual(len(calls), 1)
                self.assertNotIn("private", str(raised.exception))
            finally:
                await provider.close()

    async def test_timeout_cancel_and_invalid_input(self):
        cancelled = asyncio.Event()
        entered = asyncio.Event()
        async def handler(request):
            entered.set()
            try:
                await asyncio.Future()
            finally:
                cancelled.set()
        # Leave room for SDK initialization under load; the timeout must expire
        # while transport is in flight to verify upstream cancellation.
        provider = BailianEmbeddings(EmbeddingConfig("mock-key", "http://mock.local/v1", timeout=2), httpx.MockTransport(handler))
        try:
            for invalid in ([""], ["a\x00"], ["a" * 10_001]):
                with self.assertRaises(EmbeddingError) as raised:
                    await provider.embed(invalid)
                self.assertEqual(raised.exception.code, "invalid_embedding_input")
            with self.assertRaises(EmbeddingError) as raised:
                await provider.embed(["local"])
            self.assertEqual(raised.exception.code, "embedding_timeout")
            self.assertTrue(cancelled.is_set())
            cancelled.clear()
            entered.clear()
            task = asyncio.create_task(provider.embed(["local"]))
            await asyncio.wait_for(entered.wait(), timeout=3)
            task.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await task
            self.assertTrue(cancelled.is_set())
        finally:
            await provider.close()
