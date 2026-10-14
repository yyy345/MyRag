from app.providers.chat import ChatError
from app.rag.retriever import RetrievalScopeError

DEFAULT_SYSTEM_PROMPT="你是一个专业的知识库助手，请根据提供的参考资料回答问题。如果参考资料中没有相关信息，请如实告知用户。回答时请引用具体的参考资料编号。"


def utf16_prefix(text,limit):
    return text.encode("utf-16-le")[:limit*2].decode("utf-16-le",errors="ignore")


def build_prompt(payload,hits):
    context="\n\n".join(f"[参考资料{i+1}]\n标题: {item['entry_title']}\n内容: {item['content']}" for i,item in enumerate(hits))
    messages=[{"role":"system","content":payload.system_prompt or DEFAULT_SYSTEM_PROMPT}]
    messages.extend({"role":message.role,"content":message.content} for message in payload.history)
    messages.append({"role":"user","content":f"参考资料：\n{context}\n\n用户问题：{payload.query}"})
    references=[{"id":item["entry_id"],"title":item["entry_title"],"content":utf16_prefix(item["content"],200)} for item in hits]
    return messages,references


class Generator:
    def __init__(self,retriever,model):
        self.retriever,self.model=retriever,model

    async def authorize(self,payload):
        async with self.retriever.pool.connection() as conn:
            await self.retriever.authorize(conn,payload.user_id,payload.kb_id)

    async def stream(self,payload):
        hits=await self.retriever.search(payload.user_id,payload.kb_id,payload.query)
        messages,references=build_prompt(payload,hits)
        source=self.model.stream(messages)
        try:
            async for event in source:
                result={**event,"request_id":str(payload.request_id)}
                if event["type"]=="done":
                    result.update(references=references,hit_chunk_ids=[item["chunk_id"] for item in hits])
                yield result
        finally:
            await source.aclose()
