CREATE TABLE feishu_file_previews (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_key text NOT NULL, app_id text NOT NULL, generation bigint NOT NULL,
    account_id text NOT NULL, open_id text NOT NULL,
    file_id bigint NOT NULL, version_id bigint NOT NULL,
    sources text[] NOT NULL, options text NOT NULL,
    stage text NOT NULL DEFAULT 'queued', metadata jsonb, mime text, body bytea,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (file_id,version_id) REFERENCES feishu_file_versions(file_id,id),
    CHECK (octet_length(body) <= 3145728)
);
CREATE INDEX feishu_preview_age ON feishu_file_previews(created_at);
CREATE INDEX feishu_preview_reader ON feishu_file_previews(tenant_key,app_id,account_id,file_id,version_id);
