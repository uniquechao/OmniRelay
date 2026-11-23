package project

// Only public selection fields are projected; password material and system roles are never returned.
const userChoiceJoins = ` LEFT JOIN account_passwords pw ON (pw.tenant_key,pw.app_id,pw.account_id)=(a.tenant_key,a.app_id,a.id)
 LEFT JOIN directory_sources ds ON (ds.tenant_key,ds.app_id)=(a.tenant_key,a.app_id) AND ds.visible
 LEFT JOIN directory_people dp ON (dp.tenant_key,dp.app_id,dp.snapshot_version,dp.open_id)=(a.tenant_key,a.app_id,ds.active_version,a.open_id)`
const userChoiceJSON = `'account_id',a.id,'display_name',coalesce(nullif(dp.name,''),nullif(a.name,''),a.id),'avatar_url',coalesce(nullif(dp.avatar_url,''),a.avatar_url),'username',coalesce(pw.username,'')`
const userChoiceSearch = `lower(concat_ws(' ',dp.name,a.name,a.id,pw.username))`
