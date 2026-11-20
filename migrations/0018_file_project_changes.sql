CREATE TABLE feishu_file_project_revisions (
    tenant_key text NOT NULL, app_id text NOT NULL, file_id bigint NOT NULL REFERENCES feishu_files,
    version bigint NOT NULL DEFAULT 0, PRIMARY KEY(tenant_key,app_id,file_id)
);
INSERT INTO feishu_file_project_revisions(tenant_key,app_id,file_id)
SELECT DISTINCT tenant_key,app_id,file_id FROM feishu_file_projects;

CREATE TABLE feishu_file_project_audit (
    tenant_key text NOT NULL, app_id text NOT NULL, file_id bigint NOT NULL,
    version bigint NOT NULL, project_id text NOT NULL, operation text NOT NULL,
    actor_id text, changed_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(tenant_key,app_id,file_id,version)
);
CREATE FUNCTION record_file_project_change() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    link feishu_file_projects;
    revision bigint;
BEGIN
    IF TG_OP = 'DELETE' THEN link := OLD; ELSE link := NEW; END IF;
    INSERT INTO feishu_file_project_revisions AS r VALUES(link.tenant_key,link.app_id,link.file_id,1)
    ON CONFLICT(tenant_key,app_id,file_id) DO UPDATE SET version=r.version+1 RETURNING version INTO revision;
    INSERT INTO feishu_file_project_audit(tenant_key,app_id,file_id,version,project_id,operation,actor_id)
    VALUES(link.tenant_key,link.app_id,link.file_id,revision,link.project_id,TG_OP,
           nullif(current_setting('omnirelay.file_project_actor',true),''));
    RETURN NULL;
END $$;
CREATE TRIGGER record_file_project_change AFTER INSERT OR DELETE ON feishu_file_projects
FOR EACH ROW EXECUTE FUNCTION record_file_project_change();
