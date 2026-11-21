-- 仅在已迁移的空验收库内构造两个项目，角色、任务和日报经工作台接口写入。
BEGIN;
INSERT INTO accounts(id, tenant_key, app_id, open_id, name, active) VALUES
    ('manager', 'tenant', 'cli_fixture', 'manager', '项目负责人', true),
    ('second', 'tenant', 'cli_fixture', 'second', '项目成员', true);
INSERT INTO project_profiles(tenant_key, app_id, project_id, name, status, version) VALUES
    ('tenant', 'cli_fixture', 'h3', '隔离项目甲', 'active', 1),
    ('tenant', 'cli_fixture', 'joint-b', '隔离项目乙', 'planned', 1);
COMMIT;
