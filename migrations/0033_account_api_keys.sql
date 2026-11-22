CREATE TABLE account_api_keys (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id text NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    realm text NOT NULL,
    label text NOT NULL CHECK (char_length(label) BETWEEN 1 AND 80),
    secret_digest bytea NOT NULL UNIQUE CHECK (octet_length(secret_digest) = 32),
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '30 days')
);
CREATE INDEX account_api_keys_owner ON account_api_keys (account_id, realm, expires_at);

CREATE FUNCTION revoke_disabled_account_keys() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM account_api_keys WHERE account_id = NEW.id;
    RETURN NEW;
END;
$$;
CREATE TRIGGER revoke_disabled_account_keys AFTER UPDATE OF active ON accounts
    FOR EACH ROW WHEN (OLD.active AND NOT NEW.active) EXECUTE FUNCTION revoke_disabled_account_keys();
