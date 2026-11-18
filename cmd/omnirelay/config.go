package main

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type configuration struct {
	databaseURL, listenAddr, publicOrigin  string
	tlsCertFile, tlsKeyFile                string
	appID, appSecret, tenantKey, bootstrap string
	syncInterval                           time.Duration
	pageBudget                             int
}

func readConfig(getenv func(string) string, serving bool) (configuration, error) {
	cfg := configuration{
		databaseURL:  strings.TrimSpace(getenv("OMNIRELAY_DATABASE_URL")),
		listenAddr:   ":8080",
		syncInterval: time.Hour,
		pageBudget:   1000,
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
	cfg.publicOrigin = "https://" + origin.Host
	cfg.tlsCertFile = strings.TrimSpace(getenv("OMNIRELAY_TLS_CERT_FILE"))
	cfg.tlsKeyFile = strings.TrimSpace(getenv("OMNIRELAY_TLS_KEY_FILE"))
	if (cfg.tlsCertFile == "") != (cfg.tlsKeyFile == "") {
		return configuration{}, errors.New("OMNIRELAY_TLS_CERT_FILE and OMNIRELAY_TLS_KEY_FILE must be configured together")
	}
	cfg.appID = strings.TrimSpace(getenv("OMNIRELAY_FEISHU_APP_ID"))
	cfg.appSecret = strings.TrimSpace(getenv("OMNIRELAY_FEISHU_APP_SECRET"))
	cfg.tenantKey = strings.TrimSpace(getenv("OMNIRELAY_FEISHU_TENANT_KEY"))
	cfg.bootstrap = strings.TrimSpace(getenv("OMNIRELAY_BOOTSTRAP_OPEN_ID"))
	if cfg.appID == "" || cfg.appSecret == "" || cfg.tenantKey == "" {
		return configuration{}, errors.New("Feishu app ID, app secret and tenant key are required")
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
