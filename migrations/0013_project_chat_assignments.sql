-- Group links have a lifecycle independent of briefing configuration.
ALTER TABLE project_chat_links
    DROP CONSTRAINT project_chat_links_tenant_key_app_id_project_id_fkey,
    ADD UNIQUE (tenant_key, app_id, project_id, chat_id);
DROP INDEX project_chat_links_project_idx;

ALTER TABLE feishu_chat_conversations
    ADD COLUMN project_assignment_version bigint NOT NULL DEFAULT 0 CHECK (project_assignment_version >= 0);

-- Keep the revision even when a project has no primary group.
CREATE TABLE project_chat_primary (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    project_id text NOT NULL,
    chat_id text,
    version bigint NOT NULL CHECK (version > 0),
    PRIMARY KEY (tenant_key, app_id, project_id),
    FOREIGN KEY (tenant_key, app_id, project_id, chat_id)
        REFERENCES project_chat_links (tenant_key, app_id, project_id, chat_id)
);

-- Preserve the primary markers previously derived from the configured targets.
INSERT INTO project_chat_primary (tenant_key, app_id, project_id, chat_id, version)
SELECT l.tenant_key, l.app_id, l.project_id, l.chat_id, 1
FROM project_relay_settings s JOIN project_chat_links l
    ON (l.tenant_key, l.app_id, l.project_id, l.chat_id) =
       (s.tenant_key, s.app_id, s.project_id, s.target_chat_id)
JOIN feishu_chat_conversations c
    ON (c.tenant_key, c.app_id, c.chat_id) = (l.tenant_key, l.app_id, l.chat_id)
WHERE c.kind = 'group';

CREATE VIEW project_chat_assignments AS
SELECT c.tenant_key, c.app_id, c.chat_id, l.project_id,
    (p.chat_id IS NOT NULL) AS primary_group, c.project_assignment_version AS version
FROM feishu_chat_conversations c
LEFT JOIN project_chat_links l USING (tenant_key, app_id, chat_id)
LEFT JOIN project_chat_primary p USING (tenant_key, app_id, project_id, chat_id)
WHERE c.kind='group';

ALTER TABLE audit_events ADD COLUMN details jsonb;
