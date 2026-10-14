-- Python owns only chunk writes and the corresponding durable result receipt.
-- Business task/entry status remains owned by the Go result consumer.
CREATE TABLE index_receipt (
    task_id UUID PRIMARY KEY REFERENCES index_task(id) ON DELETE CASCADE,
    result JSONB NOT NULL CHECK (jsonb_typeof(result) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
