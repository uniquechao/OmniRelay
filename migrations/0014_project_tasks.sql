CREATE TABLE project_task_boards (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    project_id text NOT NULL,
    business_timezone text NOT NULL DEFAULT 'Asia/Shanghai',
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    PRIMARY KEY (tenant_key, app_id, project_id)
);

CREATE TABLE project_milestones (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL,
    id text NOT NULL DEFAULT gen_random_uuid()::text,
    title text NOT NULL, occurs_on date, task_id text,
    PRIMARY KEY (tenant_key, app_id, project_id, id),
    FOREIGN KEY (tenant_key, app_id, project_id) REFERENCES project_task_boards
);

CREATE TABLE project_tasks (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL,
    id text NOT NULL DEFAULT gen_random_uuid()::text,
    title text NOT NULL, description text NOT NULL DEFAULT '',
    owner_member_id text, milestone_id text, due_at text,
    priority text CHECK (priority IN ('P0','P1','P2','P3')),
    tag_ids text[] NOT NULL DEFAULT '{}',
    column_id text NOT NULL DEFAULT 'todo' CHECK (column_id IN ('todo','in_progress','blocked','done')),
    status text NOT NULL DEFAULT 'todo' CHECK (status = column_id OR status = 'cancelled'),
    position bigint NOT NULL, version bigint NOT NULL DEFAULT 1,
    blocked_reason text NOT NULL DEFAULT '', completion_note text NOT NULL DEFAULT '', change_reason text NOT NULL DEFAULT '',
    PRIMARY KEY (tenant_key, app_id, project_id, id),
    FOREIGN KEY (tenant_key, app_id, project_id) REFERENCES project_task_boards,
    FOREIGN KEY (tenant_key, app_id, project_id, milestone_id) REFERENCES project_milestones
);
ALTER TABLE project_milestones ADD FOREIGN KEY (tenant_key, app_id, project_id, task_id) REFERENCES project_tasks;
CREATE INDEX project_tasks_board_order ON project_tasks (tenant_key, app_id, project_id, column_id, position, id);

CREATE TABLE project_task_moves (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL,
    account_id text NOT NULL, request_key text NOT NULL, command jsonb NOT NULL, result jsonb NOT NULL,
    PRIMARY KEY (tenant_key, app_id, project_id, account_id, request_key),
    FOREIGN KEY (tenant_key, app_id, project_id) REFERENCES project_task_boards
);
