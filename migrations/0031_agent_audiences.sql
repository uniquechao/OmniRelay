-- One grant for one scheduled occurrence, with immutable per-audience snapshots.
CREATE TABLE agent_notice_plans (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id text NOT NULL REFERENCES agent_instances, creator text NOT NULL,
    creator_generation text NOT NULL, creator_permissions bigint NOT NULL,
    channel text NOT NULL, bot text NOT NULL, config_version bigint NOT NULL,
    due_at timestamptz NOT NULL, native_id text, enabled boolean NOT NULL DEFAULT true,
    request_key text NOT NULL, definition jsonb NOT NULL,
    UNIQUE(agent_id,creator,request_key), UNIQUE(agent_id,native_id)
);
CREATE TABLE agent_notice_targets (
    plan_id uuid NOT NULL REFERENCES agent_notice_plans,
    mode text NOT NULL CHECK(mode IN ('group_notice','person_notice')),
    target text NOT NULL, recipient text NOT NULL,
    assignment_version bigint NOT NULL, membership_generation text NOT NULL,
    permission_version bigint NOT NULL,
    PRIMARY KEY(plan_id,mode,target)
);
ALTER TABLE agent_group_work
    ADD COLUMN mode text NOT NULL DEFAULT 'group_reply'
        CHECK(mode IN ('group_reply','private_reply','group_notice','person_notice')),
    ADD COLUMN plan_id uuid REFERENCES agent_notice_plans,
    ADD CONSTRAINT notice_source CHECK((plan_id IS NOT NULL)=(mode IN ('group_notice','person_notice'))),
    ADD FOREIGN KEY(plan_id,mode,chat_id) REFERENCES agent_notice_targets(plan_id,mode,target);
ALTER TABLE agent_group_work DROP CONSTRAINT agent_group_work_tenant_key_app_id_channel_message_id_key;
CREATE UNIQUE INDEX agent_reply_source ON agent_group_work(tenant_key,app_id,channel,message_id) WHERE plan_id IS NULL;
CREATE UNIQUE INDEX agent_notice_occurrence ON agent_group_work(plan_id,mode,chat_id) WHERE plan_id IS NOT NULL;
