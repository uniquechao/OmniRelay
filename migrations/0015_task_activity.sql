ALTER TABLE project_tasks
    ADD COLUMN participant_ids text[] NOT NULL DEFAULT '{}',
    ADD COLUMN acceptance text NOT NULL DEFAULT '',
    ADD COLUMN start_at text,
    ADD COLUMN relation_names jsonb NOT NULL DEFAULT '{}';

CREATE TABLE project_task_activity (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL, task_id text NOT NULL,
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    kind text NOT NULL CHECK(kind IN ('comment','change')), action text NOT NULL,
    actor_id text NOT NULL REFERENCES accounts(id), actor_name text NOT NULL,
    body text NOT NULL DEFAULT '', before_values jsonb NOT NULL DEFAULT '{}', after_values jsonb NOT NULL DEFAULT '{}',
    FOREIGN KEY (tenant_key,app_id,project_id,task_id) REFERENCES project_tasks
);
CREATE INDEX project_task_activity_page ON project_task_activity
    (tenant_key,app_id,project_id,task_id,created_at DESC,id DESC);

CREATE TABLE project_task_suggestions (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL, task_id text NOT NULL,
    id text PRIMARY KEY DEFAULT gen_random_uuid()::text, task_version bigint NOT NULL,
    actor_id text NOT NULL REFERENCES accounts(id), proposed_status text NOT NULL,
    note text NOT NULL, status text NOT NULL DEFAULT 'pending', version bigint NOT NULL DEFAULT 1,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(), reviewed_by text REFERENCES accounts(id),
    reason text, reviewed_at timestamptz,
    FOREIGN KEY (tenant_key,app_id,project_id,task_id) REFERENCES project_tasks
);

-- 旧任务缺少可信的创建信息，保留空值；首次正式修改后开始记录更新者。
ALTER TABLE project_tasks
    ADD COLUMN created_by text REFERENCES accounts(id),
    ADD COLUMN created_by_name text,
    ADD COLUMN created_at timestamptz,
    ADD COLUMN updated_by text REFERENCES accounts(id),
    ADD COLUMN updated_by_name text,
    ADD COLUMN updated_at timestamptz;

CREATE FUNCTION stamp_project_task_actor() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_by := nullif(current_setting('omnirelay.task_actor',true),'');
    IF NEW.updated_by IS NULL THEN
        RAISE EXCEPTION '正式任务命令缺少操作者';
    END IF;
    SELECT coalesce(nullif(name,''),id) INTO STRICT NEW.updated_by_name
        FROM accounts WHERE id=NEW.updated_by AND tenant_key=NEW.tenant_key AND app_id=NEW.app_id;
    NEW.updated_at := clock_timestamp();
    IF TG_OP='INSERT' THEN
        NEW.created_by := NEW.updated_by;
        NEW.created_by_name := NEW.updated_by_name;
        NEW.created_at := NEW.updated_at;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER project_task_actor BEFORE INSERT OR UPDATE OF version ON project_tasks
    FOR EACH ROW EXECUTE FUNCTION stamp_project_task_actor();

CREATE TABLE project_members (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL,
    account_id text NOT NULL, member_id text NOT NULL,
    role text NOT NULL CHECK(role IN ('manager','editor','member','reader','revoked')),
    generation text NOT NULL DEFAULT gen_random_uuid()::text,
    PRIMARY KEY(tenant_key,app_id,project_id,account_id),
    UNIQUE(tenant_key,app_id,project_id,member_id),
    FOREIGN KEY(tenant_key,app_id,account_id) REFERENCES accounts(tenant_key,app_id,id)
);
CREATE INDEX project_members_account ON project_members(tenant_key,app_id,account_id);
CREATE TABLE project_task_tags (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL,
    id text NOT NULL, name text NOT NULL,
    PRIMARY KEY(tenant_key,app_id,project_id,id)
);
INSERT INTO project_task_tags
SELECT DISTINCT tenant_key,app_id,project_id,tag,tag FROM project_tasks CROSS JOIN LATERAL unnest(tag_ids) tag;

CREATE TABLE project_sources (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL,
    kind text NOT NULL CHECK(kind IN ('document','minutes','message')), id text NOT NULL,
    title text NOT NULL, body text NOT NULL, chat_id text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(tenant_key,app_id,project_id,kind,id)
);
CREATE TABLE project_source_readers (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL,
    kind text NOT NULL, source_id text NOT NULL, account_id text NOT NULL,
    PRIMARY KEY(tenant_key,app_id,project_id,kind,source_id,account_id),
    FOREIGN KEY(tenant_key,app_id,project_id,kind,source_id) REFERENCES project_sources ON DELETE CASCADE,
    FOREIGN KEY(tenant_key,app_id,project_id,account_id) REFERENCES project_members ON DELETE CASCADE
);
CREATE TABLE project_task_sources (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL,
    task_id text NOT NULL, kind text NOT NULL, source_id text NOT NULL,
    PRIMARY KEY(tenant_key,app_id,project_id,task_id,kind,source_id),
    FOREIGN KEY(tenant_key,app_id,project_id,kind,source_id) REFERENCES project_sources ON DELETE CASCADE,
    FOREIGN KEY(tenant_key,app_id,project_id,task_id) REFERENCES project_tasks
);
