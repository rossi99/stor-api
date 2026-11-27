-- Apple and Google accounts have no password; user_id UNIQUE keeps one sign-in method per account.
ALTER TABLE users ALTER COLUMN password_hash DROP NOT NULL;

CREATE TABLE identities (
    provider text NOT NULL CHECK (provider IN ('apple', 'google')),
    subject  text NOT NULL,
    user_id  uuid NOT NULL UNIQUE REFERENCES users (id) ON DELETE CASCADE,
    PRIMARY KEY (provider, subject)
);
