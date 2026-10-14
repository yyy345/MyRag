-- Additive migration: old readers remain compatible. New and old writers must not
-- operate on the same entries during cutover (old writers do not bump versions).
ALTER TABLE knowledge_entry ADD COLUMN entry_version BIGINT NOT NULL DEFAULT 1
    CHECK (entry_version > 0);
ALTER TABLE knowledge_entry ADD CONSTRAINT knowledge_entry_id_kb_unique UNIQUE (id,kb_id);

CREATE TABLE index_task (
    id UUID PRIMARY KEY,
    entry_id UUID NOT NULL,
    kb_id UUID NOT NULL,
    entry_version BIGINT NOT NULL CHECK (entry_version > 0),
    trace_id VARCHAR(128) NOT NULL CHECK (length(trace_id) > 0),
    status TEXT NOT NULL DEFAULT 'PENDING'
        CHECK (status IN ('PENDING','PROCESSING','COMPLETED','FAILED','STALE')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (entry_id,entry_version),
    FOREIGN KEY (entry_id,kb_id) REFERENCES knowledge_entry(id,kb_id) ON DELETE CASCADE
);

CREATE TABLE index_outbox (
    task_id UUID PRIMARY KEY REFERENCES index_task(id) ON DELETE CASCADE,
    payload JSONB NOT NULL CHECK (jsonb_typeof(payload)='object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error TEXT
);
CREATE INDEX index_outbox_pending_idx ON index_outbox(available_at,created_at)
    WHERE published_at IS NULL;
