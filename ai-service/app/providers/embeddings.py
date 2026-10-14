"""Bailian embeddings: explicit batches and response identity validation."""
import asyncio
from dataclasses import dataclass, field
import math
import os

import httpx
from langchain_openai import OpenAIEmbeddings
from openai import APIConnectionError, APIStatusError, APITimeoutError, RateLimitError

from app.providers.config import provider_enabled, validate_provider

DIMENSIONS = 1024
BATCH_SIZE = 10


class EmbeddingError(Exception):
    def __init__(self, code: str, retryable: bool = False):
        super().__init__(code)
        self.code, self.retryable = code, retryable


@dataclass(frozen=True)
class EmbeddingConfig:
    api_key: str = field(repr=False)
    base_url: str = "https://dashscope.aliyuncs.com/compatible-mode/v1"
    model: str = "text-embedding-v3"
    timeout: float = 45.0

    @classmethod
    def from_env(cls):
        if not provider_enabled():
            return None
        # Chat/vision provider selection must never send embedding requests or
        # the embedding key to DeepSeek. Keep the existing 1024-dimension index.
        config = cls(os.environ.get("BAILIAN_API_KEY", ""), os.environ.get("BAILIAN_BASE_URL", cls.base_url), os.environ.get("BAILIAN_EMBEDDING_MODEL", cls.model))
        validate_provider(config.api_key, config.base_url, config.model, "embedding")
        return config


def validate_vector(values):
    if not isinstance(values, list) or len(values) != DIMENSIONS or any(isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or abs(value) > 3.4028234663852886e38 for value in values) or not any(values):
        raise EmbeddingError("invalid_embedding_response")
    return [float(value) for value in values]


class BailianEmbeddings:
    def __init__(self, config: EmbeddingConfig, transport=None):
        self._http = httpx.Client(trust_env=False, follow_redirects=False, timeout=config.timeout)
        self._async_http = httpx.AsyncClient(trust_env=False, follow_redirects=False, timeout=config.timeout, transport=transport)
        self._model = OpenAIEmbeddings(model=config.model, api_key=config.api_key, base_url=config.base_url, dimensions=DIMENSIONS, chunk_size=BATCH_SIZE, max_retries=0, timeout=config.timeout, check_embedding_ctx_length=False, http_client=self._http, http_async_client=self._async_http)
        self._name, self._timeout = config.model, config.timeout
        self._slots = asyncio.Semaphore(2)

    async def close(self):
        self._http.close()
        await self._async_http.aclose()

    async def embed(self, texts: list[str]) -> list[list[float]]:
        if not isinstance(texts, list) or len(texts) > 10_000 or any(not isinstance(text, str) or not text.strip() or "\x00" in text or len(text) > 10_000 for text in texts):
            raise EmbeddingError("invalid_embedding_input")
        if not texts:
            return []
        output = []
        for offset in range(0, len(texts), BATCH_SIZE):
            # JS OpenAIEmbeddings strips LF by default; retain existing vectors'
            # input semantics, including CR preservation for CRLF input.
            batch = [text.replace("\n", " ") for text in texts[offset:offset+BATCH_SIZE]]
            try:
                async with asyncio.timeout(self._timeout):
                    async with self._slots:
                        # Framework aembed_documents discards response indices.
                        # Keep them via its configured client to detect duplicates
                        # and reorder out-of-order responses before storing chunks.
                        response = await self._model.async_client.with_raw_response.create(input=batch, model=self._name, dimensions=DIMENSIONS, encoding_format="float")
            except (TimeoutError, APITimeoutError):
                raise EmbeddingError("embedding_timeout", True) from None
            except RateLimitError:
                raise EmbeddingError("embedding_rate_limited", True) from None
            except APIConnectionError:
                raise EmbeddingError("embedding_unavailable", True) from None
            except APIStatusError as error:
                raise EmbeddingError("embedding_unavailable", error.status_code >= 500) from None
            except Exception:
                raise EmbeddingError("embedding_unavailable") from None
            try:
                # SDK typed models coerce boolean/vector/index values. Validate
                # the raw JSON before coercion to enforce numerical identity.
                data = response.http_response.json()["data"]
                if not isinstance(data, list) or len(data) != len(batch):
                    raise ValueError()
                mapped = {}
                for item in data:
                    index = item["index"]
                    if type(index) is not int or index not in range(len(batch)) or index in mapped:
                        raise ValueError()
                    mapped[index] = validate_vector(item["embedding"])
                output.extend(mapped[index] for index in range(len(batch)))
            except (ValueError, TypeError, AttributeError, KeyError, EmbeddingError):
                raise EmbeddingError("invalid_embedding_response") from None
        return output

    async def query(self, text: str) -> list[float]:
        return (await self.embed([text]))[0]
