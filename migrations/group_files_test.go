package migrations

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGroupFileUpgradeKeepsLegacyForReview(t *testing.T) {
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("需要隔离的 PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
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
	run := func(query string) {
		t.Helper()
		if _, err := tx.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	run("CREATE SCHEMA group_file_upgrade_test; SET LOCAL search_path=group_file_upgrade_test")
	changes, err := readMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range changes[:21] {
		run(c.sql)
		if c.version == 16 {
			run(`INSERT INTO feishu_files(tenant_key,kind,token) VALUES('t','docx','doc');
            INSERT INTO feishu_file_projects VALUES('t','a',1,'unknown')`)
		}
	}
	run(`INSERT INTO feishu_file_projects VALUES('t','a',1,'inherited');
    SELECT set_config('omnirelay.file_project_actor','editor',true);
    INSERT INTO feishu_file_projects VALUES('t','a',1,'manual');
    INSERT INTO feishu_file_projects VALUES('t','a',1,'unknown') ON CONFLICT DO NOTHING`)
	run(changes[21].sql)
	var count int
	var manual string
	if err := tx.QueryRow(ctx, `SELECT (SELECT count(*) FROM feishu_file_project_legacy),string_agg(project_id,',') FROM feishu_file_manual_projects`).Scan(&count, &manual); err != nil || count != 3 || manual != "manual" {
		t.Fatal(count, manual, err)
	}
}
