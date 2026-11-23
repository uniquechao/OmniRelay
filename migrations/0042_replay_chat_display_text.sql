-- Refresh only histories with legacy display text. The worker rechecks access
-- and merges mentions from each original message under its existing cursor.
UPDATE feishu_chat_sync s
SET full_at = NULL, next_at = LEAST(next_at, now())
WHERE EXISTS (
    SELECT FROM feishu_chat_messages m
    WHERE (m.tenant_key, m.app_id, m.chat_id) = (s.tenant_key, s.app_id, s.chat_id)
      AND (m.thread_id = s.thread_id OR (s.thread_id = '' AND m.visible_in_chat))
      AND NOT m.deleted AND m.message_type IN ('text', 'post')
      AND (m.search_text ~ '@_user_[0-9]+|@_all\y' OR m.search_text ~* '<p([[:space:]]|>)')
);
