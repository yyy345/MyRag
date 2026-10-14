"""Local document parsing. No provider credentials, model calls or database writes."""

MAX_DOCUMENT_BYTES = 20 * 1024 * 1024
MAX_DOCUMENT_CHARACTERS = 2_000_000
JS_WHITESPACE = "\u0009\u000a\u000b\u000c\u000d\u0020\u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"


class ParseError(ValueError):
    def __init__(self, code: str, message: str, status: int = 422):
        super().__init__(message)
        self.code = code
        self.status = status


def decode_chinese_text(content: bytes) -> str:
    # Preserve the legacy UTF-8-first/GBK-fallback behavior, including whitespace.
    text = content.decode("utf-8", errors="replace")
    return content.decode("gbk", errors="replace") if "\ufffd" in text else text


def parse_document(content: bytes, filename: str) -> str:
    if len(content) > MAX_DOCUMENT_BYTES:
        raise ParseError("document_too_large", "文档最大 20MB", 413)
    extension = filename.rsplit(".", 1)[-1].lower() if "." in filename else ""
    if extension not in {"txt", "md", "pdf", "docx", "csv", "xlsx"}:
        raise ParseError("unsupported_format", "不支持的文件格式", 415)
    if extension == "pdf":
        from app.ingestion.pdf import parse_pdf
        text = parse_pdf(content)
    elif extension == "docx":
        from app.ingestion.docx import parse_docx
        text = parse_docx(content)
    elif extension == "csv":
        from app.ingestion.csv import parse_csv
        text = parse_csv(content)
    elif extension == "xlsx":
        from app.ingestion.xlsx import parse_xlsx
        text = parse_xlsx(content)
    else:
        text = decode_chinese_text(content)
    text = text.replace("\x00", "")
    if len(text) > MAX_DOCUMENT_CHARACTERS:
        raise ParseError("document_text_limit", "文档解析文本过长，请拆分文件", 413)
    if not text.strip(JS_WHITESPACE):
        raise ParseError("empty_document", "文档解析后无有效文本，可能是扫描版 PDF 或文档内容为空")
    return text
