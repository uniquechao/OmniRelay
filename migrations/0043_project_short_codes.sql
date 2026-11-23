CREATE TABLE project_short_codes (
    tenant_key text NOT NULL, app_id text NOT NULL, project_id text NOT NULL,
    short_code text COLLATE "C" NOT NULL UNIQUE CHECK (short_code ~ '^[A-Z]{3}$'),
    PRIMARY KEY (tenant_key,app_id,project_id)
);

-- GB 2312 level-one Hanzi are ordered by pinyin. Outside this range, use
-- other letters in the name, or the project-specific fallback below.
CREATE FUNCTION project_name_initials(label text) RETURNS text
LANGUAGE plpgsql IMMUTABLE STRICT AS $$
DECLARE
    initials text := '';
    ch text;
    encoded bytea;
    position_no integer;
    starts integer[] := ARRAY[1601,1637,1833,2078,2274,2302,2433,2594,2787,3106,3212,3472,3635,3722,3730,3858,4027,4086,4390,4558,4684,4925,5249];
BEGIN
    FOREACH ch IN ARRAY string_to_array(label,NULL) LOOP
        IF ch COLLATE "C" ~ '^[a-zA-Z]$' THEN
            initials := initials || upper(ch COLLATE "C");
        ELSIF ascii(ch) BETWEEN 19968 AND 40959 THEN
            encoded := convert_to(ch,'GB18030');
            IF length(encoded)=2 THEN
                position_no := (get_byte(encoded,0)-160)*100 + get_byte(encoded,1)-160;
                IF get_byte(encoded,1) BETWEEN 161 AND 254 AND position_no BETWEEN 1601 AND 5589 THEN
                    initials := initials || substr('ABCDEFGHJKLMNOPQRSTWXYZ',width_bucket(position_no,starts),1);
                END IF;
            END IF;
        END IF;
        EXIT WHEN length(initials)=3;
    END LOOP;
    RETURN initials;
END $$;

CREATE FUNCTION project_ensure_short_code(tenant text, app text, project text, label text) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    initials text;
    seed integer;
    n integer;
    candidate text;
BEGIN
    IF EXISTS (SELECT FROM project_short_codes WHERE (tenant_key,app_id,project_id)=(tenant,app,project)) THEN
        RETURN;
    END IF;
    initials := project_name_initials(coalesce(nullif(label,''),project));
    IF initials <> '' THEN
        initials := rpad(initials,3,'X');
        seed := (ascii(substr(initials,1,1))-65)*676 + (ascii(substr(initials,2,1))-65)*26 + ascii(substr(initials,3,1))-65;
    ELSE
        seed := ('x'||substr(md5(project),1,7))::bit(28)::int % 17576;
    END IF;
    FOR offset_no IN 0..17575 LOOP
        n := (seed+offset_no) % 17576;
        candidate := chr(65+n/676) || chr(65+(n/26)%26) || chr(65+n%26);
        INSERT INTO project_short_codes VALUES (tenant,app,project,candidate) ON CONFLICT DO NOTHING;
        IF FOUND OR EXISTS (SELECT FROM project_short_codes WHERE (tenant_key,app_id,project_id)=(tenant,app,project)) THEN
            RETURN;
        END IF;
    END LOOP;
    RAISE EXCEPTION 'three-letter project code space exhausted';
END $$;

CREATE FUNCTION register_project_short_code() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM project_ensure_short_code(NEW.tenant_key,NEW.app_id,NEW.project_id,to_jsonb(NEW)->>'name');
    RETURN NEW;
END $$;

DO $$
DECLARE source_table text;
BEGIN
    FOREACH source_table IN ARRAY ARRAY['project_profiles','project_members','project_task_boards','project_relay_settings','project_chat_links'] LOOP
        EXECUTE format('CREATE TRIGGER register_short_code AFTER INSERT OR UPDATE OF tenant_key,app_id,project_id ON %I FOR EACH ROW EXECUTE FUNCTION register_project_short_code()',source_table);
    END LOOP;
END $$;

SELECT project_ensure_short_code(tenant_key,app_id,project_id,name)
FROM (
    SELECT tenant_key,app_id,project_id FROM project_profiles
    UNION SELECT tenant_key,app_id,project_id FROM project_members
    UNION SELECT tenant_key,app_id,project_id FROM project_task_boards
    UNION SELECT tenant_key,app_id,project_id FROM project_relay_settings
    UNION SELECT tenant_key,app_id,project_id FROM project_chat_links
) ids LEFT JOIN project_profiles USING (tenant_key,app_id,project_id)
ORDER BY tenant_key,app_id,project_id;

CREATE FUNCTION project_task_code(short_code text, display_number bigint) RETURNS text
LANGUAGE sql IMMUTABLE STRICT AS $$
    SELECT short_code || '-T' || lpad(display_number::text,greatest(3,length(display_number::text)),'0')
$$;
