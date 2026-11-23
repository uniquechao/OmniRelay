CREATE TABLE project_daily_briefs (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    project_id text NOT NULL,
    business_date date NOT NULL,
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    body text NOT NULL CHECK (length(body) > 0),
    version bigint NOT NULL CHECK (version > 0),
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_key, app_id, project_id, business_date),
    FOREIGN KEY (tenant_key, app_id, updated_by) REFERENCES accounts (tenant_key, app_id, id)
);
