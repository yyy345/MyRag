import unittest
from unittest.mock import patch
from uuid import uuid4

from fastapi.testclient import TestClient
from app.ingestion.parser import ParseError, parse_document
from app.main import create_app
from pdf_fixtures import samples


class PDFParserTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.documents = samples()

    def test_chinese_and_multiple_pages_preserve_order(self):
        text = parse_document(self.documents["chinese.PDF"], "chinese.PDF")
        self.assertEqual(text, "\n\n知识库解析测试\n第一行中文 English 123\n\n第二页内容\n结束")
        text = parse_document(self.documents["english.pdf"], "english.pdf")
        self.assertEqual(text, "\n\nPage one\nLine two\n\nPage two")

    def test_no_text_encryption_and_damage_have_safe_errors(self):
        for name, expected in {"blank.pdf": "pdf_no_text", "scanned.pdf": "pdf_no_text", "encrypted.pdf": "encrypted_pdf", "corrupt.pdf": "invalid_pdf", "fake.pdf": "invalid_pdf", "empty.pdf": "invalid_pdf"}.items():
            with self.subTest(name=name):
                with self.assertRaises(ParseError) as caught:
                    parse_document(self.documents[name], name)
                self.assertEqual(caught.exception.code, expected)
                self.assertEqual(caught.exception.status, 422)
                self.assertNotIn("fixture-password", str(caught.exception))

    def test_page_and_text_limits(self):
        for limit in ["MAX_PDF_PAGES", "MAX_PDF_CHARACTERS"]:
            with self.subTest(limit=limit), patch("app.ingestion.pdf." + limit, 1):
                with self.assertRaises(ParseError) as caught:
                    parse_document(self.documents["english.pdf"], "english.pdf")
                self.assertEqual(caught.exception.status, 413)

    def test_real_multipart_boundary(self):
        token = "local-pdf-tests-" + "x" * 32
        with TestClient(create_app(token)) as client:
            for name in ("chinese.PDF", "scanned.pdf", "encrypted.pdf", "corrupt.pdf"):
                with self.subTest(name=name):
                    request_id = str(uuid4())
                    response = client.post("/internal/v1/parse", headers={"Authorization": "Bearer " + token}, data={"request_id": request_id}, files={"file": (name, self.documents[name], "application/pdf")})
                    self.assertEqual(response.status_code, 200 if name == "chinese.PDF" else 422)
                    if response.status_code == 200:
                        body = response.json()
                        self.assertEqual(body["request_id"], request_id)
                        self.assertEqual(body["metadata"]["characters"], len(body["text"]))
                    else:
                        self.assertIn("code", response.json()["detail"])
