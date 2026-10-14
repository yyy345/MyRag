import json
import os
import unittest
from unittest.mock import patch
from uuid import uuid4

import httpx
from app.api.schemas import ChatRequest
from app.main import create_app
from app.providers.chat import ChatProvider
from app.rag.retriever import Retriever, RetrievalScopeError, reciprocal_rank_fusion
from test_generator import StreamBytes
from test_worker import WorkerIntegrationTests, MockEmbeddings, ENDPOINT


def candidate(key,score=0.5):
    return {"chunk_id":key,"entry_id":key+"entry","entry_title":"title","content":"content","score":score}


class FusionTests(unittest.TestCase):
    def test_rank_fusion_overlap_ties_payload_and_limits(self):
        self.assertEqual(reciprocal_rank_fusion([],[]),[])
        v=[candidate("a",0.9),candidate("b",0.8),candidate("c",0.7)]
        f=[candidate("b",0.1),candidate("d",0.2)]
        result=reciprocal_rank_fusion(v,f)
        self.assertEqual([item["chunk_id"] for item in result],["b","a","d","c"])
        self.assertEqual(result[0]["score"],0.8)
        self.assertEqual([item["chunk_id"] for item in reciprocal_rank_fusion([candidate("a")],[candidate("b")])],["a","b"])
        self.assertEqual(len(reciprocal_rank_fusion([candidate(str(i)) for i in range(20)],[])),5)
        self.assertEqual(reciprocal_rank_fusion([candidate("a"),candidate("a")],[]),[candidate("a")])
        with self.assertRaises(ValueError):
            reciprocal_rank_fusion([],[],k=0)


class QueryEmbeddings(MockEmbeddings):
    async def query(self,text):
        self.calls+=1
        return [1.0]+[0.0]*1023


class RetrievalIntegrationTests(WorkerIntegrationTests):
    # Reuse the isolated database fixture; inherited Worker tests are excluded
    # by load_tests below so each capability is validated once.
    async def fixture(self):
        tasks=[]
        for text in ("alpha beta", "alpha gamma", "知识库 中文", "pending alpha", "disabled alpha"):
            task=await self.task(text)
            tasks.append(task)
            vector=[1.0]+[0.0]*1023 if len(tasks)!=2 else [0.8,0.6]+[0.0]*1022
            async with self.pool.connection() as conn:
                await conn.execute("UPDATE knowledge_entry SET processing_status='COMPLETED' WHERE id=%s",(task.entry_id,))
                await conn.execute("INSERT INTO knowledge_chunk(id,entry_id,chunk_index,content,embedding) VALUES(%s,%s,0,%s,%s::vector)",(uuid4(),task.entry_id,text,"["+",".join(str(x) for x in vector)+"]"))
        async with self.pool.connection() as conn:
            await conn.execute("UPDATE knowledge_entry SET processing_status='PENDING' WHERE id=%s",(tasks[3].entry_id,))
            await conn.execute("UPDATE knowledge_entry SET status='DISABLED' WHERE id=%s",(tasks[4].entry_id,))
        return tasks

    async def test_vector_scope_status_dimensions_and_order(self):
        tasks=await self.fixture()
        retriever=Retriever(self.pool,QueryEmbeddings())
        async with self.pool.connection() as conn:
            results=await retriever.vector(conn,self.user,self.kb,[1.0]+[0.0]*1023)
            self.assertEqual(len(results),3)
            self.assertEqual(results[-1]["entry_id"],str(tasks[1].entry_id))
            self.assertAlmostEqual(results[-1]["score"],0.8,places=5)
            self.assertEqual(await retriever.vector(conn,uuid4(),self.kb,[1.0]+[0.0]*1023),[])
        with self.assertRaises(RetrievalScopeError):
            await retriever.search(uuid4(),self.kb,"alpha")
        self.assertEqual(retriever.embeddings.calls,0)

    async def test_fulltext_chinese_quotes_empty_and_parameterized_input(self):
        tasks=await self.fixture()
        retriever=Retriever(self.pool,QueryEmbeddings())
        async with self.pool.connection() as conn:
            results=await retriever.fulltext(conn,self.user,self.kb,"alpha")
            self.assertEqual({row["entry_id"] for row in results},{str(tasks[0].entry_id),str(tasks[1].entry_id)})
            results=await retriever.fulltext(conn,self.user,self.kb,"知识库")
            self.assertEqual([row["entry_id"] for row in results],[str(tasks[2].entry_id)])
            self.assertEqual(await retriever.fulltext(conn,self.user,self.kb,"unmatched"),[])
            self.assertEqual(await retriever.fulltext(conn,self.user,self.kb,"   "),[])
            self.assertEqual(await retriever.fulltext(conn,uuid4(),self.kb,"alpha"),[])
            results=await retriever.fulltext(conn,self.user,self.kb,"' OR 1=1 --")
            self.assertLessEqual(len(results),3)

    async def test_hybrid_returns_five_unique_hits_and_never_updates_heat(self):
        await self.fixture()
        retriever=Retriever(self.pool,QueryEmbeddings())
        results=await retriever.search(self.user,self.kb,"alpha")
        self.assertEqual(len(results),3)
        self.assertEqual(len({row["chunk_id"] for row in results}),3)
        self.assertEqual(retriever.embeddings.calls,1)
        self.assertTrue(all(row["hits"]==0 for row in await self.fetch("SELECT hit_count AS hits FROM knowledge_chunk")))

    async def test_fulltext_mode_scopes_candidates_without_embeddings(self):
        tasks=await self.fixture()
        retriever=Retriever(self.pool)
        results=await retriever.search(self.user,self.kb,"alpha")
        self.assertEqual({row["entry_id"] for row in results},{str(tasks[0].entry_id),str(tasks[1].entry_id)})
        self.assertIsNone(retriever.embeddings)
        self.assertEqual(await retriever.search(self.user,self.kb,"unmatched"),[])
        with self.assertRaises(RetrievalScopeError):
            await retriever.search(uuid4(),self.kb,"alpha")
        for query in ("", " ", "\x00", "a"*10001):
            with self.assertRaises(ValueError):
                await retriever.search(self.user,self.kb,query)
        self.assertTrue(all(row["hits"]==0 for row in await self.fetch("SELECT hit_count AS hits FROM knowledge_chunk")))

    async def test_fulltext_lifespan_and_sdk_chat_need_only_deepseek_key(self):
        tasks=await self.fixture()
        calls=[]
        providers=[]
        def handler(request):
            calls.append(str(request.url))
            self.assertEqual(str(request.url),"https://api.deepseek.com/chat/completions")
            body=json.loads(request.content)
            self.assertEqual(body["model"],"deepseek-v4-flash")
            self.assertIn("alpha beta",body["messages"][-1]["content"])
            stream=StreamBytes([
                {"id":"local","object":"chat.completion.chunk","created":0,"model":"deepseek-flash","choices":[{"index":0,"delta":{"content":"本地全文检索回答🙂"},"finish_reason":None}]},
                {"id":"local","object":"chat.completion.chunk","created":0,"model":"deepseek-flash","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":4,"total_tokens":16}},
            ])
            return httpx.Response(200,headers={"Content-Type":"text/event-stream"},stream=stream)
        def factory(config):
            provider=ChatProvider(config,httpx.MockTransport(handler))
            providers.append(provider)
            return provider
        env={"AI_PROVIDER_ENABLED":"true","AI_CHAT_PROVIDER":"deepseek","AI_RETRIEVAL_MODE":"fulltext","DEEPSEEK_API_KEY":"mock-deepseek-key","DATABASE_URL":ENDPOINT+self.name}
        with patch.dict(os.environ,env,clear=True), patch("app.providers.chat.ChatProvider",side_effect=factory), patch("app.providers.embeddings.BailianEmbeddings",side_effect=AssertionError("embedding provider must not be allocated")):
            app=create_app(token="local-test-token-at-least-32-characters",vision=object())
            async with app.router.lifespan_context(app):
                self.assertIsNone(app.state.rag.retriever.embeddings)
                async with httpx.AsyncClient(transport=httpx.ASGITransport(app=app),base_url="http://mock.local") as client:
                    self.assertEqual((await client.get("/health/ready")).status_code,200)
                events=[event async for event in app.state.rag.stream(ChatRequest(request_id=uuid4(),user_id=self.user,kb_id=self.kb,query="alpha"))]
                self.assertEqual(events[0]["content"],"本地全文检索回答🙂")
                self.assertEqual({ref["id"] for ref in events[-1]["references"]},{str(tasks[0].entry_id),str(tasks[1].entry_id)})
                self.assertEqual(len(events[-1]["hit_chunk_ids"]),2)
                self.assertEqual(events[-1]["usage"],{"prompt_tokens":12,"completion_tokens":4})
            self.assertTrue(providers[0]._async_http.is_closed)
        self.assertEqual(calls,["https://api.deepseek.com/chat/completions"])

    async def test_invalid_or_hybrid_missing_key_fail_before_clients_are_created(self):
        env={"AI_PROVIDER_ENABLED":"true","AI_CHAT_PROVIDER":"deepseek","DEEPSEEK_API_KEY":"mock-deepseek-key","DATABASE_URL":ENDPOINT+self.name}
        for mode in ("unknown", "hybrid"):
            with self.subTest(mode=mode), patch.dict(os.environ,env | {"AI_RETRIEVAL_MODE":mode},clear=True), patch("app.providers.chat.ChatProvider") as factory:
                app=create_app(token="local-test-token-at-least-32-characters",vision=object())
                with self.assertRaises(ValueError):
                    async with app.router.lifespan_context(app):
                        self.fail("invalid configuration must not start")
                factory.assert_not_called()


def load_tests(loader,tests,pattern):
    suite=unittest.TestSuite(loader.loadTestsFromTestCase(FusionTests))
    for name in ("test_vector_scope_status_dimensions_and_order","test_fulltext_chinese_quotes_empty_and_parameterized_input","test_hybrid_returns_five_unique_hits_and_never_updates_heat","test_fulltext_mode_scopes_candidates_without_embeddings","test_fulltext_lifespan_and_sdk_chat_need_only_deepseek_key","test_invalid_or_hybrid_missing_key_fail_before_clients_are_created"):
        suite.addTest(RetrievalIntegrationTests(name))
    return suite
