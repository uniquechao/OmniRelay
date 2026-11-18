ALTER TABLE feishu_bot_configs
    DROP CONSTRAINT feishu_bot_configs_connection_state_check,
    ALTER COLUMN connection_state SET DEFAULT 'disabled',
    ADD CONSTRAINT feishu_bot_configs_connection_state_check
        CHECK (connection_state IN ('disabled', 'connecting', 'connected', 'reconnecting', 'unavailable'));

UPDATE feishu_bot_configs SET connection_state='disabled' WHERE NOT enabled;
