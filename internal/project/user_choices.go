package project

// Only public selection fields are projected; password material and system roles are never returned.
const userChoiceJoins = ` LEFT JOIN account_passwords pw ON (pw.tenant_key,pw.app_id,pw.account_id)=(a.tenant_key,a.app_id,a.id)
 LEFT JOIN directory_sources ds ON (ds.tenant_key,ds.app_id)=(a.tenant_key,a.app_id) AND ds.visible
 LEFT JOIN directory_people dp ON (dp.tenant_key,dp.app_id,dp.snapshot_version,dp.open_id)=(a.tenant_key,a.app_id,ds.active_version,a.open_id)`
const userChoiceJSON = `'account_id',a.id,'display_name',coalesce(nullif(dp.name,''),nullif(a.name,''),a.id),'avatar_url',coalesce(nullif(dp.avatar_url,''),a.avatar_url),'username',coalesce(pw.username,'')`
// Match the system user directory, including people in the published Feishu
// directory who have not signed in yet. Project bindings do not scope this list.
const systemPeople = `WITH directory AS (
	SELECT p.* FROM directory_people p JOIN directory_sources s
	ON (s.tenant_key,s.app_id,s.active_version)=(p.tenant_key,p.app_id,p.snapshot_version)
	WHERE p.tenant_key=$1 AND p.app_id=$2 AND s.visible
), system_people AS (
	SELECT coalesce(a.open_id,p.open_id) AS open_id, a.id AS account,
		coalesce(nullif(p.name,''),nullif(a.name,''),a.id,p.open_id) AS name,
		coalesce(nullif(p.avatar_url,''),a.avatar_url,'') AS avatar, coalesce(pw.username,'') AS username,
		lower(concat_ws(' ',p.name,a.name,a.id,pw.username,coalesce(a.open_id,p.open_id))) AS searchable
	FROM (SELECT * FROM accounts WHERE tenant_key=$1 AND app_id=$2) a
	FULL JOIN directory p USING(open_id)
	LEFT JOIN account_passwords pw ON (pw.tenant_key,pw.app_id,pw.account_id)=($1,$2,a.id)
	LEFT JOIN directory_roster dr ON (dr.tenant_key,dr.app_id,dr.open_id)=($1,$2,coalesce(a.open_id,p.open_id))
	WHERE coalesce(a.active,true) AND dr.resigned_at IS NULL AND coalesce(p.directory_status,'active')<>'resigned'
)`
