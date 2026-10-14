import unittest
from unittest.mock import patch
from uuid import uuid4

from fastapi.testclient import TestClient
from app.ingestion.parser import MAX_DOCUMENT_BYTES, ParseError, parse_document
from app.main import create_app


class TextParserTests(unittest.TestCase):
    def test_text_encodings_and_nul_cleanup_preserve_content(self):
        for filename, content, expected in [
            ("正文.TXT", "中文🙂\x00\r\n next\t".encode(), "中文🙂\r\n next\t"),
            ("资料.md", "# 标题\n\n正文".encode("gbk"), "# 标题\n\n正文"),
            ("bom.txt", b"\xef\xbb\xbfhello", "\ufeffhello"),
        ]:
            with self.subTest(filename=filename):
                self.assertEqual(parse_document(content, filename), expected)

    def test_empty_unsupported_and_oversized_documents_are_rejected(self):
        for content in (b"", b"\x00 \n\t", "\ufeff \u3000".encode()):
            with self.assertRaises(ParseError) as error:
                parse_document(content, "blank.txt")
            self.assertEqual(error.exception.code, "empty_document")
        for filename, content, status in [("bad.exe", b"a", 415), ("large.txt", b"a" * (MAX_DOCUMENT_BYTES + 1), 413)]:
            with self.assertRaises(ParseError) as error:
                parse_document(content, filename)
            self.assertEqual(error.exception.status, status)

    def test_authenticated_multipart_returns_text_and_structured_errors(self):
        token = "local-parser-test-" + "x" * 32
        with TestClient(create_app(token)) as client:
            headers = {"Authorization": f"Bearer {token}"}
            request_id = str(uuid4())
            response = client.post("/internal/v1/parse", headers=headers, data={"request_id": request_id}, files={"file": ("资料.md", "# 中文🙂\x00", "text/markdown")})
            self.assertEqual(response.status_code, 200)
            self.assertEqual(response.json(), {"request_id": request_id, "text": "# 中文🙂", "metadata": {"filename": "资料.md", "characters": 5}})
            for content, expected in [(b" ", 422), (b"a" * (MAX_DOCUMENT_BYTES + 1), 413)]:
                response = client.post("/internal/v1/parse", headers=headers, data={"request_id": request_id}, files={"file": ("file.txt", content)})
                self.assertEqual(response.status_code, expected)
                self.assertIn("code", response.json()["detail"])

    def test_text_and_markdown_share_the_parsed_character_limit(self):
        for filename in ("large.txt", "large.md"):
            with patch("app.ingestion.parser.MAX_DOCUMENT_CHARACTERS", 3), self.assertRaises(ParseError) as error:
                parse_document("中文🙂a".encode(), filename)
            self.assertEqual(error.exception.code, "document_text_limit")
            self.assertEqual(error.exception.status, 413)
