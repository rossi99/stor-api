CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email         text NOT NULL UNIQUE CHECK (email = lower(email)),
    password_hash text NOT NULL
);

-- Opaque bearer tokens, stored as SHA-256 so a database leak yields no usable tokens.
CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);

CREATE TABLE households (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    top_up_day         int NOT NULL DEFAULT 1 CHECK (top_up_day BETWEEN 1 AND 28),
    setup_complete     boolean NOT NULL DEFAULT false
);

-- Ledgers are stored as absolute slots: 0 joint, 1 and 2 the members.
-- The API maps them to you/partner relative to whoever is signed in.
CREATE TABLE members (
    household_id       uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    slot               smallint NOT NULL CHECK (slot IN (1, 2)),
    user_id            uuid UNIQUE REFERENCES users (id),
    contribution_pence bigint NOT NULL DEFAULT 0 CHECK (contribution_pence >= 0),
    PRIMARY KEY (household_id, slot)
);

CREATE TABLE transactions (
    household_id  uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    id            text NOT NULL,
    occurred_at   timestamptz NOT NULL,
    title         text NOT NULL,
    category      text NOT NULL,
    amount_pence  bigint NOT NULL,
    actor_slot    smallint CHECK (actor_slot IN (0, 1, 2)),
    actor_name    text,
    ledger_slot   smallint NOT NULL CHECK (ledger_slot IN (0, 1, 2)),
    tag           text NOT NULL,
    split_percent int CHECK (split_percent BETWEEN 0 AND 100),
    monogram      text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (household_id, id)
);
CREATE INDEX transactions_recent ON transactions (household_id, occurred_at DESC);

CREATE TABLE goals (
    household_id  uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    id            text NOT NULL,
    name          text NOT NULL,
    saved_pence   bigint NOT NULL DEFAULT 0,
    target_pence  bigint NOT NULL,
    monthly_pence bigint NOT NULL DEFAULT 0 CHECK (monthly_pence >= 0),
    note          text NOT NULL DEFAULT '',
    PRIMARY KEY (household_id, id)
);
