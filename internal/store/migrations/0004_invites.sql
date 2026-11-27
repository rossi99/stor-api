-- One live invite per household. Codes are stored as SHA-256, like session tokens.
CREATE TABLE invites (
    household_id uuid PRIMARY KEY REFERENCES households (id) ON DELETE CASCADE,
    code_hash    bytea NOT NULL UNIQUE,
    expires_at   timestamptz NOT NULL
);
