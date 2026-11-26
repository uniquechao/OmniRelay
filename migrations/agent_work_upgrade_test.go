package migrations

import (
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Upgrade a populated V46 schema and reconcile its visible history, real
// platform receipt, internal delivery reference and committed tool result.
func TestAgentWorkbenchUpgradePreservesHistories(t *testing.T) {
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	run := func(sql string) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	run("CREATE SCHEMA migration_agent_upgrade; SET LOCAL search_path=migration_agent_upgrade")
	changes, err := readMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range changes[:46] {
		run(m.sql)
	}
	run(`INSERT INTO accounts(id,tenant_key,app_id,open_id) VALUES('owner','t','a','ou_owner');
	INSERT INTO agent_instances(agent_id,tenant_key,app_id,project_id,owner_id) VALUES('pm','t','a','p','rt');
	INSERT INTO agent_group_work(id,tenant_key,app_id,channel,account_id,chat_id,thread_id,message_id,config_version,project_id,membership_generation,assignment_version,permission_version,deadline,mode,agent_id,session_id,state,question,asked_at,response_text,delivery_id,platform_message_id) VALUES
	('one','t','a','workbench','owner','p','','q',0,'p','m',0,0,now(),'project_chat','pm','native-one','sent','question',now(),'answer','00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000001'),
	('two','t','a','feishu','owner','g','topic','msg',1,'p','m',1,0,now(),'group_reply','pm','native-two','sent',NULL,NULL,'delivered','00000000-0000-4000-8000-000000000002','om_platform');
	INSERT INTO agent_project_tool_calls(work_id,slot,tool,fingerprint,receipt) VALUES('one',1,'task_create','hash','{"id":"task-1"}');
	INSERT INTO agent_project_tool_reads(work_id,tool,fingerprint,args,digest) VALUES('one','file_get','fingerprint','{"file_ref":"note"}','reference_hash')`)
	for _, m := range changes[46:] {
		run(m.sql)
	}
	var ok bool
	err = tx.QueryRow(ctx, `SELECT (SELECT count(*)=2 FROM agent_group_work)
	AND EXISTS(SELECT FROM agent_group_work JOIN agent_project_tool_calls ON work_id=id WHERE id='one'
	AND thread_id='' AND account_id='owner' AND project_id='p' AND agent_id='pm' AND session_id='native-one'
	AND question='question' AND response_text='answer' AND platform_message_id IS NULL AND receipt->>'id'='task-1')
	AND EXISTS(SELECT FROM agent_project_tool_reads WHERE work_id='one' AND tool='file_reference_check' AND digest='reference_hash' AND args->>'file_ref'='note')
	AND EXISTS(SELECT FROM agent_group_work WHERE id='two' AND session_id='native-two'
	AND platform_message_id='om_platform' AND state='sent' AND thread_id='topic')`).Scan(&ok)
	if err != nil || !ok {
		t.Fatalf("migration reconciliation failed: %v %v", ok, err)
	}
}
