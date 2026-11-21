ALTER TABLE feishu_inbound_work DROP CONSTRAINT feishu_inbound_work_state_check;
ALTER TABLE feishu_inbound_work DROP CONSTRAINT feishu_inbound_work_check;
ALTER TABLE feishu_inbound_work ADD CHECK (
    (state IN ('pending_route','candidate') AND account_id IS NOT NULL)
    OR (state = 'admission_denied' AND account_id IS NULL)
    OR state = 'candidate_cancelled'
);
ALTER TABLE feishu_chat_messages ADD COLUMN sdk_received boolean NOT NULL DEFAULT false;

CREATE TABLE feishu_candidate_message_runs (
    id text PRIMARY KEY,
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    generation bigint NOT NULL,
    candidate_sha text NOT NULL,
    candidate_pod text NOT NULL,
    sender_open_id text NOT NULL,
    project_id text NOT NULL,
    group_chat text NOT NULL,
    private_chat text NOT NULL,
    file_token text NOT NULL,
    starts_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    expires_at timestamptz NOT NULL,
    transfer boolean NOT NULL DEFAULT true,
    transfer_stopped_at timestamptz,
    outbound boolean NOT NULL,
    may_reply boolean NOT NULL,
    may_reconcile boolean NOT NULL,
    budget integer NOT NULL CHECK (budget >= 0),
    FOREIGN KEY (tenant_key,app_id) REFERENCES feishu_bot_configs,
    CHECK (group_chat <> '' OR private_chat <> ''),
    CHECK (group_chat = '' OR group_chat <> private_chat),
    CHECK (expires_at > starts_at)
);
CREATE INDEX feishu_candidate_message_runs_live
    ON feishu_candidate_message_runs (tenant_key,app_id,expires_at) WHERE transfer OR outbound;

CREATE TABLE feishu_candidate_event_owners (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    source_id text NOT NULL,
    chat_id text NOT NULL,
    run_id text REFERENCES feishu_candidate_message_runs,
    PRIMARY KEY (tenant_key,app_id,source_id)
);

CREATE TABLE feishu_candidate_message_queue (
    run_id text NOT NULL REFERENCES feishu_candidate_message_runs,
    source_id text NOT NULL,
    event jsonb NOT NULL,
    command boolean NOT NULL,
    withdrawn boolean NOT NULL DEFAULT false,
    revision integer NOT NULL DEFAULT 1,
    ack_revision integer NOT NULL DEFAULT 0,
    lease_id text,
    lease_until timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (run_id,source_id)
);
CREATE INDEX feishu_candidate_message_queue_pending
    ON feishu_candidate_message_queue (run_id,created_at,source_id) WHERE revision > ack_revision;

CREATE TABLE feishu_candidate_message_receipts (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    source_id text NOT NULL,
    run_id text NOT NULL,
    revision integer NOT NULL,
    PRIMARY KEY (tenant_key,app_id,source_id)
);

CREATE OR REPLACE VIEW feishu_file_sources AS
SELECT s.tenant_key,s.app_id,s.message_id,s.file_id,s.name,s.declared_size,s.present,s.version_id,s.state,
       s.synced_at,s.attempted_at,s.next_at,f.kind,f.token,m.chat_id,m.sender_id,m.sender_name,m.sent_at,c.name AS chat_name,c.kind AS chat_kind,
       b.credentials_generation
FROM feishu_file_shares s JOIN feishu_files f ON f.id=s.file_id AND f.tenant_key=s.tenant_key
JOIN feishu_chat_messages m ON (m.tenant_key,m.app_id,m.message_id)=(s.tenant_key,s.app_id,s.message_id)
JOIN feishu_chat_conversations c ON (c.tenant_key,c.app_id,c.chat_id)=(m.tenant_key,m.app_id,m.chat_id)
JOIN feishu_bot_configs b ON (b.tenant_key,b.app_id)=(s.tenant_key,s.app_id)
JOIN feishu_chat_sync root ON (root.tenant_key,root.app_id,root.chat_id,root.thread_id)
    =(m.tenant_key,m.app_id,m.chat_id,'')
WHERE s.present AND b.enabled AND NOT root.source_blocked AND m.files_indexed AND NOT m.deleted
AND (m.visible_in_chat OR m.thread_id='' OR NOT EXISTS (
    SELECT FROM feishu_chat_sync t WHERE (t.tenant_key,t.app_id,t.chat_id,t.thread_id)=
    (m.tenant_key,m.app_id,m.chat_id,m.thread_id) AND t.source_blocked))
AND NOT EXISTS (SELECT FROM feishu_candidate_event_owners o
    WHERE (o.tenant_key,o.app_id,o.source_id)=(m.tenant_key,m.app_id,m.message_id) AND o.run_id IS NOT NULL);
