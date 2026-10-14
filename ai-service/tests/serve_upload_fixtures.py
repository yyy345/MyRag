"""Local Go integration harness: fixed document bytes and a parser-only server."""
import base64
import json
import socket
from io import BytesIO

import httpx
from PIL import Image
from app.providers.vision import BailianVision, VisionConfig

import uvicorn
from app.main import create_app
from csv_fixtures import samples as csv_samples
from docx_fixtures import samples as docx_samples
from pdf_fixtures import samples as pdf_samples
from xlsx_fixtures import samples as xlsx_samples

TOKEN = "parse-local-test-abcdefghijklmnopqrstuvwxyz0123456789"


if __name__ == "__main__":
    images = []
    for color in ("red", "blue"):
        buffer = BytesIO()
        Image.new("RGB", (2, 2), color).save(buffer, format="PNG")
        images.append({"filename": color+".png", "content": base64.b64encode(buffer.getvalue()).decode(), "media_type": "image/png"})

    async def mock_model(request):
        payload = json.loads(request.content)
        if payload["messages"][0]["content"][1]["image_url"]["url"].endswith(images[1]["content"]):
            return httpx.Response(429, json={"error": {"message": "private model error", "type": "rate_limit"}})
        return httpx.Response(200, json={"id": "local", "object": "chat.completion", "created": 0, "model": "qwen-vl-plus", "choices": [{"index": 0, "message": {"role": "assistant", "content": "红色方块🙂"}, "finish_reason": "stop"}]})

    vision = BailianVision(VisionConfig("local-test-only", "http://mock.local/v1"), httpx.MockTransport(mock_model))
    documents = [
        ("中文🙂.TXT", "中文🙂\x00\n正文".encode(), "中文🙂\n正文", 200),
        ("中文.md", "# 标题\n\nGBK中文".encode("gbk"), "# 标题\n\nGBK中文", 200),
        ("chinese.PDF", pdf_samples()["chinese.PDF"], "\n\n知识库解析测试\n第一行中文 English 123\n\n第二页内容\n结束", 200),
        ("table.docx", docx_samples()["table.docx"], "表前\n\n列一\n\n列二\n\n单元格第一段\n\n第二段\n\n数据🙂\n\n表后\n\n", 200),
        ("multiline.csv", csv_samples()["multiline.csv"], "名称 | 备注\n--- | ---\n中文 | 第一行 ↵ 第二行", 200),
        ("formula.xlsx", xlsx_samples()["formula.xlsx"], "输入 | 求和 | 文本 | 缺缓存 | 错误 | 布尔 | 日期\n--- | --- | --- | --- | --- | --- | ---\n1 | 3 | 中文 |  |  | true | 60", 200),
        ("bad.xlsx", xlsx_samples()["corrupt.xlsx"], "", 400),
        ("blank.csv", csv_samples()["empty.csv"], "", 400),
        ("large.txt", b"a" * 2_000_001, "", 400),
    ]
    listener = socket.socket()
    listener.bind(("127.0.0.1", 0))
    data = {"url": f"http://127.0.0.1:{listener.getsockname()[1]}", "images": images, "files": [
        {"filename": name, "content": base64.b64encode(content).decode(), "expected": text, "status": status}
        for name, content, text, status in documents
    ]}
    print(json.dumps(data, ensure_ascii=True), flush=True)
    server = uvicorn.Server(uvicorn.Config(create_app(TOKEN, vision=vision), log_level="critical", access_log=False))
    try:
        server.run(sockets=[listener])
    finally:
        listener.close()
