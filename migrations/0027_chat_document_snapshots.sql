ALTER TABLE feishu_chat_messages ADD COLUMN documents_indexed boolean NOT NULL DEFAULT false;
CREATE INDEX feishu_chat_documents_unindexed ON feishu_chat_messages (tenant_key, app_id, sent_at DESC)
    WHERE NOT deleted AND NOT documents_indexed AND body LIKE '%https:%';

CREATE TABLE feishu_chat_documents (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    message_id text NOT NULL,
    kind text NOT NULL,
    token text NOT NULL,
    generation bigint NOT NULL DEFAULT 0,
    object_kind text NOT NULL DEFAULT '',
    title text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'pending',
    content bytea,
    next_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_key, app_id, message_id, kind, token),
    FOREIGN KEY (tenant_key, app_id, message_id) REFERENCES feishu_chat_messages ON DELETE CASCADE
);
CREATE INDEX feishu_chat_documents_due ON feishu_chat_documents (tenant_key, app_id, next_at);

CREATE FUNCTION invalidate_chat_documents() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM feishu_chat_documents WHERE (tenant_key, app_id, message_id) = (OLD.tenant_key, OLD.app_id, OLD.message_id);
    NEW.documents_indexed := false;
    RETURN NEW;
END $$;
CREATE TRIGGER invalidate_chat_documents BEFORE UPDATE OF body, message_type, deleted ON feishu_chat_messages
    FOR EACH ROW WHEN ((OLD.body, OLD.message_type, OLD.deleted) IS DISTINCT FROM (NEW.body, NEW.message_type, NEW.deleted))
    EXECUTE FUNCTION invalidate_chat_documents();
