package main

import (
	"strings"
	"testing"
	"time"
)

func TestConfigRejectsUnsafeDatabaseAndOrigins(t *testing.T) {
	for _, input := range []string{
		"postgresql://user:do-not-disclose@db.example.test/relay?sslmode=disable",
		"postgresql://user:do-not-disclose@db.example.test/relay?sslmode=require",
		"postgresql://user:do-not-disclose@db.example.test/relay?sslmode=verify-full&sslmode=disable",
	} {
		env := goodEnvironment()
		env["OMNIRELAY_DATABASE_URL"] = input
		_, err := readConfig(func(key string) string { return env[key] }, false)
		if err == nil || strings.Contains(err.Error(), "do-not-disclose") {
			t.Fatalf("expected sanitized database configuration failure, got %v", err)
		}
	}
	for _, origin := range []string{
		"http://relay.example.test",
		"https://relay.example.test/deep/path",
		"https://user:private-value@relay.example.test",
		"https://relay.example.test/#fragment",
	} {
		env := goodEnvironment()
		env["OMNIRELAY_PUBLIC_ORIGIN"] = origin
		if _, err := readConfig(func(key string) string { return env[key] }, true); err == nil {
			t.Fatalf("unexpected origin acceptance: %q", origin)
		}
	}
}

func TestConfigServeDefaultsAndMigrationCredentialBoundary(t *testing.T) {
	env := goodEnvironment()
	cfg, err := readConfig(func(key string) string { return env[key] }, true)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.listenAddr != ":8080" || cfg.syncInterval != time.Hour || cfg.pageBudget != 1000 {
		t.Fatalf("unexpected runtime defaults: %q, %v", cfg.listenAddr, cfg.syncInterval)
	}
	delete(env, "OMNIRELAY_FEISHU_APP_SECRET")
	if _, err := readConfig(func(key string) string { return env[key] }, false); err != nil {
		t.Fatalf("migration should only need its database credentials: %v", err)
	}
	if _, err := readConfig(func(key string) string { return env[key] }, true); err == nil {
		t.Fatal("server accepted missing Feishu secret")
	}
}

func goodEnvironment() map[string]string {
	return map[string]string{
		"OMNIRELAY_DATABASE_URL":      "postgresql://user@db.example.test/relay?sslmode=verify-full",
		"OMNIRELAY_PUBLIC_ORIGIN":     "https://relay.example.test",
		"OMNIRELAY_FEISHU_APP_ID":     "cli_example",
		"OMNIRELAY_FEISHU_APP_SECRET": "test-secret-value",
		"OMNIRELAY_FEISHU_TENANT_KEY": "tenant_example",
	}
}

func TestConfigDirectoryPageBudget(t *testing.T) {
	env := goodEnvironment()
	get := func(key string) string { return env[key] }
	for _, invalid := range []string{"0", "-1", "5001", "many"} {
		env["OMNIRELAY_DIRECTORY_PAGE_BUDGET"] = invalid
		if _, err := readConfig(get, true); err == nil {
			t.Errorf("accepted invalid page budget %q", invalid)
		}
	}
	env["OMNIRELAY_DIRECTORY_PAGE_BUDGET"] = "2000"
	if cfg, err := readConfig(get, true); err != nil || cfg.pageBudget != 2000 {
		t.Fatalf("custom page budget not read: %d, %v", cfg.pageBudget, err)
	}
}
