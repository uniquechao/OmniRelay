ALTER TABLE project_profiles ADD COLUMN updated_at timestamptz;

UPDATE project_profiles p SET updated_at=events.updated_at
FROM (
    SELECT tenant_key, app_id, resource_id, change_version, max(created_at) AS updated_at
    FROM audit_events
    WHERE resource_type='project' AND result='success'
      AND action IN ('project.create', 'project.profile.update', 'project.archive')
    GROUP BY tenant_key, app_id, resource_id, change_version
) events
WHERE (p.tenant_key,p.app_id,p.project_id,p.version)=
      (events.tenant_key,events.app_id,events.resource_id,events.change_version);

ALTER TABLE project_profiles ALTER COLUMN updated_at SET DEFAULT clock_timestamp();

CREATE FUNCTION stamp_project_profile_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := clock_timestamp();
    RETURN NEW;
END;
$$;

CREATE TRIGGER project_profile_update_timestamp
BEFORE UPDATE OF version ON project_profiles
FOR EACH ROW WHEN (OLD.version IS DISTINCT FROM NEW.version)
EXECUTE FUNCTION stamp_project_profile_update();
