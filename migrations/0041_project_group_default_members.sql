-- Initialize each group binding from its first available verified roster.
ALTER TABLE project_chat_links ADD COLUMN members_initialized_at timestamptz;

CREATE FUNCTION initialize_project_group_members(tenant text, app text, chat text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    project text;
    roster text[];
    names jsonb;
BEGIN
    SELECT l.project_id, g.members, g.member_names INTO project, roster, names
    FROM project_chat_links l
    JOIN feishu_chat_conversations c USING (tenant_key,app_id,chat_id)
    JOIN feishu_drive_group_memberships g USING (tenant_key,app_id,chat_id)
    JOIN feishu_bot_configs b USING (tenant_key,app_id)
    WHERE (l.tenant_key,l.app_id,l.chat_id)=(tenant,app,chat)
      AND l.members_initialized_at IS NULL AND c.kind='group' AND b.enabled
      AND g.generation=b.credentials_generation AND g.verified_at>now()-interval '26 hours'
      AND cardinality(g.members)>0
    FOR UPDATE OF l;
    IF NOT FOUND THEN RETURN; END IF;

    -- Use the same open ID as login. Existing and disabled accounts retain their identity and state.
    SELECT ARRAY(
        SELECT DISTINCT id FROM unnest(roster) member(id)
        LEFT JOIN directory_roster dr ON (dr.tenant_key,dr.app_id,dr.open_id)=(tenant,app,id)
        LEFT JOIN directory_sources ds ON (ds.tenant_key,ds.app_id)=(tenant,app) AND ds.visible
        LEFT JOIN directory_people dp ON (dp.tenant_key,dp.app_id,dp.snapshot_version,dp.open_id)=(tenant,app,ds.active_version,id)
        WHERE id<>'' AND dr.resigned_at IS NULL AND coalesce(dp.directory_status,'active')<>'resigned'
        ORDER BY id
    ) INTO roster;
    INSERT INTO accounts(tenant_key,app_id,open_id,name)
        SELECT tenant,app,id,coalesce(nullif(names->>id,''),id) FROM unnest(roster) member(id)
        ORDER BY id ON CONFLICT (tenant_key,app_id,open_id) DO NOTHING;
    INSERT INTO project_members(tenant_key,app_id,project_id,account_id,member_id,role)
        SELECT tenant,app,project,a.id,'feishu:'||a.open_id,'member' FROM accounts a
        WHERE (a.tenant_key,a.app_id)=(tenant,app) AND a.active AND a.open_id=ANY(roster)
        ORDER BY a.id ON CONFLICT (tenant_key,app_id,project_id,account_id) DO NOTHING;
    UPDATE project_chat_links SET members_initialized_at=clock_timestamp()
        WHERE (tenant_key,app_id,chat_id)=(tenant,app,chat);
END;
$$;

CREATE FUNCTION initialize_members_on_group_binding() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='UPDATE' AND OLD.project_id IS DISTINCT FROM NEW.project_id THEN
        UPDATE project_chat_links SET members_initialized_at=NULL
        WHERE (tenant_key,app_id,chat_id)=(NEW.tenant_key,NEW.app_id,NEW.chat_id);
    END IF;
    PERFORM initialize_project_group_members(NEW.tenant_key,NEW.app_id,NEW.chat_id);
    RETURN NULL;
END;
$$;
CREATE TRIGGER project_group_bound_members AFTER INSERT OR UPDATE OF project_id ON project_chat_links
    FOR EACH ROW EXECUTE FUNCTION initialize_members_on_group_binding();

CREATE FUNCTION initialize_members_on_group_roster() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM initialize_project_group_members(NEW.tenant_key,NEW.app_id,NEW.chat_id);
    RETURN NULL;
END;
$$;
CREATE TRIGGER project_group_roster_members AFTER INSERT OR UPDATE OF verified_at, members ON feishu_drive_group_memberships
    FOR EACH ROW WHEN (NEW.verified_at IS NOT NULL) EXECUTE FUNCTION initialize_members_on_group_roster();

SELECT initialize_project_group_members(tenant_key,app_id,chat_id) FROM project_chat_links
ORDER BY tenant_key,app_id,chat_id;
