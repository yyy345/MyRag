-- Retained independently of user/KB/entry deletion: cleanup must survive cascades.
CREATE TABLE storage_cleanup (
    user_id UUID NOT NULL,
    entry_id UUID NOT NULL,
    available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, entry_id)
);
CREATE INDEX storage_cleanup_pending_idx ON storage_cleanup(available_at, created_at);
