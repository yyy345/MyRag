import unittest
from unittest.mock import patch
from uuid import uuid4

from fastapi.testclient import TestClient
from app.main import create_app
from app.ingestion.parser import ParseError, parse_document
from csv_fixtures import FIXES, samples


class CSVParserTests(unittest.TestCase):
    def test_encoding_empty_cells_and_markdown(self):
        expected = {
            "basic.csv": "姓名 | 年龄\n--- | ---\n张三 | 18\n李四 | 20",
            "gbk.CSV": "名称 | 说明\n--- | ---\n中文 | 编码",
            "bom.csv": "名称 | 备注\n--- | ---\n测试 | 完成",
            "comma.csv": "name | note\n--- | ---\nA | one,two",
            "short.csv": "a | b | c\n--- | --- | ---\n1 | 2 | \n3 |  | 5",
            "blank_rows.csv": "a | b\n--- | ---\n1 | 2",
            "nul.csv": "a | b\n--- | ---\n1 | xy",
        }
        documents = samples()
        for name, value in expected.items():
            with self.subTest(name=name):
                self.assertEqual(parse_document(documents[name], name), value)

    def test_quotes_multiline_and_table_escaping(self):
        for name, expected in FIXES.items():
            if expected is not None:
                with self.subTest(name=name):
                    self.assertEqual(parse_document(samples()[name], name), expected)

    def test_bad_records_empty_input_and_limits(self):
        for name, code in {"empty.csv": "empty_document", "extra.csv": "invalid_csv_columns", "unclosed.csv": "invalid_csv"}.items():
            with self.subTest(name=name), self.assertRaises(ParseError) as caught:
                parse_document(samples()[name], name)
            self.assertEqual(caught.exception.code, code)
        for limit in ("MAX_CSV_ROWS", "MAX_CSV_COLUMNS", "MAX_CSV_CHARACTERS"):
            with self.subTest(limit=limit), patch("app.ingestion.csv." + limit, 1), self.assertRaises(ParseError) as caught:
                parse_document(samples()["basic.csv"], "basic.csv")
            self.assertEqual(caught.exception.status, 413)

    def test_multipart_contract(self):
        token = "local-csv-test-" + "x" * 32
        with TestClient(create_app(token)) as client:
            for name in ("multiline.csv", "unclosed.csv", "empty.csv"):
                request_id = str(uuid4())
                response = client.post("/internal/v1/parse", headers={"Authorization": "Bearer " + token}, data={"request_id": request_id}, files={"file": (name, samples()[name], "text/csv")})
                self.assertEqual(response.status_code, 200 if name == "multiline.csv" else 422)
                if response.status_code == 200:
                    self.assertEqual(response.json()["text"], FIXES[name])
                    self.assertEqual(response.json()["request_id"], request_id)
