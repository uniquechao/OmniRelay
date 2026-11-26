ALTER TABLE feishu_inbound_work DROP CONSTRAINT feishu_inbound_work_check;
ALTER TABLE feishu_inbound_work ADD CHECK (
    (state IN ('pending_route','candidate','agent_routed','agent_denied','agent_disabled','unbound_group','historical_unrouted') AND account_id IS NOT NULL)
    OR (state='admission_denied' AND account_id IS NULL) OR state='candidate_cancelled'
);
UPDATE feishu_inbound_work w SET state='candidate' FROM feishu_candidate_message_receipts r
WHERE (r.tenant_key,r.app_id,r.source_id)=(w.tenant_id,w.app_id,w.message_id) AND w.state='pending_route';
UPDATE feishu_inbound_work SET state='historical_unrouted' WHERE state='pending_route';
ALTER TABLE feishu_inbound_work DROP CONSTRAINT feishu_inbound_work_agent_work_id_key;
CREATE INDEX feishu_inbound_agent_reference ON feishu_inbound_work(agent_work_id) WHERE agent_work_id IS NOT NULL;

ALTER TABLE agent_group_work ADD COLUMN intent text NOT NULL DEFAULT 'project' CHECK(intent IN ('project','knowledge','admin')),
    ADD COLUMN direct_question text, ADD COLUMN choice_prompt text, ADD COLUMN choice_grants jsonb, ADD COLUMN parent_work text REFERENCES agent_group_work,
    ADD COLUMN write_requested boolean NOT NULL DEFAULT false,
    ADD COLUMN legacy_output boolean NOT NULL DEFAULT false, ADD COLUMN config_snapshot jsonb, ADD COLUMN applied_config_version bigint;
-- Prior Runtime journals predate slot attestations. Preserve their committed
-- receipts without granting permission for new calls on an old work.
UPDATE agent_group_work SET legacy_output=true;
UPDATE agent_group_work SET intent='knowledge' WHERE mode='private_reply';
CREATE UNIQUE INDEX agent_one_project_delegation ON agent_group_work(parent_work, project_id) WHERE parent_work IS NOT NULL;
ALTER TABLE agent_instances DROP CONSTRAINT agent_instances_tenant_key_app_id_project_id_key;
ALTER TABLE agent_group_work DROP CONSTRAINT agent_group_work_state_check;
ALTER TABLE agent_group_work ADD CHECK(state IN ('route','running','ready','sending','sent','suppressed','expired','unknown','failed','awaiting_project'));

-- Only external delivery requires a platform-issued message identifier.
ALTER TABLE agent_group_work DROP CONSTRAINT agent_group_work_check1;
ALTER TABLE agent_group_work ADD CHECK(state!='sent' OR channel='workbench' OR platform_message_id IS NOT NULL);
UPDATE agent_group_work SET platform_message_id=NULL WHERE channel='workbench' AND platform_message_id=delivery_id::text;

ALTER TABLE agent_project_tool_reads ADD COLUMN citations text[];
-- Permanent tool rejections are reported by slot alongside committed mutations.
-- Transient transport failures retain their original retry semantics.
CREATE TABLE agent_project_tool_failures (
    work_id text NOT NULL REFERENCES agent_group_work(id) ON DELETE CASCADE,
    slot integer NOT NULL CHECK (slot BETWEEN 1 AND 128),
    tool text NOT NULL, fingerprint text NOT NULL,
    status integer NOT NULL CHECK(status IN (400,403,409)),
    target_hint text NOT NULL,
    PRIMARY KEY (work_id,slot)
);
