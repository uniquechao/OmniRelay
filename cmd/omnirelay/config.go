package main

import (
	"context"
	"encoding/hex"
	"errors"
	"github.com/uniquechao/OmniRelay/internal/directory"
	"github.com/uniquechao/OmniRelay/internal/identity"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type configuration struct {
	candidateOAuth                         *identity.CandidateOAuth
	candidateRead                          func(context.Context, string) (directory.SampleSnapshot, error)
	databaseURL, listenAddr, publicOrigin  string
	tlsCertFile, tlsKeyFile                string
	appID, appSecret, tenantKey, bootstrap string
	syncInterval, sessionIdleTTL           time.Duration
	pageBudget                             int
	credentialsGeneration                  int64
	botHeartbeatTTL                        time.Duration
	botSendEnabled                         bool
	botLeaseNamespace, botPodUID           string
	setupKey                               []byte
	oauthProxyToken                        string
	candidateReceiverToken                 string
	candidateReceiverRollback              string
	candidateMessageRun                    *webRun
}

func readConfig(getenv func(string) string, serving bool) (configuration, error) {
	cfg := configuration{
		databaseURL:           strings.TrimSpace(getenv("OMNIRELAY_DATABASE_URL")),
		listenAddr:            ":8080",
		syncInterval:          time.Hour,
		pageBudget:            1000,
		credentialsGeneration: 1,
		botHeartbeatTTL:       30 * time.Second,
	}
	database, err := url.Parse(cfg.databaseURL)
	if err != nil || database.Hostname() == "" || len(strings.Trim(database.Path, "/")) == 0 ||
		(database.Scheme != "postgresql" && database.Scheme != "postgres") || database.Fragment != "" {
		return configuration{}, errors.New("OMNIRELAY_DATABASE_URL must specify the PostgreSQL host and database")
	}
	parameters, err := url.ParseQuery(database.RawQuery)
	if err != nil || len(parameters["sslmode"]) != 1 || parameters.Get("sslmode") != "verify-full" {
		return configuration{}, errors.New("OMNIRELAY_DATABASE_URL must use sslmode=verify-full")
	}
	if !serving {
		return cfg, nil
	}
	cfg.publicOrigin = strings.TrimSpace(getenv("OMNIRELAY_PUBLIC_ORIGIN"))
	origin, err := url.Parse(cfg.publicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil ||
		(origin.Path != "" && origin.Path != "/") || origin.RawQuery != "" || origin.ForceQuery ||
		origin.Fragment != "" || strings.Contains(cfg.publicOrigin, "#") {
		return configuration{}, errors.New("OMNIRELAY_PUBLIC_ORIGIN must be an HTTPS origin")
	}
	host := strings.ToLower(origin.Host)
	if origin.Port() == "443" {
		host = strings.TrimSuffix(host, ":443")
	}
	cfg.publicOrigin = "https://" + host
	cfg.tlsCertFile = strings.TrimSpace(getenv("OMNIRELAY_TLS_CERT_FILE"))
	cfg.tlsKeyFile = strings.TrimSpace(getenv("OMNIRELAY_TLS_KEY_FILE"))
	if (cfg.tlsCertFile == "") != (cfg.tlsKeyFile == "") {
		return configuration{}, errors.New("OMNIRELAY_TLS_CERT_FILE and OMNIRELAY_TLS_KEY_FILE must be configured together")
	}
	cfg.oauthProxyToken = strings.TrimSpace(getenv("OMNIRELAY_OAUTH_PROXY_TOKEN"))
	if cfg.oauthProxyToken != "" {
		token, err := hex.DecodeString(cfg.oauthProxyToken)
		if err != nil || len(token) != 32 {
			return configuration{}, errors.New("OMNIRELAY_OAUTH_PROXY_TOKEN must contain a 32-byte hex-encoded token")
		}
		if cfg.tlsCertFile == "" {
			return configuration{}, errors.New("OMNIRELAY_OAUTH_PROXY_TOKEN requires TLS on the Go listener")
		}
	}
	cfg.appID = strings.TrimSpace(getenv("OMNIRELAY_FEISHU_APP_ID"))
	cfg.appSecret = strings.TrimSpace(getenv("OMNIRELAY_FEISHU_APP_SECRET"))
	cfg.tenantKey = strings.TrimSpace(getenv("OMNIRELAY_FEISHU_TENANT_KEY"))
	cfg.bootstrap = strings.TrimSpace(getenv("OMNIRELAY_BOOTSTRAP_OPEN_ID"))
	if strings.TrimSpace(getenv("OMNIRELAY_SETUP_MODE")) == "true" {
		cfg.setupKey, err = parseSetupKey(getenv("OMNIRELAY_SETUP_KEY"))
		if err != nil {
			return configuration{}, err
		}
	}
	if cfg.appID == "" || cfg.tenantKey == "" {
		if len(cfg.setupKey) == 0 {
			return configuration{}, errors.New("Feishu app ID and tenant key are required")
		}
		if cfg.appID != "" || cfg.appSecret != "" || cfg.tenantKey != "" || cfg.bootstrap != "" {
			return configuration{}, errors.New("remove partial Feishu environment settings to use web setup")
		}
	}
	if generation := strings.TrimSpace(getenv("OMNIRELAY_FEISHU_CREDENTIALS_GENERATION")); generation != "" {
		cfg.credentialsGeneration, err = strconv.ParseInt(generation, 10, 64)
		if err != nil || cfg.credentialsGeneration < 1 || strconv.FormatInt(cfg.credentialsGeneration, 10) != generation {
			return configuration{}, errors.New("OMNIRELAY_FEISHU_CREDENTIALS_GENERATION must be a positive decimal integer")
		}
	}
	if ttl := strings.TrimSpace(getenv("OMNIRELAY_FEISHU_BOT_HEARTBEAT_TTL")); ttl != "" {
		cfg.botHeartbeatTTL, err = time.ParseDuration(ttl)
		if err != nil || cfg.botHeartbeatTTL <= 0 {
			return configuration{}, errors.New("OMNIRELAY_FEISHU_BOT_HEARTBEAT_TTL must be a positive duration")
		}
	}
	cfg.botLeaseNamespace = strings.TrimSpace(getenv("OMNIRELAY_FEISHU_BOT_LEASE_NAMESPACE"))
	cfg.botSendEnabled = strings.TrimSpace(getenv("OMNIRELAY_FEISHU_BOT_SEND_ENABLED")) == "true"
	cfg.botPodUID = strings.TrimSpace(getenv("OMNIRELAY_FEISHU_BOT_POD_UID"))
	if (cfg.botLeaseNamespace == "") != (cfg.botPodUID == "") {
		return configuration{}, errors.New("Feishu Bot Lease namespace and Pod UID must be configured together")
	}
	cfg.candidateReceiverToken = strings.TrimSpace(getenv("OMNIRELAY_CANDIDATE_RECEIVER_TOKEN"))
	if cfg.candidateReceiverToken != "" {
		cfg.candidateReceiverRollback = getenv("OMNIRELAY_CANDIDATE_RECEIVER_ROLLBACK")
		token, err := hex.DecodeString(cfg.candidateReceiverToken)
		sha, shaErr := hex.DecodeString(receiverSourceSHA)
		if err != nil || len(token) != 32 || shaErr != nil || len(sha) != 20 ||
			!receiverRollbackRef.MatchString(cfg.candidateReceiverRollback) || cfg.tlsCertFile == "" || cfg.botLeaseNamespace == "" {
			return configuration{}, errors.New("候选消息接收端需要固定源码版本、回退摘要、TLS、Bot Lease 和独立服务令牌")
		}
	}
	if addr := strings.TrimSpace(getenv("OMNIRELAY_LISTEN_ADDR")); addr != "" {
		cfg.listenAddr = addr
	}
	if interval := strings.TrimSpace(getenv("OMNIRELAY_DIRECTORY_INTERVAL")); interval != "" {
		cfg.syncInterval, err = time.ParseDuration(interval)
		if err != nil || cfg.syncInterval < time.Minute || cfg.syncInterval > 24*time.Hour {
			return configuration{}, errors.New("OMNIRELAY_DIRECTORY_INTERVAL must be between 1m and 24h")
		}
	}
	if budget := strings.TrimSpace(getenv("OMNIRELAY_DIRECTORY_PAGE_BUDGET")); budget != "" {
		cfg.pageBudget, err = strconv.Atoi(budget)
		if err != nil || cfg.pageBudget < 1 || cfg.pageBudget > 5000 {
			return configuration{}, errors.New("OMNIRELAY_DIRECTORY_PAGE_BUDGET must be between 1 and 5000")
		}
	}
	return cfg, nil
}

func parseSetupKey(value string) ([]byte, error) {
	key, err := hex.DecodeString(strings.TrimSpace(value))
	if err != nil || len(key) != 32 {
		return nil, errors.New("OMNIRELAY_SETUP_KEY must contain a 32-byte hex-encoded key")
	}
	return key, nil
}
