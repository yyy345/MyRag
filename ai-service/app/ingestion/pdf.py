"""Extract embedded PDF text locally; never run OCR or follow external resources."""
from io import BytesIO

from pypdf import PdfReader
from pypdf.errors import PyPdfError

from app.ingestion.parser import JS_WHITESPACE, ParseError

MAX_PDF_PAGES = 500
MAX_PDF_CHARACTERS = 2_000_000


def parse_pdf(content: bytes) -> str:
    if not content.startswith(b"%PDF-"):
        raise ParseError("invalid_pdf", "PDF 文件格式无效或已损坏")
    try:
        reader = PdfReader(BytesIO(content), strict=True)
        if reader.is_encrypted:
            raise ParseError("encrypted_pdf", "暂不支持加密 PDF，请先解密后上传")
        count = len(reader.pages)
        if not count:
            raise ParseError("invalid_pdf", "PDF 没有可读取的页面")
        if count > MAX_PDF_PAGES:
            raise ParseError("pdf_page_limit", "PDF 最多支持 500 页", 413)
        pages = []
        characters = 0
        for page in reader.pages:
            # pdf-parse separates pages with two newlines. Extractors may differ
            # in intra-page whitespace; do not collapse meaningful text here.
            text = (page.extract_text() or "").replace("\x00", "").rstrip("\n")
            characters += len(text) + 2
            if characters > MAX_PDF_CHARACTERS:
                raise ParseError("pdf_text_limit", "PDF 解析文本过长", 413)
            pages.append("\n\n" + text)
        text = "".join(pages)
        if not text.strip(JS_WHITESPACE):
            raise ParseError("pdf_no_text", f"PDF 解析后无文本（共 {count} 页），可能是扫描件；当前未启用 OCR")
        return text
    except ParseError:
        raise
    except (PyPdfError, ValueError, TypeError, KeyError, IndexError, RecursionError, NotImplementedError):
        raise ParseError("invalid_pdf", "PDF 文件格式无效或已损坏") from None
