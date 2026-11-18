ALTER TABLE accounts
    ADD COLUMN permission_version bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT accounts_access_scope_unique UNIQUE (tenant_key, app_id, id);

CREATE TABLE access_bootstraps (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    account_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_key, app_id),
    FOREIGN KEY (tenant_key, app_id, account_id) REFERENCES accounts (tenant_key, app_id, id)
);

CREATE TABLE role_grants (
    id text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    account_id text NOT NULL,
    project_id text,
    membership_generation text,
    role text NOT NULL,
    source text NOT NULL,
    granted_by text,
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    FOREIGN KEY (tenant_key, app_id, account_id) REFERENCES accounts (tenant_key, app_id, id),
    FOREIGN KEY (tenant_key, app_id, granted_by) REFERENCES accounts (tenant_key, app_id, id),
    CHECK (
        (project_id IS NULL AND membership_generation IS NULL AND role IN ('system_admin', 'system_operator', 'audit_viewer'))
        OR (
            project_id IS NOT NULL AND project_id <> ''
            AND membership_generation IS NOT NULL AND membership_generation <> ''
            AND role = 'project_admin'
        )
    ),
    CHECK (
        (source = 'bootstrap' AND role = 'system_admin' AND granted_by IS NULL)
        OR (source = 'platform_admin' AND project_id IS NULL AND granted_by IS NOT NULL)
        OR (source = 'project_manager' AND project_id IS NOT NULL AND granted_by IS NOT NULL)
    )
);

CREATE UNIQUE INDEX role_grants_active_scope_idx
    ON role_grants (tenant_key, app_id, account_id, role, coalesce(project_id, ''))
    WHERE revoked_at IS NULL;
CREATE INDEX role_grants_scope_listing_idx
    ON role_grants (tenant_key, app_id, project_id, id)
    WHERE revoked_at IS NULL;

CREATE TABLE audit_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    actor_account_id text NOT NULL,
    action text NOT NULL,
    resource_type text NOT NULL,
    resource_id text NOT NULL,
    result text NOT NULL,
    change_version bigint NOT NULL,
    correlation_id text NOT NULL DEFAULT gen_random_uuid()::text,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (tenant_key, app_id, actor_account_id) REFERENCES accounts (tenant_key, app_id, id)
);

CREATE INDEX audit_events_scope_listing_idx ON audit_events (tenant_key, app_id, id DESC);
