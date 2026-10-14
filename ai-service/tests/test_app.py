import unittest
from uuid import uuid4
from fastapi.testclient import TestClient
from app.main import create_app


class BoundaryTests(unittest.TestCase):
    def test_configuration_rejects_missing_credentials(self):
        for token in ("", "short", " " * 40, "x" * 32 + "\n"):
            with self.assertRaises(ValueError):
                create_app(token)

    def test_health_and_internal_authentication(self):
        token = "test-only-" + "x" * 32
        with TestClient(create_app(token)) as client:
            self.assertEqual(client.get("/health/live").status_code, 200)
            self.assertEqual(client.get("/health/ready").status_code, 503)
            request_id = str(uuid4())
            payloads = {
                "parse": {"data": {"request_id": request_id}, "files": {"file": ("test.txt", b"fixture", "text/plain")}},
                "describe-image": {"json": {"request_id": request_id, "image_base64": "eA==", "media_type": "image/png"}},
                "chat/stream": {"json": {"request_id": request_id, "user_id": str(uuid4()), "kb_id": str(uuid4()), "query": "fixture"}},
                "extract": {"json": {"request_id": request_id, "history": [{"role": "user", "content": "fixture"}]}},
            }
            for path, kwargs in payloads.items():
                url = f"/internal/v1/{path}"
                for headers in ({}, {"Authorization": "Bearer wrong"}):
                    self.assertEqual(client.post(url, headers=headers, **kwargs).status_code, 401)
                response = client.post(url, headers={"Authorization": f"Bearer {token}"}, **kwargs)
                self.assertEqual(response.status_code, {"parse": 200, "describe-image": 422, "chat/stream":503,"extract":503}[path])
                self.assertNotIn(token, response.text)

    def test_validation_rejects_bad_scope_without_echoing_document_or_credentials(self):
        token = "test-only-" + "x" * 32
        with TestClient(create_app(token)) as client:
            headers = {"Authorization": f"Bearer {token}"}
            for payload in ({}, {"request_id": "invalid", "query": "private document", "extra": token}):
                response = client.post("/internal/v1/chat/stream", headers=headers, json=payload)
                self.assertEqual(response.status_code, 422)
                self.assertNotIn("private document", response.text)
                self.assertNotIn(token, response.text)
            self.assertEqual(client.get("/docs").status_code, 404)
            self.assertEqual(client.get("/openapi.json").status_code, 404)


if __name__ == "__main__":
    unittest.main()
