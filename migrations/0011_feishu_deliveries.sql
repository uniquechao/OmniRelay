CREATE TABLE feishu_send_uploads (
    id text PRIMARY KEY,
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    account_id text NOT NULL,
    chat_id text NOT NULL,
    generation bigint NOT NULL,
    filename text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('file', 'image')),
    content_digest bytea NOT NULL,
    resource_key text NOT NULL,
    expires_at timestamptz NOT NULL DEFAULT now() + interval '24 hours',
    FOREIGN KEY (tenant_key, app_id, chat_id)
        REFERENCES feishu_chat_conversations (tenant_key, app_id, chat_id)
);

CREATE TABLE feishu_send_operations (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    operation_key text NOT NULL,
    account_id text NOT NULL,
    chat_id text NOT NULL,
    generation bigint NOT NULL,
    fingerprint bytea NOT NULL,
    bot_open_id text NOT NULL DEFAULT '',
    bot_name text NOT NULL DEFAULT '',
    business_receipt jsonb,
    deadline timestamptz NOT NULL DEFAULT now() + interval '1 minute',
    PRIMARY KEY (tenant_key, app_id, operation_key)
);

CREATE TABLE feishu_send_parts (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    operation_key text NOT NULL,
    position integer NOT NULL CHECK (position >= 0),
    kind text NOT NULL CHECK (kind IN ('text', 'post', 'file', 'image')),
    attachment_id text NOT NULL DEFAULT '',
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'success', 'failed', 'unknown')),
    reason text NOT NULL DEFAULT '',
    attempted boolean NOT NULL DEFAULT false,
    message_id text,
    PRIMARY KEY (tenant_key, app_id, operation_key, position),
    FOREIGN KEY (tenant_key, app_id, operation_key)
        REFERENCES feishu_send_operations (tenant_key, app_id, operation_key),
    CHECK (state <> 'success' OR (attempted AND message_id IS NOT NULL)),
    CHECK (message_id IS NULL OR (attempted AND state IN ('success', 'unknown')))
);

CREATE UNIQUE INDEX feishu_send_message_claim ON feishu_send_parts (tenant_key, app_id, message_id)
    WHERE message_id IS NOT NULL;
