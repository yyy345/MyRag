"""Bound upload ingestion before multipart parsing can spool an unlimited file."""

from starlette.exceptions import HTTPException
from starlette.responses import JSONResponse
from starlette.types import ASGIApp, Message, Receive, Scope, Send


class RequestBodyLimitMiddleware:
    def __init__(self, app: ASGIApp, max_bytes: int):
        self.app = app
        self.max_bytes = max_bytes

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if scope["type"] != "http" or scope["method"] != "POST" or not scope["path"].startswith("/internal/v1/"):
            await self.app(scope, receive, send)
            return

        limit = self.max_bytes if scope["path"].rstrip("/")=="/internal/v1/parse" else min(self.max_bytes,8*1024*1024)

        detail = {"code": "request_too_large", "message": "上传请求超过大小限制"}
        lengths = [value for name, value in scope["headers"] if name.lower() == b"content-length"]
        if lengths:
            try:
                if len(lengths) != 1 or not lengths[0].isdigit():
                    raise ValueError
                declared = int(lengths[0])
            except ValueError:
                await JSONResponse({"detail": {"code": "invalid_content_length", "message": "无效的请求长度"}}, status_code=400)(scope, receive, send)
                return
            if declared > limit:
                # Do not read any body or create multipart temporary files.
                await JSONResponse({"detail": detail}, status_code=413)(scope, receive, send)
                return

        received = 0

        async def limited_receive() -> Message:
            nonlocal received
            message = await receive()
            if message["type"] == "http.request":
                received += len(message.get("body", b""))
                if received > limit:
                    # Starlette closes multipart temporary files when its input
                    # stream raises; FastAPI converts this exception to a 413.
                    raise HTTPException(status_code=413, detail=detail)
            return message

        # Count actual bytes as well: absent or false Content-Length cannot
        # bypass the limit. Avoid buffering a second copy of the request.
        await self.app(scope, limited_receive, send)
