"""Legacy 2048/200 recursive character chunks, counted in UTF-16 units."""
from langchain_text_splitters import RecursiveCharacterTextSplitter

CHUNK_SIZE = 2048
CHUNK_OVERLAP = 200
SEPARATORS = ["\n\n", "\n", "。", "！", "？", ".", "!", "?", "；", ";", " ", ""]
JS_WHITESPACE = "\u0009\u000a\u000b\u000c\u000d\u0020\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"


def utf16_length(text: str) -> int:
    return len(text.encode("utf-16-le")) // 2


class LegacySplitter(RecursiveCharacterTextSplitter):
    def _join_docs(self, docs: list[str], separator: str) -> str | None:
        text = separator.join(docs).strip(JS_WHITESPACE)
        return text or None


def chunk_text(text: str) -> list[dict]:
    if not isinstance(text, str) or "\x00" in text or len(text) > 2_000_000:
        raise ValueError("invalid indexing text")
    # Reject lone surrogates before storage/provider encoding. Python splitting
    # preserves whole code points, correcting JS's split('') surrogate damage.
    utf16_length(text)
    splitter = LegacySplitter(chunk_size=CHUNK_SIZE, chunk_overlap=CHUNK_OVERLAP, separators=SEPARATORS, length_function=utf16_length, keep_separator=True)
    return [{"index": index, "content": content} for index, content in enumerate(splitter.split_text(text))]
