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
	if cfg, err := readConfig(func(key string) string { return env[key] }, true); err != nil || cfg.appSecret != "" {
		t.Fatal("server should expose missing Feishu credentials")
	}
}

func TestCandidateReceiverConfig(t *testing.T) {
	env := goodEnvironment()
	read := func() (configuration, error) {
		return readConfig(func(key string) string { return env[key] }, true)
	}
	if cfg, err := read(); err != nil || cfg.candidateReceiverToken != "" {
		t.Fatalf("candidate receiver should default off: %v", err)
	}
	previous := receiverSourceSHA
	receiverSourceSHA = strings.Repeat("a", 40)
	t.Cleanup(func() { receiverSourceSHA = previous })
	env["OMNIRELAY_CANDIDATE_RECEIVER_TOKEN"] = strings.Repeat("b", 64)
	env["OMNIRELAY_TLS_CERT_FILE"] = "/cert"
	env["OMNIRELAY_TLS_KEY_FILE"] = "/key"
	env["OMNIRELAY_FEISHU_BOT_LEASE_NAMESPACE"] = "fixture"
	env["OMNIRELAY_FEISHU_BOT_POD_UID"] = "fixture-pod"
	if _, err := read(); err == nil {
		t.Fatal("receiver without a recorded rollback point")
	}
	env["OMNIRELAY_CANDIDATE_RECEIVER_ROLLBACK"] = "registry.example.test/omnirelay@sha256:" + strings.Repeat("c", 64)
	if _, err := read(); err != nil {
		t.Fatal(err)
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

func TestConfigTLSRequiresCertificateAndKeyTogether(t *testing.T) {
	env := goodEnvironment()
	env["OMNIRELAY_TLS_CERT_FILE"] = "/certificates/tls.crt"
	if _, err := readConfig(func(key string) string { return env[key] }, true); err == nil {
		t.Fatal("accepted a certificate without the TLS private key")
	}
}

func TestConfigFeishuBotGenerationAndHeartbeat(t *testing.T) {
	env := goodEnvironment()
	get := func(key string) string { return env[key] }
	cfg, err := readConfig(get, true)
	if err != nil || cfg.credentialsGeneration != 1 || cfg.botHeartbeatTTL != 30*time.Second {
		t.Fatalf("unexpected default Bot settings: %d %v %v", cfg.credentialsGeneration, cfg.botHeartbeatTTL, err)
	}
	for _, invalid := range []string{"0", "-1", "01", "+1", "9223372036854775808", "private-value"} {
		env["OMNIRELAY_FEISHU_CREDENTIALS_GENERATION"] = invalid
		if _, err := readConfig(get, true); err == nil || strings.Contains(err.Error(), invalid) {
			t.Errorf("expected sanitized generation error")
		}
	}
	env["OMNIRELAY_FEISHU_CREDENTIALS_GENERATION"] = "12"
	env["OMNIRELAY_FEISHU_BOT_HEARTBEAT_TTL"] = "0s"
	if _, err := readConfig(get, true); err == nil {
		t.Fatal("accepted zero heartbeat TTL")
	}
	env["OMNIRELAY_FEISHU_BOT_HEARTBEAT_TTL"] = "45s"
	cfg, err = readConfig(get, true)
	if err != nil || cfg.credentialsGeneration != 12 || cfg.botHeartbeatTTL != 45*time.Second {
		t.Fatalf("unexpected custom Bot settings: %d %v %v", cfg.credentialsGeneration, cfg.botHeartbeatTTL, err)
	}
}

func TestConfigFeishuBotSendOptIn(t *testing.T) {
	env := goodEnvironment()
	get := func(key string) string { return env[key] }
	cfg, err := readConfig(get, true)
	if err != nil || cfg.botSendEnabled {
		t.Fatalf("expected Bot send to be disabled by default: %v %v", cfg.botSendEnabled, err)
	}
	env["OMNIRELAY_FEISHU_BOT_SEND_ENABLED"] = "true"
	cfg, err = readConfig(get, true)
	if err != nil || !cfg.botSendEnabled {
		t.Fatalf("expected explicit Bot send enablement: %v %v", cfg.botSendEnabled, err)
	}
}

func TestConfigFeishuBotLeaseRequiresNamespaceAndPodUID(t *testing.T) {
	env := goodEnvironment()
	get := func(key string) string { return env[key] }
	env["OMNIRELAY_FEISHU_BOT_LEASE_NAMESPACE"] = "staging"
	if _, err := readConfig(get, true); err == nil {
		t.Fatal("accepted Lease namespace without a Pod UID")
	}
	env["OMNIRELAY_FEISHU_BOT_POD_UID"] = "pod-fixture"
	cfg, err := readConfig(get, true)
	if err != nil || cfg.botLeaseNamespace != "staging" || cfg.botPodUID != "pod-fixture" {
		t.Fatalf("unexpected Lease config: %s %s %v", cfg.botLeaseNamespace, cfg.botPodUID, err)
	}
}

func TestConfigAllowsWebSetupWithNoFeishuEnvironment(t *testing.T) {
	env := goodEnvironment()
	delete(env, "OMNIRELAY_FEISHU_APP_ID")
	delete(env, "OMNIRELAY_FEISHU_APP_SECRET")
	delete(env, "OMNIRELAY_FEISHU_TENANT_KEY")
	get := func(key string) string { return env[key] }

	env["OMNIRELAY_PUBLIC_ORIGIN"] = "https://RELAY.example.test:443/"
	env["OMNIRELAY_SETUP_MODE"] = "true"
	env["OMNIRELAY_SETUP_KEY"] = strings.Repeat("12", 32)
	cfg, err := readConfig(get, true)
	if err != nil || cfg.appID != "" || cfg.tenantKey != "" {
		t.Fatalf("web setup should start with no Feishu credentials: %v", err)
	}
	if cfg.publicOrigin != "https://relay.example.test" {
		t.Fatalf("web setup origin must match the browser: %s", cfg.publicOrigin)
	}
	env["OMNIRELAY_SETUP_KEY"] = "invalid"
	if _, err := readConfig(get, true); err == nil {
		t.Fatal("web setup must require a valid setup key")
	}
}

func TestConfigOAuthProxyTrustRequiresTLS(t *testing.T) {
	env := goodEnvironment()
	get := func(key string) string { return env[key] }
	token := strings.Repeat("ab", 32)
	env["OMNIRELAY_OAUTH_PROXY_TOKEN"] = token
	if _, err := readConfig(get, true); err == nil {
		t.Fatal("accepted OAuth proxy trust without listener TLS")
	}
	env["OMNIRELAY_TLS_CERT_FILE"] = "/certificates/tls.crt"
	env["OMNIRELAY_TLS_KEY_FILE"] = "/certificates/tls.key"
	cfg, err := readConfig(get, true)
	if err != nil || cfg.oauthProxyToken != token {
		t.Fatal("could not configure authenticated OAuth proxy")
	}
	env["OMNIRELAY_OAUTH_PROXY_TOKEN"] = "private-invalid-token"
	if _, err := readConfig(get, true); err == nil || strings.Contains(err.Error(), env["OMNIRELAY_OAUTH_PROXY_TOKEN"]) {
		t.Fatal("invalid OAuth proxy token was accepted or exposed")
	}
}
