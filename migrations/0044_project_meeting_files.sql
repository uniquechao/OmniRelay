CREATE TABLE project_meeting_files (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    project_id text NOT NULL,
    file_ref text NOT NULL,
    marked_by text NOT NULL,
    marked_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_key, app_id, project_id, file_ref)
);
