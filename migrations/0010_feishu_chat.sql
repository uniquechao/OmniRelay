CREATE TABLE feishu_chat_conversations (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    chat_id text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('group', 'p2p')),
    peer_open_id text NOT NULL DEFAULT '',
    name text NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_key, app_id, chat_id),
    FOREIGN KEY (tenant_key, app_id) REFERENCES feishu_bot_configs (tenant_key, app_id),
    CHECK ((kind = 'p2p') = (peer_open_id <> ''))
);

CREATE TABLE feishu_chat_messages (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    message_id text NOT NULL,
    chat_id text NOT NULL,
    sent_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    sender_id text NOT NULL DEFAULT '',
    sender_kind text NOT NULL DEFAULT '',
    sender_name text NOT NULL DEFAULT '',
    message_type text NOT NULL DEFAULT '',
    thread_id text NOT NULL DEFAULT '',
    parent_id text NOT NULL DEFAULT '',
    body text NOT NULL DEFAULT '',
    search_text text NOT NULL DEFAULT '',
    deleted boolean NOT NULL DEFAULT false,
    visible_in_chat boolean NOT NULL DEFAULT false,
    PRIMARY KEY (tenant_key, app_id, message_id),
    CHECK (NOT deleted OR (body = '' AND search_text = ''))
);

CREATE INDEX feishu_chat_timeline
    ON feishu_chat_messages (tenant_key, app_id, chat_id, sent_at DESC, message_id DESC);

CREATE TABLE feishu_chat_sync (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    chat_id text NOT NULL,
    thread_id text NOT NULL DEFAULT '',
    page_token text NOT NULL DEFAULT '',
    range_start timestamptz,
    range_end timestamptz,
    covered_until timestamptz,
    full_at timestamptz,
    source_blocked boolean NOT NULL DEFAULT false,
    attempted_at timestamptz,
    completed_at timestamptz,
    next_at timestamptz NOT NULL DEFAULT now(),
    state text NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'syncing', 'complete', 'partial', 'failed', 'access_lost')),
    PRIMARY KEY (tenant_key, app_id, chat_id, thread_id),
    FOREIGN KEY (tenant_key, app_id, chat_id) REFERENCES feishu_chat_conversations (tenant_key, app_id, chat_id)
);

CREATE INDEX feishu_chat_sync_due ON feishu_chat_sync (tenant_key, app_id, next_at);

CREATE TABLE feishu_chat_discovery (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    page_token text NOT NULL DEFAULT '',
    next_at timestamptz NOT NULL DEFAULT now(),
    completed_at timestamptz,
    state text NOT NULL DEFAULT 'pending'
        CHECK (state IN ('pending', 'syncing', 'complete', 'failed', 'access_lost')),
    PRIMARY KEY (tenant_key, app_id),
    FOREIGN KEY (tenant_key, app_id) REFERENCES feishu_bot_configs (tenant_key, app_id)
);

-- Existing verified commands identify Bot conversations. Fetch message bodies
-- from Feishu separately so an old command cannot restore a recalled message.
INSERT INTO feishu_chat_conversations (tenant_key, app_id, chat_id, kind, peer_open_id)
SELECT DISTINCT ON (tenant_id, app_id, chat_id)
    tenant_id, app_id, chat_id, chat_kind, CASE WHEN chat_kind='p2p' THEN sender_open_id ELSE '' END
FROM feishu_inbound_events
ORDER BY tenant_id, app_id, chat_id, received_at, message_id;

INSERT INTO feishu_chat_sync (tenant_key, app_id, chat_id)
SELECT tenant_key, app_id, chat_id FROM feishu_chat_conversations;
