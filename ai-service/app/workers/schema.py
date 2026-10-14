async def check_schema(conn):
    columns = {"knowledge_entry": {"id": "uuid", "kb_id": "uuid", "content": "text", "entry_version": "int8"}, "index_task": {"id": "uuid", "entry_id": "uuid", "kb_id": "uuid", "entry_version": "int8", "trace_id": "varchar", "status": "text"}, "index_receipt": {"task_id": "uuid", "result": "jsonb", "created_at": "timestamptz"}}
    cursor = await conn.execute("SELECT table_name,column_name,udt_name,is_nullable FROM information_schema.columns WHERE table_schema='public'")
    actual = {(row["table_name"],row["column_name"]):(row["udt_name"],row["is_nullable"]) for row in await cursor.fetchall()}
    if any(actual.get((table,column)) != (kind,"NO") for table, fields in columns.items() for column,kind in fields.items()):
        raise ValueError("missing or incompatible indexing columns")
    cursor = await conn.execute("""SELECT
EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('public.knowledge_chunk') AND attname='embedding' AND attnotnull AND format_type(atttypid,atttypmod)='vector(1024)') AS vector,
EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass('public.index_receipt') AND contype='f' AND confrelid=to_regclass('public.index_task') AND confdeltype='c' AND convalidated) AS foreign_key,
EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid=to_regclass('public.index_receipt') AND contype='p' AND convalidated) AS primary_key""")
    row = await cursor.fetchone()
    if not all(row.values()):
        raise ValueError("missing or incompatible indexing constraints")
