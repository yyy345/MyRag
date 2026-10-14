import unittest
from unittest.mock import patch
from uuid import uuid4

from fastapi.testclient import TestClient
from app.ingestion.parser import ParseError, parse_document
from app.main import create_app
from docx_fixtures import archive, samples


class DOCXParserTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.documents = samples()

    def test_paragraphs_tables_and_runs_preserve_legacy_order(self):
        expected = {
            "paragraphs.DOCX": "标题 中文🙂\n\n前后  空格\n\n\n\n结束\n\n",
            "table.docx": "表前\n\n列一\n\n列二\n\n单元格第一段\n\n第二段\n\n数据🙂\n\n表后\n\n",
            "runs.docx": "第一段\t第二段换行后\n\n",
            "nested.docx": "外层\n\n内层\n\n外层尾\n\n",
        }
        for name, text in expected.items():
            with self.subTest(name=name):
                self.assertEqual(parse_document(self.documents[name], name), text)

    def test_empty_and_damaged_documents_fail_safely(self):
        for name in ["blank.docx", "corrupt.docx", "fake.docx", "missing.docx", "malformed.docx"]:
            with self.subTest(name=name), self.assertRaises(ParseError) as caught:
                parse_document(self.documents[name], name)
            self.assertEqual(caught.exception.code, "empty_document" if name == "blank.docx" else "invalid_docx")
            self.assertEqual(caught.exception.status, 422)

    def test_package_limits_and_dtd_rejection(self):
        for limit in ["MAX_DOCX_PARTS", "MAX_DOCX_EXPANDED_BYTES", "MAX_DOCX_XML_BYTES", "MAX_DOCX_CHARACTERS"]:
            with self.subTest(limit=limit), patch("app.ingestion.docx." + limit, 1), self.assertRaises(ParseError) as caught:
                parse_document(self.documents["table.docx"], "table.docx")
            self.assertEqual(caught.exception.status, 413)
        document = archive({"word/document.xml": '<!DOCTYPE a [<!ENTITY secret SYSTEM "file:///not-to-be-read">]><a>&secret;</a>'})
        with self.assertRaises(ParseError) as caught:
            parse_document(document, "entity.docx")
        self.assertEqual(caught.exception.code, "invalid_docx")

    def test_authenticated_multipart(self):
        token = "local-docx-test-" + "x" * 32
        with TestClient(create_app(token)) as client:
            for name in ["table.docx", "blank.docx", "corrupt.docx"]:
                with self.subTest(name=name):
                    request_id = str(uuid4())
                    response = client.post("/internal/v1/parse", headers={"Authorization": "Bearer " + token}, data={"request_id": request_id}, files={"file": (name, self.documents[name], "application/vnd.openxmlformats-officedocument.wordprocessingml.document")})
                    self.assertEqual(response.status_code, 200 if name == "table.docx" else 422)
                    if response.status_code == 200:
                        body = response.json()
                        self.assertEqual(body["request_id"], request_id)
                        self.assertEqual(body["metadata"]["characters"], len(body["text"]))
                        self.assertIn("单元格第一段", body["text"])
