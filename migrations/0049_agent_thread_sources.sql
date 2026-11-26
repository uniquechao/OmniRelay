-- A digest records which audience-visible source revisions informed the turn.
-- Historical work without injected thread context has no digest.
ALTER TABLE agent_group_work ADD COLUMN thread_source_digest bytea;
-- Earlier file_get calls returned only a file reference or the first local
-- excerpt. Preserve the evidence that the original answer actually used.
UPDATE agent_project_tool_reads SET tool='file_reference_check' WHERE tool='file_get';
CREATE INDEX agent_active_thread_order ON agent_group_work
    (tenant_key,app_id,channel,chat_id,thread_id,assignment_version,group_asked_at,id)
    WHERE mode='group_reply' AND thread_id<>'' AND state IN ('route','running','ready','sending');
