\getenv candidate_id CANDIDATE_ID
\getenv migration_password MIGRATION_PASSWORD
\getenv runtime_password RUNTIME_PASSWORD
CREATE ROLE candidate_migration LOGIN PASSWORD :'migration_password';
CREATE ROLE candidate_runtime LOGIN PASSWORD :'runtime_password';
REVOKE ALL ON DATABASE candidate FROM PUBLIC;
GRANT CONNECT ON DATABASE candidate TO candidate_migration, candidate_runtime;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
ALTER SCHEMA public OWNER TO candidate_migration;
GRANT USAGE ON SCHEMA public TO candidate_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE candidate_migration IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO candidate_runtime;
ALTER DEFAULT PRIVILEGES FOR ROLE candidate_migration IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO candidate_runtime;
CREATE SCHEMA candidate_guard;
CREATE TABLE candidate_guard.identity(id text PRIMARY KEY, db_name name, migration_role name, runtime_role name);
INSERT INTO candidate_guard.identity VALUES (:'candidate_id',current_database(),'candidate_migration','candidate_runtime');
GRANT USAGE ON SCHEMA candidate_guard TO candidate_migration,candidate_runtime;
GRANT SELECT ON candidate_guard.identity TO candidate_migration,candidate_runtime;
