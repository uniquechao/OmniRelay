ALTER TABLE feishu_drive_group_memberships ADD COLUMN member_names jsonb NOT NULL DEFAULT '{}'::jsonb;
UPDATE feishu_drive_group_memberships SET next_at=now();

-- Revisit legacy card summaries through the usual authenticated history worker.
UPDATE feishu_chat_sync s SET full_at=NULL,next_at=now()
WHERE EXISTS (SELECT FROM feishu_chat_messages m
    WHERE (m.tenant_key,m.app_id,m.chat_id)=(s.tenant_key,s.app_id,s.chat_id)
      AND m.message_type='interactive' AND NOT m.deleted);
