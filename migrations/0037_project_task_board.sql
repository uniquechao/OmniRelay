ALTER TABLE project_task_boards
  ADD COLUMN columns jsonb NOT NULL DEFAULT '[{"id":"todo","name":"待办","color":"#64748b","stage":"todo"},{"id":"in_progress","name":"进行中","color":"#3b82f6","stage":"in_progress"},{"id":"blocked","name":"受阻","color":"#ef4444","stage":"blocked"},{"id":"done","name":"已完成","color":"#22c55e","stage":"done"}]',
  ADD COLUMN default_column_id text NOT NULL DEFAULT 'todo',
  ADD COLUMN last_number bigint NOT NULL DEFAULT 0;
DO $$ DECLARE entry record;
BEGIN
  FOR entry IN SELECT conname FROM pg_constraint WHERE conrelid='project_tasks'::regclass AND contype='c' AND pg_get_constraintdef(oid) LIKE '%column_id%' LOOP
    EXECUTE format('ALTER TABLE project_tasks DROP CONSTRAINT %I',entry.conname);
  END LOOP;
END $$;
ALTER TABLE project_tasks ADD CHECK(status IN ('todo','in_progress','blocked','done','cancelled')), ADD COLUMN display_number bigint;
WITH numbered AS (SELECT tenant_key,app_id,project_id,id,row_number() OVER(PARTITION BY tenant_key,app_id,project_id ORDER BY created_at NULLS FIRST,id) n FROM project_tasks)
UPDATE project_tasks t SET display_number=n.n FROM numbered n WHERE (t.tenant_key,t.app_id,t.project_id,t.id)=(n.tenant_key,n.app_id,n.project_id,n.id);
UPDATE project_task_boards b SET last_number=(SELECT coalesce(max(t.display_number),0) FROM project_tasks t WHERE (t.tenant_key,t.app_id,t.project_id)=(b.tenant_key,b.app_id,b.project_id));
ALTER TABLE project_tasks ALTER COLUMN display_number SET NOT NULL, ADD UNIQUE(tenant_key,app_id,project_id,display_number);
CREATE FUNCTION project_assign_task_number() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE project_task_boards SET last_number=last_number+1 WHERE (tenant_key,app_id,project_id)=(NEW.tenant_key,NEW.app_id,NEW.project_id) RETURNING last_number INTO NEW.display_number;
  RETURN NEW;
END $$;
CREATE TRIGGER project_task_number BEFORE INSERT ON project_tasks FOR EACH ROW EXECUTE FUNCTION project_assign_task_number();
ALTER TABLE project_task_tags ADD COLUMN type text NOT NULL DEFAULT '项目', ADD COLUMN color text NOT NULL DEFAULT '#555b6b';
CREATE TABLE project_task_candidates(
 tenant_key text NOT NULL,app_id text NOT NULL,project_id text NOT NULL,id text NOT NULL DEFAULT gen_random_uuid()::text,
 fields jsonb NOT NULL, sources jsonb NOT NULL, model_name text NOT NULL,prompt_version text NOT NULL,
 status text NOT NULL DEFAULT 'pending_review',version bigint NOT NULL DEFAULT 1,task_id text,
 submitted_by text NOT NULL,fingerprint text NOT NULL, UNIQUE(tenant_key,app_id,project_id,submitted_by,fingerprint),
 created_at timestamptz NOT NULL DEFAULT now(),reviewed_by text,review_reason text,review_command jsonb,
 PRIMARY KEY(tenant_key,app_id,project_id,id), FOREIGN KEY(tenant_key,app_id,project_id) REFERENCES project_task_boards,
 FOREIGN KEY(tenant_key,app_id,project_id,task_id) REFERENCES project_tasks
);
