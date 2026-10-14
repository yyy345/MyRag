"""Explicit provider configuration; never loads dotenv or shares provider keys."""
from dataclasses import dataclass, field
import os
from urllib.parse import urlsplit


def provider_enabled():
    value = os.environ.get("AI_PROVIDER_ENABLED", "false")
    if value not in ("true", "false"):
        raise ValueError("AI_PROVIDER_ENABLED must be true or false")
    return value == "true"


def validate_provider(api_key, base_url, model, capability):
    try:
        parsed = urlsplit(base_url)
        # Access port to reject malformed authorities before constructing a client.
        parsed.port
        valid = (
            bool(api_key) and all(33 <= ord(c) <= 126 for c in api_key)
            and all(ord(c) > 32 for c in base_url)
            and parsed.scheme in ("http", "https") and bool(parsed.hostname)
            and not any((parsed.username, parsed.password, parsed.query, parsed.fragment))
            and bool(model.strip()) and all(ord(c) >= 32 for c in model)
        )
    except ValueError:
        valid = False
    if not valid:
        raise ValueError(f"invalid {capability} provider configuration")


@dataclass(frozen=True)
class ChatConfig:
    api_key: str = field(repr=False)
    base_url: str = "https://dashscope.aliyuncs.com/compatible-mode/v1"
    model: str = "qwen-plus"
    timeout: float = 120.0
    provider: str = "bailian"
    thinking_enabled: bool = False

    @classmethod
    def from_env(cls):
        if not provider_enabled():
            return None
        # Existing explicit Bailian installations retain their configuration.
        # New deployment examples/Compose select DeepSeek explicitly.
        provider = os.environ.get("AI_CHAT_PROVIDER", "bailian")
        if provider == "deepseek":
            thinking = os.environ.get("DEEPSEEK_THINKING_ENABLED", "false")
            if thinking not in ("true", "false"):
                raise ValueError("DEEPSEEK_THINKING_ENABLED must be true or false")
            config = cls(
                os.environ.get("DEEPSEEK_API_KEY", ""),
                os.environ.get("DEEPSEEK_BASE_URL", "https://api.deepseek.com"),
                os.environ.get("DEEPSEEK_LLM_MODEL", "deepseek-v4-flash"),
                provider=provider, thinking_enabled=thinking == "true",
            )
        elif provider == "bailian":
            config = cls(
                os.environ.get("BAILIAN_API_KEY", ""),
                os.environ.get("BAILIAN_BASE_URL", cls.base_url),
                os.environ.get("BAILIAN_LLM_MODEL", cls.model),
            )
        else:
            raise ValueError("AI_CHAT_PROVIDER must be deepseek or bailian")
        validate_provider(config.api_key, config.base_url, config.model, "chat")
        return config
