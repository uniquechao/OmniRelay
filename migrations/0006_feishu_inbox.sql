ALTER TABLE feishu_bot_configs ADD COLUMN connection_lease_until timestamptz;

CREATE TABLE feishu_inbound_events (
    app_id text NOT NULL,
    tenant_id text NOT NULL,
    message_id text NOT NULL,
    event_id text NOT NULL,
    chat_id text NOT NULL,
    chat_kind text NOT NULL CHECK (chat_kind IN ('p2p', 'group')),
    thread_id text NOT NULL,
    reply_to_message_id text NOT NULL,
    sender_open_id text NOT NULL,
    received_at timestamptz NOT NULL,
    body text NOT NULL,
    config_version bigint NOT NULL CHECK (config_version >= 0),
    connection_generation bigint NOT NULL CHECK (connection_generation > 0),
    PRIMARY KEY (app_id, tenant_id, message_id),
    FOREIGN KEY (tenant_id, app_id) REFERENCES feishu_bot_configs (tenant_key, app_id)
);

CREATE TABLE feishu_inbound_work (
    id text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    app_id text NOT NULL,
    tenant_id text NOT NULL,
    message_id text NOT NULL,
    account_id text REFERENCES accounts (id),
    state text NOT NULL CHECK (state IN ('pending_route', 'admission_denied')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (app_id, tenant_id, message_id),
    FOREIGN KEY (app_id, tenant_id, message_id) REFERENCES feishu_inbound_events (app_id, tenant_id, message_id),
    CHECK ((state = 'pending_route') = (account_id IS NOT NULL))
);

CREATE INDEX feishu_inbound_pending_idx ON feishu_inbound_work (app_id, tenant_id)
    WHERE state = 'pending_route';
