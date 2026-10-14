import socket
import threading
import time
import unittest
from uuid import uuid4

import httpx
import uvicorn

from app.main import create_app
from pdf_fixtures import samples
from docx_fixtures import samples as docx_samples
from csv_fixtures import samples as csv_samples, FIXES as CSV_FIXES
from xlsx_fixtures import samples as xlsx_samples


class HTTPTests(unittest.TestCase):
    def test_real_http_start_authentication_and_graceful_shutdown(self):
        listener = socket.socket()
        listener.bind(("127.0.0.1", 0))
        address = listener.getsockname()
        token = "test-only-" + "x" * 32
        server = uvicorn.Server(uvicorn.Config(create_app(token), log_level="critical", access_log=False))
        thread = threading.Thread(target=lambda: server.run(sockets=[listener]), daemon=True)
        thread.start()
        try:
            deadline = time.monotonic() + 5
            while not server.started and thread.is_alive() and time.monotonic() < deadline:
                time.sleep(0.01)
            self.assertTrue(server.started, "Uvicorn did not start")
            with httpx.Client(base_url=f"http://{address[0]}:{address[1]}", trust_env=False) as client:
                self.assertEqual(client.get("/health/live").json()["status"], "alive")
                self.assertEqual(client.post("/internal/v1/extract", json={}).status_code, 401)
                self.assertEqual(client.post("/internal/v1/extract", headers={"Authorization": f"Bearer {token}"}, json={}).status_code, 422)
                documents = samples()
                for name, expected in [("chinese.PDF", 200), ("scanned.pdf", 422)]:
                    response = client.post("/internal/v1/parse", headers={"Authorization": f"Bearer {token}"}, data={"request_id": str(uuid4())}, files={"file": (name, documents[name], "application/pdf")})
                    self.assertEqual(response.status_code, expected)
                    if expected == 200:
                        self.assertIn("第二页内容", response.json()["text"])
                    else:
                        self.assertEqual(response.json()["detail"]["code"], "pdf_no_text")
                for name, data in docx_samples().items():
                    if name not in ("table.docx", "corrupt.docx"):
                        continue
                    response = client.post("/internal/v1/parse", headers={"Authorization": f"Bearer {token}"}, data={"request_id": str(uuid4())}, files={"file": (name, data)})
                    self.assertEqual(response.status_code, 200 if name == "table.docx" else 422)
                    if response.status_code == 200:
                        self.assertIn("列一\n\n列二", response.json()["text"])
                    else:
                        self.assertEqual(response.json()["detail"]["code"], "invalid_docx")
                for name in ("multiline.csv", "unclosed.csv"):
                    response = client.post("/internal/v1/parse", headers={"Authorization": f"Bearer {token}"}, data={"request_id": str(uuid4())}, files={"file": (name, csv_samples()[name], "text/csv")})
                    self.assertEqual(response.status_code, 200 if name == "multiline.csv" else 422)
                    if response.status_code == 200:
                        self.assertEqual(response.json()["text"], CSV_FIXES[name])
                    else:
                        self.assertEqual(response.json()["detail"]["code"], "invalid_csv")
                documents = xlsx_samples()
                for name in ("multiple.xlsx", "dates.xlsx", "formula.xlsx", "corrupt.xlsx"):
                    response = client.post("/internal/v1/parse", headers={"Authorization": f"Bearer {token}"}, data={"request_id": str(uuid4())}, files={"file": (name, documents[name], "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")})
                    self.assertEqual(response.status_code, 422 if name == "corrupt.xlsx" else 200)
                    if name == "multiple.xlsx":
                        self.assertEqual(response.json()["text"], "第一表 | 值\n--- | ---\n中文 | 1")
                        self.assertNotIn("SECOND_SHEET_PRIVATE", response.text)
                    elif name == "dates.xlsx":
                        self.assertIn("45292.5 | 0.5208333333333334 | 60", response.json()["text"])
                    elif name == "formula.xlsx":
                        self.assertIn("1 | 3 | 中文 |  |  | true | 60", response.json()["text"])
                    else:
                        self.assertEqual(response.json()["detail"]["code"], "invalid_xlsx")
        finally:
            server.should_exit = True
            thread.join(timeout=5)
            listener.close()
        self.assertFalse(thread.is_alive(), "Uvicorn did not stop")
        with socket.socket() as probe:
            probe.settimeout(0.2)
            self.assertNotEqual(probe.connect_ex(address), 0)
