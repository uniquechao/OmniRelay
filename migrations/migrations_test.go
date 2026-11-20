package migrations

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEmbeddedMigrationsAreOrdered(t *testing.T) {
	versions, err := readMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 19 {
		t.Fatalf("预期 19 项迁移，实际 %d", len(versions))
	}
	for i, change := range versions {
		if change.version != i+1 || len(change.digest) != 64 || change.sql == "" {
			t.Fatalf("unexpected migration metadata: %d", change.version)
		}
	}
}

func TestApplyAndCheckPostgreSQL(t *testing.T) {
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	control, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "migration_" + hex.EncodeToString(nonce[:])
	if _, err := control.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = control.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for i := 0; i < 2; i++ {
		if err := Apply(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	if err := Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM omnirelay_schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 19 {
		t.Fatalf("预期已应用 19 项迁移，实际 %d", n)
	}
}

func TestFeishuBotUpgradeFromPasswordSchema(t *testing.T) {
	dsn := os.Getenv("OMNIRELAY_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires isolated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	control, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "bot_upgrade_" + hex.EncodeToString(nonce[:])
	if _, err := control.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = control.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	changes, err := readMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE omnirelay_schema_migrations (
		version integer PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	for _, change := range changes[:4] {
		if _, err := pool.Exec(ctx, change.sql); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, "INSERT INTO omnirelay_schema_migrations (version,checksum) VALUES ($1,$2)", change.version, change.digest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, "INSERT INTO accounts (tenant_key,app_id,open_id) VALUES ('tenant','app','ou_existing')"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO directory_sources (tenant_key,app_id) VALUES ('tenant','app');
		INSERT INTO directory_sync_runs (tenant_key,app_id,trigger,state) VALUES ('tenant','app','initial','succeeded');
		UPDATE directory_sources SET active_version=(SELECT id FROM directory_sync_runs),active_scope_hash='scope',visible=true;
		INSERT INTO directory_people (tenant_key,app_id,snapshot_version,open_id,name,avatar_url,directory_status)
		SELECT tenant_key,app_id,id,'ou_existing','Existing','','active' FROM directory_sync_runs
		UNION ALL SELECT tenant_key,app_id,id,'ou_previously_resigned','Resigned','','resigned' FROM directory_sync_runs;`); err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := Check(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var accounts, botConfigs int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM accounts WHERE open_id='ou_existing'),
		(SELECT count(*) FROM feishu_bot_configs)`).Scan(&accounts, &botConfigs); err != nil {
		t.Fatal(err)
	}
	if accounts != 1 || botConfigs != 0 {
		t.Fatalf("unexpected upgrade state: %d accounts, %d Bot configurations", accounts, botConfigs)
	}
	var roster, current int
	if err := pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (
		WHERE open_id='ou_existing' AND last_scope_hash='scope' AND resigned_at IS NULL)
		FROM directory_roster WHERE tenant_key='tenant' AND app_id='app'`).Scan(&roster, &current); err != nil {
		t.Fatal(err)
	}
	if roster != 1 || current != 1 {
		t.Fatalf("unexpected backfilled roster: %d people, %d current", roster, current)
	}

}
