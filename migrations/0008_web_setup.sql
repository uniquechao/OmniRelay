CREATE TABLE omnirelay_web_setup (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    credentials bytea NOT NULL
);

CREATE TABLE omnirelay_web_setup_logins (
    state_digest bytea PRIMARY KEY,
    browser_digest bytea NOT NULL,
    challenge bytea NOT NULL,
    expires_at timestamptz NOT NULL
);
