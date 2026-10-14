"""OpenAI-compatible vision via LangChain; explicitly configured per capability."""
import asyncio
import base64
import binascii
from dataclasses import dataclass, field
from io import BytesIO
import os
import warnings

import httpx
from PIL import Image
from langchain_openai import ChatOpenAI
from openai import APITimeoutError, RateLimitError
from langsmith import tracing_context
from app.providers.config import provider_enabled, validate_provider

MAX_IMAGE_BYTES = 5 * 1024 * 1024
MAX_DESCRIPTION_CHARACTERS = 20_000
DEFAULT_PROMPT = '请用中文详细描述这张图片的内容，包括关键信息、主体、场景和重要细节，约100-200字。只输出描述文字，不要加"这张图片"开头。'


class VisionError(Exception):
    def __init__(self, code: str, status: int):
        super().__init__(code)
        self.code, self.status = code, status


def validate_image(encoded: str, media_type: str) -> bytes:
    try:
        data = base64.b64decode(encoded, validate=True)
        if not data or len(data) > MAX_IMAGE_BYTES:
            raise ValueError()
        with warnings.catch_warnings():
            warnings.simplefilter("error", Image.DecompressionBombWarning)
            with Image.open(BytesIO(data)) as image:
                expected = {"image/png": "PNG", "image/jpeg": "JPEG"}.get(media_type)
                if image.format != expected or image.width * image.height > 25_000_000:
                    raise ValueError()
                image.verify()
            # JPEG verify alone does not decode all data or detect truncation.
            with Image.open(BytesIO(data)) as image:
                image.load()
        return data
    except (ValueError, OSError, SyntaxError, binascii.Error, Image.DecompressionBombError, Image.DecompressionBombWarning):
        raise VisionError("invalid_image", 422) from None


@dataclass(frozen=True)
class VisionConfig:
    api_key: str = field(repr=False)
    base_url: str = "https://dashscope.aliyuncs.com/compatible-mode/v1"
    model: str = "qwen-vl-plus"
    timeout: float = 45.0
    provider: str = "bailian"

    @classmethod
    def from_env(cls):
        if not provider_enabled():
            return None
        provider = os.environ.get("AI_VISION_PROVIDER", "bailian")
        if provider == "disabled":
            return None
        if provider == "deepseek":
            config = cls(os.environ.get("DEEPSEEK_API_KEY", ""), os.environ.get("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), os.environ.get("DEEPSEEK_VISION_MODEL", "deepseek-flash"), provider=provider)
        elif provider == "bailian":
            config = cls(os.environ.get("BAILIAN_API_KEY", ""), os.environ.get("BAILIAN_BASE_URL", cls.base_url), os.environ.get("BAILIAN_VISION_MODEL", cls.model))
        else:
            raise ValueError("AI_VISION_PROVIDER must be deepseek, bailian or disabled")
        validate_provider(config.api_key, config.base_url, config.model, "vision")
        return config


class VisionProvider:
    def __init__(self, config: VisionConfig, transport=None):
        self._http = httpx.Client(trust_env=False, follow_redirects=False, timeout=config.timeout)
        self._async_http = httpx.AsyncClient(trust_env=False, follow_redirects=False, timeout=config.timeout, transport=transport)
        self._timeout = config.timeout
        self._slots = asyncio.Semaphore(2)
        # Both providers use max_tokens. DeepSeek thinking defaults to enabled;
        # descriptions request only the answer to stay within the existing budget.
        extra_body = {"max_tokens": 512}
        if config.provider == "deepseek":
            extra_body["thinking"] = {"type": "disabled"}
        self._model = ChatOpenAI(model=config.model, api_key=config.api_key, base_url=config.base_url, temperature=0.3, extra_body=extra_body, max_retries=0, timeout=config.timeout, http_client=self._http, http_async_client=self._async_http, use_responses_api=False)

    async def close(self):
        self._http.close()
        await self._async_http.aclose()

    async def describe(self, encoded: str, media_type: str, context: str | None = None) -> str:
        prompt = DEFAULT_PROMPT
        if context and context.strip():
            # JavaScript's original slice(0,1500) counts UTF-16 code units.
            snippet = context.encode("utf-16-le")[:3000].decode("utf-16-le", errors="ignore")
            prompt = '请结合以下文档上下文，用中文详细描述这张图片的内容（包括关键信息、主体、场景和重要细节，约100-200字）。只输出描述文字，不要加"这张图片"开头。\n\n文档上下文：\n' + snippet
        try:
            async with asyncio.timeout(self._timeout):
                async with self._slots:
                    with tracing_context(enabled=False):
                        result = await self._model.ainvoke([{"role": "user", "content": [{"type": "text", "text": prompt}, {"type": "image_url", "image_url": {"url": f"data:{media_type};base64,{encoded}"}}]}])
        except (TimeoutError, APITimeoutError):
            raise VisionError("vision_timeout", 504) from None
        except RateLimitError:
            raise VisionError("vision_rate_limited", 429) from None
        except Exception:
            raise VisionError("vision_unavailable", 502) from None
        content = result.content
        if isinstance(content, list):
            content = "".join(item.get("text", "") for item in content if isinstance(item, dict) and isinstance(item.get("text", ""), str))
        if not isinstance(content, str) or not content.strip() or "\x00" in content or len(content) > MAX_DESCRIPTION_CHARACTERS:
            raise VisionError("invalid_vision_response", 502)
        return content.strip()


BailianVision = VisionProvider
