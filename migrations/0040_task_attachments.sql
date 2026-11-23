CREATE TABLE project_task_attachments (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL,
    task_id text NOT NULL, id uuid NOT NULL, name text NOT NULL, content_type text NOT NULL, content bytea NOT NULL,
    PRIMARY KEY (tenant_key, app_id, project_id, id),
    FOREIGN KEY (tenant_key, app_id, project_id, task_id) REFERENCES project_tasks ON DELETE CASCADE
);
CREATE INDEX project_task_attachments_task ON project_task_attachments (tenant_key,app_id,project_id,task_id);
