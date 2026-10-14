import json
from tempfile import SpooledTemporaryFile
import unittest
from unittest.mock import patch
from uuid import uuid4

from fastapi.testclient import TestClient
from app.api.body_limit import RequestBodyLimitMiddleware
from app.ingestion.parser import MAX_DOCUMENT_BYTES
from app.main import create_app


TOKEN = "upload-limit-test-" + "x" * 32


class UploadLimitTests(unittest.IsolatedAsyncioTestCase):
    def test_json_request_limit_before_model_validation(self):
        with TestClient(create_app(TOKEN)) as client:
            for path in ("extract","chat/stream","describe-image"):
                response=client.post("/internal/v1/"+path,content=b"{}",headers={"Content-Length":str(8*1024*1024+1),"Authorization":"Bearer "+TOKEN})
                self.assertEqual(response.status_code,413)

    async def request(self, headers, chunks):
        app = create_app(TOKEN)
        # A small test limit allows exercising streamed multipart uploads.
        app.add_middleware(RequestBodyLimitMiddleware, max_bytes=1024)
        scope = {
            "type": "http", "asgi": {"version": "3.0"}, "http_version": "1.1",
            "method": "POST", "scheme": "http", "path": "/internal/v1/parse",
            "raw_path": b"/internal/v1/parse", "query_string": b"",
            "server": ("test", 80), "client": ("test", 123), "root_path": "",
            "headers": [(b"authorization", f"Bearer {TOKEN}".encode()), *headers],
        }
        consumed = 0
        messages = []

        async def receive():
            nonlocal consumed
            body = chunks[consumed]
            consumed += 1
            return {"type": "http.request", "body": body, "more_body": consumed < len(chunks)}

        async def send(message):
            messages.append(message)

        await app(scope, receive, send)
        status = next(message["status"] for message in messages if message["type"] == "http.response.start")
        body = b"".join(message.get("body", b"") for message in messages if message["type"] == "http.response.body")
        return status, json.loads(body), consumed

    async def test_declared_oversize_rejected_before_read_or_spool(self):
        with patch("starlette.formparsers.SpooledTemporaryFile") as spool:
            status, body, consumed = await self.request([(b"content-length", b"1025")], [b"unused"])
            self.assertEqual(status, 413)
            self.assertEqual(body["detail"]["code"], "request_too_large")
            self.assertEqual(consumed, 0)
            spool.assert_not_called()

    async def test_streamed_and_understated_lengths_stop_and_close_files(self):
        prefix = (b'--boundary\r\nContent-Disposition: form-data; name="file"; filename="test.txt"\r\n'
                  b'Content-Type: text/plain\r\n\r\n')
        for lengths in ([], [(b"content-length", b"100")]):
            files = []

            def spool(*args, **kwargs):
                file = SpooledTemporaryFile(*args, **kwargs)
                files.append(file)
                return file

            with patch("starlette.formparsers.SpooledTemporaryFile", side_effect=spool):
                status, body, consumed = await self.request(
                    [(b"content-type", b"multipart/form-data; boundary=boundary"), *lengths],
                    [prefix + b"a" * 300, b"b" * 800, b"unread tail"],
                )
            self.assertEqual(status, 413)
            self.assertEqual(body["detail"]["code"], "request_too_large")
            self.assertEqual(consumed, 2)
            self.assertTrue(files)
            self.assertTrue(all(file.closed for file in files))

    async def test_invalid_lengths_do_not_read_body(self):
        for lengths in ([b"-1"], [b"invalid"], [b"1", b"2"]):
            status, _, consumed = await self.request([(b"content-length", value) for value in lengths], [b"unused"])
            self.assertEqual(status, 400)
            self.assertEqual(consumed, 0)

    async def test_small_streamed_multipart_still_parses(self):
        request_id = str(uuid4())
        body = (f'--boundary\r\nContent-Disposition: form-data; name="request_id"\r\n\r\n{request_id}\r\n'
                '--boundary\r\nContent-Disposition: form-data; name="file"; filename="test.txt"\r\n\r\n'
                '中文🙂\r\n--boundary--\r\n').encode()
        status, response, _ = await self.request([(b"content-type", b"multipart/form-data; boundary=boundary")], [body[:100], body[100:]])
        self.assertEqual(status, 200)
        self.assertEqual(response["text"], "中文🙂")
        self.assertEqual(response["request_id"], request_id)

    def test_file_limit_and_multipart_allowance(self):
        with TestClient(create_app(TOKEN)) as client:
            headers = {"Authorization": f"Bearer {TOKEN}"}
            for size, status in ((MAX_DOCUMENT_BYTES, 200), (MAX_DOCUMENT_BYTES + 1, 413)):
                # NUL padding keeps the cleaned text short, independently
                # exercising the file-byte boundary and multipart allowance.
                response = client.post("/internal/v1/parse", headers=headers, data={"request_id": str(uuid4())}, files={"file": ("test.txt", b"a" + b"\x00" * (size-1))})
                self.assertEqual(response.status_code, status)
                if status == 413:
                    self.assertEqual(response.json()["detail"]["code"], "document_too_large")
