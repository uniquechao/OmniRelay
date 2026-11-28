-- Freeze the exact personal history and its transitive source dependencies on first use.
-- Existing completed turns predate this provenance and remain in the user's history.
ALTER TABLE agent_group_work ADD COLUMN personal_at timestamptz,
    ADD COLUMN personal_displayed_at timestamptz,
    ADD COLUMN prior_displayed_work text REFERENCES agent_group_work(id),
    ADD COLUMN personal_turns text[] CHECK(cardinality(personal_turns)<=4),
    ADD COLUMN personal_sources text[] NOT NULL DEFAULT '{}' CHECK(cardinality(personal_sources)<=32);
UPDATE agent_group_work SET personal_at=coalesce(asked_at,deadline-interval '10 minutes')
    WHERE mode IN ('project_chat','private_reply');
UPDATE agent_group_work SET personal_displayed_at=statement_timestamp()
    WHERE mode IN ('project_chat','private_reply') AND state='sent';
ALTER TABLE agent_group_work ALTER COLUMN personal_at SET DEFAULT clock_timestamp();
CREATE INDEX agent_personal_context_order ON agent_group_work
    (tenant_key,app_id,account_id,project_id,channel,chat_id,thread_id,intent,personal_at DESC,id DESC)
    WHERE mode IN ('project_chat','private_reply') AND parent_work IS NULL;
CREATE INDEX agent_personal_display_order ON agent_group_work
    (tenant_key,app_id,account_id,mode,channel,chat_id,thread_id,personal_displayed_at DESC,personal_at DESC,id DESC)
    WHERE state='sent' AND personal_displayed_at IS NOT NULL AND (question IS NOT NULL OR direct_question IS NOT NULL);
-- The previous runtime may already have accepted a turn on a shared native session.
-- Preserve its journal and all committed receipts without submitting it to a second session.
UPDATE agent_group_work SET state='unknown',reason='personal_session_upgraded',lease_until=NULL
    WHERE mode IN ('project_chat','private_reply') AND (question IS NOT NULL OR direct_question IS NOT NULL)
      AND state='running' AND session_id IS NOT NULL;
-- Project-wide mutable reads (e.g. a task or brief) may legitimately change during
-- the work that writes them. Their revision constrains reuse on a later turn.
ALTER TABLE agent_project_tool_reads ADD COLUMN continuity_only boolean NOT NULL DEFAULT false;

-- Allows current object-level authorization of a separately published confirmation receipt.
ALTER TABLE agent_project_tool_calls ADD COLUMN confirmation_args jsonb;
