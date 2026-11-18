CREATE TABLE account_passwords (
    account_id text PRIMARY KEY REFERENCES accounts (id) ON DELETE CASCADE,
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    username text NOT NULL CHECK (username ~ '^[a-z0-9][a-z0-9_.-]{2,63}$'),
    salt bytea NOT NULL CHECK (octet_length(salt) = 16),
    password_hash bytea NOT NULL CHECK (octet_length(password_hash) = 32),
    UNIQUE (tenant_key, app_id, username)
);

CREATE TABLE password_attempts (
    tenant_key text NOT NULL,
    app_id text NOT NULL,
    bucket text NOT NULL,
    attempts integer NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (tenant_key, app_id, bucket)
);
CREATE INDEX password_attempts_expiry_idx ON password_attempts(expires_at);

ALTER TABLE web_sessions
    ADD COLUMN auth_method text NOT NULL DEFAULT 'feishu' CHECK (auth_method IN ('feishu','password'));
