CREATE TABLE directory_roster (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    open_id text NOT NULL,
    last_scope_hash text NOT NULL,
    resigned_at timestamptz,
    resigned_run_id bigint,
    PRIMARY KEY (tenant_key, app_id, open_id),
    FOREIGN KEY (tenant_key, app_id) REFERENCES directory_sources (tenant_key, app_id),
    FOREIGN KEY (tenant_key, app_id, resigned_run_id) REFERENCES directory_sync_runs (tenant_key, app_id, id),
    CHECK ((resigned_at IS NULL) = (resigned_run_id IS NULL))
);

INSERT INTO directory_roster (tenant_key, app_id, open_id, last_scope_hash)
SELECT p.tenant_key, p.app_id, p.open_id, s.active_scope_hash
FROM directory_sources s JOIN directory_people p
ON (p.tenant_key, p.app_id, p.snapshot_version) = (s.tenant_key, s.app_id, s.active_version)
WHERE s.active_scope_hash IS NOT NULL AND p.directory_status <> 'resigned';

ALTER TABLE audit_events
    ALTER COLUMN actor_account_id DROP NOT NULL,
    ADD COLUMN directory_sync_run_id bigint,
    ADD FOREIGN KEY (tenant_key, app_id, directory_sync_run_id) REFERENCES directory_sync_runs (tenant_key, app_id, id),
    ADD CHECK ((actor_account_id IS NULL) <> (directory_sync_run_id IS NULL));
