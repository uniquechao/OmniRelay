ALTER TABLE feishu_chat_messages
    ADD COLUMN mention_names jsonb NOT NULL DEFAULT '{}'::jsonb;
