"""DOCX raw text extraction with the same Mammoth semantics as the legacy API."""
from io import BytesIO
from xml.parsers.expat import ExpatError, ParserCreate
from zipfile import BadZipFile, LargeZipFile, ZipFile
import zlib

import mammoth

from app.ingestion.parser import ParseError

MAX_DOCX_PARTS = 2000
MAX_DOCX_EXPANDED_BYTES = 64 * 1024 * 1024
MAX_DOCX_XML_BYTES = 16 * 1024 * 1024
MAX_DOCX_CHARACTERS = 2_000_000


def reject_doctype(*_args):
    raise ParseError("invalid_docx", "DOCX 不支持包含 DTD 的 XML")


def parse_docx(content: bytes) -> str:
    try:
        with ZipFile(BytesIO(content)) as package:
            parts = package.infolist()
            names = [part.filename for part in parts]
            if len(names) != len(set(names)):
                raise ParseError("invalid_docx", "DOCX 包含重复文件项")
            if len(parts) > MAX_DOCX_PARTS or sum(p.file_size for p in parts) > MAX_DOCX_EXPANDED_BYTES:
                raise ParseError("docx_size_limit", "DOCX 解压内容过大或文件项过多", 413)
            for part in parts:
                if part.flag_bits & 1:
                    raise ParseError("encrypted_docx", "请解密 DOCX 后上传")
                if part.filename.lower().endswith((".xml", ".rels")):
                    if part.file_size > MAX_DOCX_XML_BYTES:
                        raise ParseError("docx_size_limit", "DOCX XML 内容过大", 413)
                    parser = ParserCreate()
                    parser.StartDoctypeDeclHandler = reject_doctype
                    parser.Parse(package.read(part), True)
        # Mammoth's raw-text reader defaults external_file_access to False.
        # Nothing is extracted to disk and no embedded image conversion runs.
        text = mammoth.extract_raw_text(BytesIO(content)).value
        if len(text) > MAX_DOCX_CHARACTERS:
            raise ParseError("docx_text_limit", "DOCX 解析文本过长", 413)
        return text
    except ParseError:
        raise
    except (BadZipFile, LargeZipFile, ExpatError, zlib.error, OSError, ValueError, KeyError, TypeError, AttributeError, IndexError, RecursionError, NotImplementedError):
        raise ParseError("invalid_docx", "DOCX 文件无效或已损坏，请确认文件未加密且不是旧版 DOC") from None
