CREATE TABLE feishu_drive_group_memberships (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    chat_id text NOT NULL,
    generation bigint NOT NULL,
    members text[] NOT NULL DEFAULT '{}',
    verified_at timestamptz,
    next_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_key, app_id, chat_id),
    FOREIGN KEY (tenant_key, app_id, chat_id) REFERENCES feishu_chat_conversations ON DELETE CASCADE
);

CREATE INDEX feishu_drive_group_memberships_due
    ON feishu_drive_group_memberships (tenant_key, app_id, next_at);
