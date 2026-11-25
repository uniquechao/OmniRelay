-- NULL denotes a local account without a Feishu identity. Existing accounts keep their open_id.
ALTER TABLE accounts ALTER COLUMN open_id DROP NOT NULL;
CREATE UNIQUE INDEX accounts_local_admin_scope ON accounts (tenant_key, app_id) WHERE open_id IS NULL;
