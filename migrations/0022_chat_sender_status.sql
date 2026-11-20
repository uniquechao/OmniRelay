CREATE TABLE feishu_chat_sender_checks (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    open_id text NOT NULL,
    resigned boolean,
    checked_at timestamptz NOT NULL DEFAULT statement_timestamp(),
    PRIMARY KEY (tenant_key, app_id, open_id),
    FOREIGN KEY (tenant_key, app_id) REFERENCES directory_sources (tenant_key, app_id)
);

CREATE INDEX feishu_chat_user_senders ON feishu_chat_messages
    (tenant_key, app_id, chat_id, sender_id) WHERE sender_kind = 'user';
