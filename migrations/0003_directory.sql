CREATE TABLE directory_sources (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    active_version bigint,
    active_scope_hash text,
    visible boolean NOT NULL DEFAULT false,
    last_success_at timestamptz,
    next_run_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    failures int NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_key, app_id)
);

CREATE TABLE directory_sync_runs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    trigger text NOT NULL CHECK (trigger IN ('initial', 'scheduled', 'manual')),
    state text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at timestamptz,
    finished_at timestamptz,
    lease_until timestamptz,
    claim_version bigint NOT NULL DEFAULT 0,
    department_count integer NOT NULL DEFAULT 0,
    people_count integer NOT NULL DEFAULT 0,
    missing_people_count integer NOT NULL DEFAULT 0,
    outside_scope_count integer NOT NULL DEFAULT 0,
    new_people_count integer NOT NULL DEFAULT 0,
    changed_people_count integer NOT NULL DEFAULT 0,
    failure_reason text NOT NULL DEFAULT '',
    scope jsonb,
    UNIQUE (tenant_key, app_id, id),
    FOREIGN KEY (tenant_key, app_id) REFERENCES directory_sources (tenant_key, app_id)
);
CREATE UNIQUE INDEX directory_one_active_run ON directory_sync_runs (tenant_key, app_id)
    WHERE state IN ('queued', 'running');
CREATE INDEX directory_run_history ON directory_sync_runs (tenant_key, app_id, id DESC);

ALTER TABLE directory_sources ADD CONSTRAINT directory_active_run_scope
    FOREIGN KEY (tenant_key, app_id, active_version) REFERENCES directory_sync_runs (tenant_key, app_id, id);

CREATE TABLE departments (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    snapshot_version bigint NOT NULL,
    id text NOT NULL,
    parent_id text,
    name text NOT NULL,
    path text[] NOT NULL,
    has_children boolean NOT NULL,
    PRIMARY KEY (tenant_key, app_id, snapshot_version, id),
    FOREIGN KEY (tenant_key, app_id, snapshot_version) REFERENCES directory_sync_runs (tenant_key, app_id, id),
    FOREIGN KEY (tenant_key, app_id, snapshot_version, parent_id) REFERENCES departments (tenant_key, app_id, snapshot_version, id) DEFERRABLE INITIALLY DEFERRED
);
CREATE INDEX directory_department_parent ON departments (tenant_key, app_id, snapshot_version, parent_id);

CREATE TABLE directory_people (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    snapshot_version bigint NOT NULL,
    open_id text NOT NULL,
    name text NOT NULL,
    avatar_url text NOT NULL,
    directory_status text NOT NULL CHECK (directory_status IN ('active', 'inactive', 'resigned', 'unknown')),
    PRIMARY KEY (tenant_key, app_id, snapshot_version, open_id),
    FOREIGN KEY (tenant_key, app_id, snapshot_version) REFERENCES directory_sync_runs (tenant_key, app_id, id)
);

CREATE TABLE department_memberships (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    snapshot_version bigint NOT NULL,
    department_id text NOT NULL,
    open_id text NOT NULL,
    PRIMARY KEY (tenant_key, app_id, snapshot_version, department_id, open_id),
    FOREIGN KEY (tenant_key, app_id, snapshot_version, department_id) REFERENCES departments (tenant_key, app_id, snapshot_version, id) ON DELETE CASCADE,
    FOREIGN KEY (tenant_key, app_id, snapshot_version, open_id) REFERENCES directory_people (tenant_key, app_id, snapshot_version, open_id) ON DELETE CASCADE
);
CREATE INDEX directory_memberships_by_person ON department_memberships (tenant_key, app_id, snapshot_version, open_id);
