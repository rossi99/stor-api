CREATE TABLE households (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                 text NOT NULL,
    needs_percent        int NOT NULL CHECK (needs_percent BETWEEN 0 AND 100),
    wants_percent        int NOT NULL CHECK (wants_percent BETWEEN 0 AND 100),
    savings_percent      int NOT NULL CHECK (savings_percent BETWEEN 0 AND 100),
    requires_approvals   boolean NOT NULL DEFAULT false,
    surplus_split_method text NOT NULL DEFAULT 'proportional'
        CHECK (surplus_split_method IN ('proportional', 'even')),
    invite_code          text NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    deleted_at           timestamptz,
    CHECK (needs_percent + wants_percent + savings_percent = 100)
);
CREATE UNIQUE INDEX households_invite_code_active ON households (invite_code) WHERE deleted_at IS NULL;
CREATE TRIGGER households_updated_at BEFORE UPDATE ON households
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE household_members (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    user_id      uuid NOT NULL REFERENCES users (id),
    is_creator   boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz
);
-- A user belongs to at most one household at a time (single-household app model).
CREATE UNIQUE INDEX household_members_user_active ON household_members (user_id) WHERE deleted_at IS NULL;
CREATE INDEX household_members_household ON household_members (household_id);
CREATE TRIGGER household_members_updated_at BEFORE UPDATE ON household_members
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
