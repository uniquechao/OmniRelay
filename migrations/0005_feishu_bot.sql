CREATE TABLE feishu_bot_configs (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    credentials_generation bigint NOT NULL CHECK (credentials_generation > 0),
    enabled boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 0 CHECK (version >= 0),
    bot_name text NOT NULL DEFAULT '',
    bot_open_id text NOT NULL DEFAULT '',
    connection_generation bigint NOT NULL DEFAULT 0 CHECK (connection_generation >= 0),
    connection_state text NOT NULL DEFAULT 'unavailable'
        CHECK (connection_state IN ('connecting', 'connected', 'reconnecting', 'unavailable')),
    last_heartbeat_at timestamptz,
    last_message_at timestamptz,
    PRIMARY KEY (tenant_key, app_id)
);

CREATE TABLE feishu_bot_checks (
    id text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    credentials_generation bigint NOT NULL CHECK (credentials_generation > 0),
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued', 'running', 'succeeded', 'failed')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at timestamptz,
    finished_at timestamptz,
    claim_version bigint NOT NULL DEFAULT 0,
    lease_until timestamptz,
    results jsonb NOT NULL DEFAULT '[]',
    UNIQUE (tenant_key, app_id, id, credentials_generation),
    FOREIGN KEY (tenant_key, app_id) REFERENCES feishu_bot_configs (tenant_key, app_id),
    CHECK ((state = 'running') = (lease_until IS NOT NULL)),
    CHECK ((state = 'queued') = (started_at IS NULL)),
    CHECK ((state IN ('succeeded', 'failed')) = (finished_at IS NOT NULL))
);

CREATE UNIQUE INDEX feishu_bot_checks_active_idx
    ON feishu_bot_checks (tenant_key, app_id, credentials_generation)
    WHERE state IN ('queued', 'running');

CREATE TABLE feishu_bot_check_keys (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    key text NOT NULL,
    credentials_generation bigint NOT NULL,
    fingerprint text NOT NULL,
    check_id text NOT NULL,
    PRIMARY KEY (tenant_key, app_id, key),
    FOREIGN KEY (tenant_key, app_id, check_id, credentials_generation)
        REFERENCES feishu_bot_checks (tenant_key, app_id, id, credentials_generation)
);
