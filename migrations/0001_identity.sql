CREATE TABLE accounts (
    id text PRIMARY KEY DEFAULT gen_random_uuid()::text,
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    open_id text NOT NULL,
    name text NOT NULL DEFAULT '',
    avatar_url text NOT NULL DEFAULT '',
    active boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (tenant_key, app_id, open_id)
);

CREATE TABLE oauth_login_transactions (
    state_digest bytea PRIMARY KEY CHECK (octet_length(state_digest) = 32),
    browser_digest bytea NOT NULL CHECK (octet_length(browser_digest) = 32),
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    redirect_uri text NOT NULL,
    pkce_verifier text NOT NULL,
    return_path text NOT NULL,
    expires_at timestamptz NOT NULL
);

CREATE INDEX oauth_login_transactions_expiry_idx ON oauth_login_transactions (expires_at);

CREATE TABLE web_sessions (
    token_digest bytea PRIMARY KEY CHECK (octet_length(token_digest) = 32),
    account_id text NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at timestamptz NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    CHECK (idle_expires_at <= expires_at)
);

CREATE INDEX web_sessions_account_idx ON web_sessions (account_id);
CREATE INDEX web_sessions_idle_expiry_idx ON web_sessions (idle_expires_at);
