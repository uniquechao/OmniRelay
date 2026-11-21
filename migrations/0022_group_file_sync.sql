-- 留存旧关联供核对。只有最后一次插入由已知操作者完成的关系可直接确认为人工关联。
DROP TRIGGER assign_unowned_files ON project_chat_links;
DROP FUNCTION assign_unowned_files();
ALTER TABLE feishu_file_projects RENAME TO feishu_file_project_legacy;
DROP TRIGGER record_file_project_change ON feishu_file_project_legacy;
CREATE TABLE feishu_file_manual_projects (LIKE feishu_file_project_legacy INCLUDING ALL);
ALTER TABLE feishu_file_manual_projects ADD FOREIGN KEY (file_id) REFERENCES feishu_files;
INSERT INTO feishu_file_manual_projects
SELECT l.* FROM feishu_file_project_legacy l
JOIN LATERAL (
    SELECT operation,actor_id FROM feishu_file_project_audit a
    WHERE (a.tenant_key,a.app_id,a.file_id,a.project_id)=(l.tenant_key,l.app_id,l.file_id,l.project_id)
    ORDER BY version DESC LIMIT 1
) a ON a.operation='INSERT' AND a.actor_id IS NOT NULL;
CREATE OR REPLACE FUNCTION record_file_project_change() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE link feishu_file_manual_projects; revision bigint;
BEGIN
    IF TG_OP='DELETE' THEN link := OLD; ELSE link := NEW; END IF;
    INSERT INTO feishu_file_project_revisions AS r VALUES(link.tenant_key,link.app_id,link.file_id,1)
    ON CONFLICT(tenant_key,app_id,file_id) DO UPDATE SET version=r.version+1 RETURNING version INTO revision;
    INSERT INTO feishu_file_project_audit(tenant_key,app_id,file_id,version,project_id,operation,actor_id)
    VALUES(link.tenant_key,link.app_id,link.file_id,revision,link.project_id,TG_OP,
           nullif(current_setting('omnirelay.file_project_actor',true),''));
    RETURN NULL;
END $$;
CREATE TRIGGER record_file_project_change AFTER INSERT OR DELETE ON feishu_file_manual_projects
FOR EACH ROW EXECUTE FUNCTION record_file_project_change();

INSERT INTO feishu_file_project_revisions(tenant_key,app_id,file_id)
SELECT DISTINCT tenant_key,app_id,file_id FROM feishu_file_shares ON CONFLICT DO NOTHING;

ALTER TABLE feishu_file_versions ADD COLUMN object_key text, ADD COLUMN content_type text;
CREATE TABLE feishu_file_share_versions (
    tenant_key text NOT NULL, app_id text NOT NULL, message_id text NOT NULL,
    file_id bigint NOT NULL, version_id bigint NOT NULL,
    PRIMARY KEY(tenant_key,app_id,message_id,file_id,version_id),
    FOREIGN KEY(tenant_key,app_id,message_id,file_id) REFERENCES feishu_file_shares ON DELETE CASCADE,
    FOREIGN KEY(file_id,version_id) REFERENCES feishu_file_versions(file_id,id)
);
INSERT INTO feishu_file_share_versions
SELECT tenant_key,app_id,message_id,file_id,version_id FROM feishu_file_shares WHERE version_id IS NOT NULL;

ALTER TABLE feishu_files DROP CONSTRAINT feishu_files_tenant_key_kind_token_key;
ALTER TABLE feishu_files ADD COLUMN app_scope text NOT NULL DEFAULT '';
UPDATE feishu_files f SET app_scope=g.app_id FROM (
    SELECT file_id,min(app_id) AS app_id FROM feishu_file_shares GROUP BY file_id HAVING count(DISTINCT app_id)=1
) g WHERE f.id=g.file_id AND f.kind='attachment';
ALTER TABLE feishu_files ADD UNIQUE(tenant_key,app_scope,kind,token);

-- 旧索引未识别图片、视频、富文本内嵌资源和知识库链接。
UPDATE feishu_chat_messages SET files_indexed=false
WHERE NOT deleted AND (message_type IN ('post','media','image','interactive') OR body LIKE '%/wiki/%');

CREATE TABLE feishu_wiki_targets (
    tenant_key text NOT NULL, app_id text NOT NULL, token text NOT NULL,
    kind text, object_token text, next_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(tenant_key,app_id,token)
);
CREATE TABLE feishu_message_wiki_links (
    tenant_key text NOT NULL, app_id text NOT NULL, message_id text NOT NULL, token text NOT NULL,
    PRIMARY KEY(tenant_key,app_id,message_id,token),
    FOREIGN KEY(tenant_key,app_id,message_id) REFERENCES feishu_chat_messages ON DELETE CASCADE,
    FOREIGN KEY(tenant_key,app_id,token) REFERENCES feishu_wiki_targets
);

ALTER TABLE feishu_file_shares DROP CONSTRAINT feishu_file_shares_state_check;
ALTER TABLE feishu_file_shares ADD CHECK(state IN ('pending','complete','partial','failed','revoked','expired','over_limit'));

ALTER TABLE feishu_file_shares ADD COLUMN failures integer NOT NULL DEFAULT 0;

-- 上传前登记，事务失败或撤权留下的对象仍可在宽限期内重试和回收。
CREATE TABLE feishu_sync_objects (
    tenant_key text NOT NULL, object_key text NOT NULL, published boolean NOT NULL DEFAULT false,
    reserved_until timestamptz NOT NULL DEFAULT now()+interval '24 hours',
    PRIMARY KEY(tenant_key,object_key)
);
CREATE INDEX feishu_sync_object_cleanup ON feishu_sync_objects(tenant_key,reserved_until) WHERE NOT published;
