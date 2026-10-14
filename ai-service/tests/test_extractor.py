import json
import unittest
from uuid import uuid4
import httpx
from fastapi.testclient import TestClient
from app.api.schemas import ExtractRequest
from app.main import create_app
from app.providers.chat import BailianChat,ChatError
from app.providers.vision import VisionConfig
from app.rag.extractor import Extractor,parse_sections

class ExtractionTests(unittest.IsolatedAsyncioTestCase):
    async def test_framework_model_prompt_parameters_and_parser(self):
        calls=[]
        def model(request):
            body=json.loads(request.content);calls.append(body)
            self.assertEqual(body["max_tokens"],2048)
            self.assertTrue(body["messages"][1]["content"].startswith("对话记录：\n[AI] 最新回答"))
            return httpx.Response(200,json={"id":"local","object":"chat.completion","created":0,"model":"qwen-plus","choices":[{"index":0,"message":{"role":"assistant","content":"## 标题\n"+"知识"*120},"finish_reason":"stop"}]})
        provider=BailianChat(VisionConfig("local-only","http://mock.local/v1"),httpx.MockTransport(model))
        extractor=Extractor(provider)
        payload=ExtractRequest(request_id=uuid4(),history=[{"role":"user","content":"第一问题"},{"role":"user","content":"第二问题"},{"role":"assistant","content":"最新回答"}])
        try:
            self.assertEqual(await extractor.extract(payload),[{"title":"标题","content":"知识"*120}])
            self.assertEqual(calls[-1]["temperature"],0.3)
            payload.mode="scheduled"
            await extractor.extract(payload)
            self.assertEqual(calls[-1]["temperature"],0.5)
            payload.history=payload.history[-1:]
            self.assertEqual(await extractor.extract(payload),[])
            self.assertEqual(len(calls),2)
        finally:
            await provider.close()

    def test_section_validation_none_bounds_and_unicode(self):
        for text in ("NONE"," NONE ","闲聊","## 标题\n过短",""):
            self.assertEqual(parse_sections(text),[])
        sections=parse_sections("## "+"中"*199+"🙂尾\n"+"知识"*120+"\n---\n## 第二\n"+"🙂"*100)
        self.assertEqual(len(sections),2)
        self.assertEqual(sections[0]["title"],"中"*199)
        for bad in ("\x00", "x"*200001,"---".join("## 标题\n"+"知"*200 for _ in range(21))):
            with self.assertRaises(ChatError):parse_sections(bad)

class ExtractionHTTPTests(unittest.TestCase):
    def test_internal_auth_scope_schema_and_safe_failures(self):
        class Fake:
            async def extract(self,payload):
                if payload.history[0].content=="failure":raise ChatError("chat_rate_limited")
                return [{"title":"标题","content":"知识"*120}]
        token="local-token-"+"x"*32
        with TestClient(create_app(token,extractor=Fake())) as client:
            payload={"request_id":str(uuid4()),"history":[{"role":"user","content":"问题"}]}
            self.assertEqual(client.post("/internal/v1/extract",json=payload).status_code,401)
            headers={"Authorization":"Bearer "+token}
            response=client.post("/internal/v1/extract",json=payload,headers=headers)
            self.assertEqual(response.status_code,200)
            self.assertEqual(response.json()["request_id"],payload["request_id"])
            self.assertEqual(len(response.json()["sections"]),1)
            payload["mode"]="invalid"
            self.assertEqual(client.post("/internal/v1/extract",json=payload,headers=headers).status_code,422)
            payload.pop("mode");payload["history"][0]["content"]="failure"
            response=client.post("/internal/v1/extract",json=payload,headers=headers)
            self.assertEqual(response.status_code,503)
            self.assertNotIn("failure",response.text)
