CREATE TABLE project_profiles (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    project_id text NOT NULL,
    name text NOT NULL,
    status text NOT NULL CHECK (status IN ('planned','active','paused','blocked','completed','archived')),
    project_type text NOT NULL DEFAULT '',
    background text NOT NULL DEFAULT '',
    version bigint NOT NULL CHECK (version > 0),
    PRIMARY KEY (tenant_key, app_id, project_id)
);

CREATE TABLE project_member_reports (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    project_id text NOT NULL,
    account_id text NOT NULL,
    work_date date NOT NULL,
    note text NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_key, app_id, project_id, work_date, account_id),
    FOREIGN KEY (tenant_key, app_id, project_id, account_id)
        REFERENCES project_members (tenant_key, app_id, project_id, account_id)
);
