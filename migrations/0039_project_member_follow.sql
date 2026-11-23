ALTER TABLE project_members ADD COLUMN followed boolean NOT NULL DEFAULT false;

CREATE INDEX project_member_follows ON project_members (tenant_key, app_id, account_id, project_id)
    WHERE followed AND role <> 'revoked';
