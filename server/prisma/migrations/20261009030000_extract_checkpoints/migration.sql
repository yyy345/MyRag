CREATE TABLE extract_checkpoint (
    kb_id UUID PRIMARY KEY REFERENCES knowledge_base(id) ON DELETE CASCADE,
    last_message_at TIMESTAMP(3) NOT NULL,
    completed_day DATE,
    lease_token UUID,
    lease_until TIMESTAMPTZ,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT extract_checkpoint_lease_check CHECK ((lease_token IS NULL) = (lease_until IS NULL))
);
