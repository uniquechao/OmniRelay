ALTER TABLE project_members
    ADD COLUMN role_title text NOT NULL DEFAULT '' CHECK (char_length(role_title)<=100),
    ADD COLUMN responsibility text NOT NULL DEFAULT '' CHECK (char_length(responsibility)<=4000),
    ADD COLUMN version bigint NOT NULL DEFAULT 1 CHECK (version>0);

-- Retain task history and source references when a task is removed from active views.
ALTER TABLE project_tasks ADD COLUMN deleted_at timestamptz;
CREATE INDEX project_tasks_live ON project_tasks(tenant_key,app_id,project_id,column_id,position,id) WHERE deleted_at IS NULL;
