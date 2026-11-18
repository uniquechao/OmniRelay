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
	if len(versions) != 8 {
		t.Fatalf("expected eight migrations, got %d", len(versions))
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
	if n != 8 {
		t.Fatalf("expected eight applied migrations, got %d", n)
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
}
