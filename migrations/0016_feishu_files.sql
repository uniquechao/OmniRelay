ALTER TABLE feishu_chat_messages ADD COLUMN files_indexed boolean NOT NULL DEFAULT false;
CREATE INDEX feishu_file_index_queue ON feishu_chat_messages(tenant_key,app_id,sent_at)
    WHERE NOT files_indexed AND NOT deleted;
CREATE FUNCTION invalidate_file_index() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (NEW.body, NEW.message_type) IS DISTINCT FROM (OLD.body, OLD.message_type) THEN
        NEW.files_indexed := false;
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER invalidate_file_index BEFORE UPDATE OF body,message_type ON feishu_chat_messages
    FOR EACH ROW EXECUTE FUNCTION invalidate_file_index();

CREATE TABLE feishu_files (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_key text NOT NULL, kind text NOT NULL, token text NOT NULL,
    UNIQUE(tenant_key,kind,token)
);
CREATE TABLE feishu_file_chunks (
    file_id bigint NOT NULL REFERENCES feishu_files, digest bytea NOT NULL, content bytea NOT NULL,
    PRIMARY KEY(file_id,digest)
);
CREATE TABLE feishu_file_versions (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    file_id bigint NOT NULL REFERENCES feishu_files,
    digest bytea NOT NULL, chunks bytea[] NOT NULL, byte_size bigint NOT NULL CHECK(byte_size>=0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE(file_id,digest), UNIQUE(file_id,id)
);
CREATE TABLE feishu_file_shares (
    tenant_key text NOT NULL, app_id text NOT NULL, message_id text NOT NULL,
    file_id bigint NOT NULL REFERENCES feishu_files, name text NOT NULL,
    declared_size bigint CHECK(declared_size>=0), present boolean NOT NULL DEFAULT true, version_id bigint, state text NOT NULL DEFAULT 'pending'
        CHECK(state IN ('pending','complete','partial','failed','revoked','expired')),
    synced_at timestamptz, attempted_at timestamptz, next_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(tenant_key,app_id,message_id,file_id),
    FOREIGN KEY(tenant_key,app_id,message_id) REFERENCES feishu_chat_messages,
    FOREIGN KEY(file_id,version_id) REFERENCES feishu_file_versions(file_id,id)
);
CREATE INDEX feishu_file_refresh ON feishu_file_shares(tenant_key,app_id,next_at);
CREATE TABLE feishu_file_projects (
    tenant_key text NOT NULL, app_id text NOT NULL, file_id bigint NOT NULL REFERENCES feishu_files,
    project_id text NOT NULL, PRIMARY KEY(tenant_key,app_id,file_id,project_id)
);

CREATE VIEW feishu_file_sources AS
SELECT s.*,f.kind,f.token,m.chat_id,m.sender_id,m.sender_name,m.sent_at,c.name AS chat_name,c.kind AS chat_kind,
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
    (m.tenant_key,m.app_id,m.chat_id,m.thread_id) AND t.source_blocked));

-- 首次为群绑定项目时，为已采集但尚无归属的文件建立初始归属。
CREATE FUNCTION assign_unowned_files() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM FROM feishu_chat_conversations WHERE (tenant_key,app_id,chat_id)=(NEW.tenant_key,NEW.app_id,NEW.chat_id) FOR UPDATE;
    INSERT INTO feishu_file_projects
    SELECT DISTINCT s.tenant_key,s.app_id,s.file_id,NEW.project_id
    FROM feishu_file_shares s JOIN feishu_chat_messages m USING(tenant_key,app_id,message_id)
    WHERE (m.tenant_key,m.app_id,m.chat_id)=(NEW.tenant_key,NEW.app_id,NEW.chat_id)
      AND s.present AND NOT m.deleted AND m.files_indexed AND NOT EXISTS (
        SELECT FROM feishu_file_projects p WHERE (p.tenant_key,p.app_id,p.file_id)=(s.tenant_key,s.app_id,s.file_id))
    ON CONFLICT DO NOTHING;
    RETURN NEW;
END $$;
CREATE TRIGGER assign_unowned_files AFTER INSERT ON project_chat_links
    FOR EACH ROW EXECUTE FUNCTION assign_unowned_files();
