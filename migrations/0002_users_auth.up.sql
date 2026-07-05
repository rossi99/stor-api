CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         citext NOT NULL,
    password_hash text NOT NULL,
    name          text NOT NULL,
    age           int CHECK (age IS NULL OR (age >= 0 AND age <= 150)),
    gender        text,
    currency      text NOT NULL DEFAULT 'GBP',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz
);
CREATE UNIQUE INDEX users_email_active ON users (email) WHERE deleted_at IS NULL;
CREATE TRIGGER users_updated_at BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Opaque refresh tokens, stored hashed. Rotation chains share a family_id so
-- reuse of a rotated token can revoke the whole family. Hard-deleted by a
-- purge, not soft-deleted: revoked_at already carries the semantics.
CREATE TABLE refresh_tokens (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users (id),
    token_hash  bytea NOT NULL UNIQUE,
    family_id   uuid NOT NULL,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    revoked_at  timestamptz,
    replaced_by uuid REFERENCES refresh_tokens (id)
);
CREATE INDEX refresh_tokens_family ON refresh_tokens (family_id);
CREATE INDEX refresh_tokens_user ON refresh_tokens (user_id);

CREATE TABLE password_reset_tokens (
    token_hash bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX password_reset_tokens_user ON password_reset_tokens (user_id);
