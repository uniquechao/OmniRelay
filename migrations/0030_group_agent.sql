ALTER TABLE feishu_inbound_work DROP CONSTRAINT feishu_inbound_work_check;
ALTER TABLE feishu_inbound_work ADD CHECK (
    (state IN ('pending_route','candidate','agent_routed','agent_denied') AND account_id IS NOT NULL)
    OR (state='admission_denied' AND account_id IS NULL) OR state='candidate_cancelled'
);

CREATE TABLE agent_instances (
    agent_id text PRIMARY KEY,
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL,
    owner_id text NOT NULL, owner_epoch bigint NOT NULL DEFAULT 1 CHECK(owner_epoch>0),
    UNIQUE (tenant_key,app_id,project_id)
);

-- One immutable intake snapshot per accepted mention, and one original-message reply.
CREATE TABLE agent_group_work (
    id text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tenant_key text NOT NULL, app_id text NOT NULL, channel text NOT NULL,
    account_id text NOT NULL, chat_id text NOT NULL, thread_id text NOT NULL, message_id text NOT NULL,
    config_version bigint NOT NULL,
    UNIQUE (tenant_key,app_id,channel,message_id),
    FOREIGN KEY(tenant_key,app_id,account_id) REFERENCES accounts(tenant_key,app_id,id),
    project_id text NOT NULL, membership_generation text NOT NULL,
    assignment_version bigint NOT NULL, permission_version bigint NOT NULL,
    agent_id text REFERENCES agent_instances, session_id text,
    state text NOT NULL DEFAULT 'route' CHECK(state IN
       ('route','running','ready','sending','sent','suppressed','expired','unknown','failed')),
    generation bigint NOT NULL DEFAULT 0, lease_until timestamptz,
    deadline timestamptz NOT NULL, available_at timestamptz NOT NULL DEFAULT now(),
    bound_generation bigint, bound_turn integer,
    tool_turn integer, tool_receipt jsonb, response_text text, delivery_id uuid NOT NULL UNIQUE DEFAULT gen_random_uuid(),
    platform_message_id text, reason text NOT NULL DEFAULT '',
    CHECK ((tool_turn IS NULL)=(tool_receipt IS NULL)),
    CHECK (state!='sent' OR platform_message_id IS NOT NULL)
);
CREATE INDEX agent_group_due ON agent_group_work (available_at,deadline)
    WHERE state IN ('route','running','ready','sending');


ALTER TABLE feishu_inbound_work ADD COLUMN agent_work_id text UNIQUE REFERENCES agent_group_work(id);
