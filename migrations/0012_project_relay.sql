CREATE TABLE project_relay_settings (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    project_id text NOT NULL,
    target_chat_id text,
    timezone text NOT NULL,
    local_time text NOT NULL,
    plan_enabled boolean NOT NULL DEFAULT false,
    public_submission_count boolean NOT NULL DEFAULT false,
    version bigint NOT NULL CHECK (version > 0),
    PRIMARY KEY (tenant_key, app_id, project_id),
    UNIQUE (tenant_key, app_id, target_chat_id),
    FOREIGN KEY (tenant_key, app_id, target_chat_id)
        REFERENCES feishu_chat_conversations (tenant_key, app_id, chat_id)
);

-- A project can link several groups; the briefing target is selected independently.
CREATE TABLE project_chat_links (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    project_id text NOT NULL,
    chat_id text NOT NULL,
    PRIMARY KEY (tenant_key, app_id, chat_id),
    FOREIGN KEY (tenant_key, app_id, project_id)
        REFERENCES project_relay_settings (tenant_key, app_id, project_id),
    FOREIGN KEY (tenant_key, app_id, chat_id)
        REFERENCES feishu_chat_conversations (tenant_key, app_id, chat_id)
);
CREATE INDEX project_chat_links_project_idx ON project_chat_links (tenant_key, app_id, project_id, chat_id);

CREATE TABLE project_briefing_runs (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    project_id text NOT NULL,
    business_date date NOT NULL,
    id text NOT NULL DEFAULT gen_random_uuid()::text,
    account_id text NOT NULL,
    target_chat_id text,
    settings_version bigint NOT NULL,
    source_revision text NOT NULL,
    visible_submissions integer NOT NULL CHECK (visible_submissions >= 0),
    generated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_key, app_id, project_id, business_date),
    UNIQUE (tenant_key, app_id, id),
    FOREIGN KEY (tenant_key, app_id, account_id) REFERENCES accounts (tenant_key, app_id, id),
    FOREIGN KEY (tenant_key, app_id, target_chat_id)
        REFERENCES feishu_chat_conversations (tenant_key, app_id, chat_id)
);

-- E1 retains the time of positive confirmation, including reconciliation.
ALTER TABLE feishu_send_parts ADD COLUMN confirmed_at timestamptz;
