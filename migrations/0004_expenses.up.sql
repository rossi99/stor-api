CREATE TABLE expenses (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name         text NOT NULL,
    amount_minor bigint NOT NULL CHECK (amount_minor >= 0),
    frequency    text NOT NULL CHECK (frequency IN ('weekly', 'monthly', 'annual')),
    category     text NOT NULL CHECK (category IN (
        'bills', 'subscriptions', 'sinkingFunds', 'groceries', 'transport',
        'eatingOut', 'entertainment', 'health', 'clothing', 'other')),
    added_by     uuid NOT NULL REFERENCES users (id),
    status       text NOT NULL DEFAULT 'approved' CHECK (status IN ('pending', 'approved')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz
);
CREATE INDEX expenses_household ON expenses (household_id) WHERE deleted_at IS NULL;
CREATE TRIGGER expenses_updated_at BEFORE UPDATE ON expenses
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- One row per member sign-off; the set of user_ids is the app's `agreedBy`.
CREATE TABLE expense_approvals (
    expense_id uuid NOT NULL REFERENCES expenses (id) ON DELETE RESTRICT,
    user_id    uuid NOT NULL REFERENCES users (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz,
    PRIMARY KEY (expense_id, user_id)
);
CREATE TRIGGER expense_approvals_updated_at BEFORE UPDATE ON expense_approvals
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
