ALTER TABLE agent_group_work DROP CONSTRAINT agent_group_work_mode_check;
ALTER TABLE agent_group_work ADD CHECK (mode IN ('group_reply','private_reply','group_notice','person_notice','project_chat'));
ALTER TABLE agent_group_work ADD COLUMN question text, ADD COLUMN asked_at timestamptz;
ALTER TABLE agent_group_work ADD CHECK ((mode='project_chat')=(question IS NOT NULL AND asked_at IS NOT NULL));
CREATE INDEX agent_project_chat_history ON agent_group_work(tenant_key,app_id,account_id,project_id,asked_at DESC)
    WHERE mode='project_chat';
