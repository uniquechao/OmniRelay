CREATE TABLE agent_configurations (
    tenant_key text NOT NULL, app_id text NOT NULL, scope text NOT NULL, role text NOT NULL,
    name text NOT NULL, persona text NOT NULL, tools text[], enabled boolean NOT NULL,
    version bigint NOT NULL CHECK(version > 0), applied_version bigint NOT NULL DEFAULT 0,
    failed_version bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (tenant_key,app_id,scope,role),
    CHECK ((role='project-pm' AND scope<>'') OR (role IN ('query','management-pm') AND scope=''))
);
CREATE TABLE agent_configuration_changes (
    tenant_key text NOT NULL, app_id text NOT NULL, scope text NOT NULL, role text NOT NULL,
    version bigint NOT NULL, account_id text NOT NULL, reason text NOT NULL,
    previous jsonb NOT NULL, updated jsonb NOT NULL, changed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(tenant_key,app_id,scope,role,version),
    FOREIGN KEY(tenant_key,app_id,scope,role) REFERENCES agent_configurations,
    FOREIGN KEY(tenant_key,app_id,account_id) REFERENCES accounts(tenant_key,app_id,id)
);
-- An empty workbench thread identifies the existing default conversation. Its native
-- session binding, group receipts and historical messages retain their original IDs.
CREATE TABLE agent_personal_conversations (
    tenant_key text NOT NULL, app_id text NOT NULL, account_id text NOT NULL, project_id text NOT NULL,
    membership_generation text NOT NULL, id text NOT NULL DEFAULT gen_random_uuid()::text,
    title text NOT NULL, request_key text NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_key,app_id,account_id,project_id,membership_generation,id),
    UNIQUE (tenant_key,app_id,account_id,project_id,membership_generation,request_key),
    FOREIGN KEY (tenant_key,app_id,account_id) REFERENCES accounts(tenant_key,app_id,id)
);
CREATE INDEX agent_personal_conversation_messages ON agent_group_work
    (tenant_key,app_id,account_id,project_id,membership_generation,thread_id,asked_at DESC)
    WHERE mode='project_chat';
CREATE INDEX agent_personal_recent_work ON agent_group_work
    (tenant_key,app_id,account_id,deadline DESC,id DESC);
