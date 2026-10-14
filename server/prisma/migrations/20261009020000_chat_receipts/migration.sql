CREATE TABLE chat_receipt (
    request_id UUID PRIMARY KEY,
    session_id UUID NOT NULL REFERENCES chat_session(id) ON DELETE CASCADE,
    message_id UUID NOT NULL UNIQUE REFERENCES chat_message(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
