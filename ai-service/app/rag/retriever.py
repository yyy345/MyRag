from uuid import UUID

from app.providers.embeddings import validate_vector


class RetrievalScopeError(Exception):
    pass


def reciprocal_rank_fusion(vector, fulltext, k=60, limit=5):
    if k<=0 or not 1<=limit<=100:
        raise ValueError("invalid fusion limits")
    results = {}
    scores = {}
    for source in (vector, fulltext):
        seen = set()
        for rank, item in enumerate(source):
            key = item["chunk_id"]
            if key in seen:
                continue
            seen.add(key)
            results.setdefault(key,item)
            scores[key] = scores.get(key,0.0)+1/(k+rank+1)
    # Stable sorting keeps vector-first insertion order for equal RRF scores,
    # matching JS Map + stable Array.sort. Preserve source score in the payload.
    return [results[key] for key in sorted(results,key=lambda key: -scores[key])[:limit]]


class Retriever:
    def __init__(self,pool,embeddings=None):
        self.pool,self.embeddings=pool,embeddings

    async def authorize(self,conn,user,kb):
        try:
            user,kb=UUID(str(user)),UUID(str(kb))
        except ValueError:
            raise RetrievalScopeError("invalid_retrieval_scope") from None
        cursor=await conn.execute("SELECT 1 FROM knowledge_base WHERE id=%s AND user_id=%s",(kb,user))
        if await cursor.fetchone() is None:
            raise RetrievalScopeError("unauthorized_retrieval_scope")

    async def vector(self,conn,user,kb,vector,limit=10):
        if not 1<=limit<=100:
            raise ValueError("invalid vector limit")
        vector="["+",".join(str(value) for value in validate_vector(vector))+"]"
        cursor=await conn.execute("""SELECT c.id::text AS chunk_id,c.entry_id::text AS entry_id,
e.title AS entry_title,c.content,1-(c.embedding <=> %s::vector) AS score
FROM knowledge_chunk c JOIN knowledge_entry e ON e.id=c.entry_id JOIN knowledge_base k ON k.id=e.kb_id
WHERE e.kb_id=%s AND k.user_id=%s AND e.status='ENABLED' AND e.processing_status='COMPLETED'
ORDER BY c.embedding <=> %s::vector,c.id LIMIT %s""",(vector,kb,user,vector,limit))
        return await cursor.fetchall()

    async def fulltext(self,conn,user,kb,query,limit=5):
        if not 1<=limit<=100:
            raise ValueError("invalid fulltext limit")
        cursor=await conn.execute("""SELECT c.id::text AS chunk_id,c.entry_id::text AS entry_id,
e.title AS entry_title,c.content,ts_rank_cd(to_tsvector('simple',c.content),websearch_to_tsquery('simple',%s)) AS score
FROM knowledge_chunk c JOIN knowledge_entry e ON e.id=c.entry_id JOIN knowledge_base k ON k.id=e.kb_id
WHERE e.kb_id=%s AND k.user_id=%s AND e.status='ENABLED' AND e.processing_status='COMPLETED'
AND to_tsvector('simple',c.content) @@ websearch_to_tsquery('simple',%s)
ORDER BY score DESC,c.id LIMIT %s""",(query,kb,user,query,limit))
        return await cursor.fetchall()

    async def search(self,user,kb,query):
        if not isinstance(query,str) or not query.strip() or len(query)>10_000 or "\x00" in query:
            raise ValueError("invalid retrieval query")
        if self.embeddings is None:
            # Explicit fulltext mode never constructs or calls an embedding provider.
            async with self.pool.connection() as conn:
                async with conn.transaction():
                    await self.authorize(conn,user,kb)
                    keywords=await self.fulltext(conn,user,kb,query)
            return reciprocal_rank_fusion([],keywords)
        # Authorization happens before embedding and is rechecked after the
        # external computation. SQL joins scope every candidate independently.
        async with self.pool.connection() as conn:
            await self.authorize(conn,user,kb)
        vector=await self.embeddings.query(query)
        async with self.pool.connection() as conn:
            async with conn.transaction():
                await self.authorize(conn,user,kb)
                candidates=await self.vector(conn,user,kb,vector)
                keywords=await self.fulltext(conn,user,kb,query)
        return reciprocal_rank_fusion(candidates,keywords)
