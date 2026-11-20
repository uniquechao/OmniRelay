CREATE INDEX audit_events_scope_time_idx ON audit_events (tenant_key, app_id, created_at, id);

CREATE COLLATION audit_text_order (provider = icu, locale = 'zh-CN-u-kn-true');

CREATE VIEW audit_query_events AS
SELECT e.*,
    CASE split_part(action, '.', 1)
        WHEN 'identity' THEN 'login' WHEN 'account' THEN 'account'
        WHEN 'role' THEN 'role' WHEN 'directory' THEN 'directory' ELSE 'config'
    END AS kind,
    CASE WHEN directory_sync_run_id IS NOT NULL THEN '系统（飞书通讯录同步）'
        ELSE coalesce(actor_account_id, '') END AS actor_label,
    CASE action
        WHEN 'identity.login' THEN '登录' WHEN 'identity.logout' THEN '退出'
        WHEN 'identity.password_change' THEN '修改密码' WHEN 'account.preauthorize' THEN '开通飞书账号'
        WHEN 'account.enable' THEN '启用账号' WHEN 'account.disable' THEN '停用账号'
        WHEN 'role.bootstrap' THEN '初始化管理员' WHEN 'role.grant' THEN '授予角色'
        WHEN 'role.revoke' THEN '撤销角色' WHEN 'directory.sync' THEN '同步组织和用户'
        WHEN 'directory.resign' THEN '确认人员离职' WHEN 'account.disable_protected' THEN '保留最后一位管理员账号'
        ELSE action END AS action_label
FROM audit_events e;
