-- The budget sheet's settings: 50/30/20 targets, a fixed monthly loan allocation,
-- and a per-person cash account cap (NULL means uncapped).
ALTER TABLE households
    ADD COLUMN essential_pct         int NOT NULL DEFAULT 50,
    ADD COLUMN discretionary_pct     int NOT NULL DEFAULT 30,
    ADD COLUMN saving_pct            int NOT NULL DEFAULT 20,
    ADD COLUMN loan_allocation_pence bigint NOT NULL DEFAULT 0 CHECK (loan_allocation_pence >= 0),
    ADD COLUMN cash_cap_pence        bigint CHECK (cash_cap_pence >= 0),
    ADD CONSTRAINT households_targets_check CHECK (
        least(essential_pct, discretionary_pct, saving_pct) >= 0
        AND essential_pct + discretionary_pct + saving_pct = 100);

-- Recurring bills. paid_from is 0 for the joint pots, else the slot whose cash account pays it.
CREATE TABLE bills (
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    id           text NOT NULL,
    name         text NOT NULL,
    group_name   text NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('bill', 'pot', 'loan', 'saving', 'investment')),
    category     text NOT NULL CHECK (category IN ('essential', 'discretionary', 'saving')),
    amount_pence bigint NOT NULL CHECK (amount_pence >= 0),
    frequency    text NOT NULL CHECK (frequency IN ('monthly', 'weekly', 'quarterly', 'yearly')),
    -- Empty means every month; otherwise the full amount lands in just these months.
    months       smallint[] NOT NULL DEFAULT '{}'
                 CHECK (months <@ '{1,2,3,4,5,6,7,8,9,10,11,12}' AND (months = '{}' OR frequency = 'monthly')),
    paid_from    smallint NOT NULL CHECK (paid_from BETWEEN 0 AND 4),
    due_day      smallint CHECK (due_day BETWEEN 1 AND 31),
    meta         text NOT NULL DEFAULT '',
    is_estimated boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (household_id, id)
);

-- Payday transfers ticked off, per budget month (stored as the first of the month).
CREATE TABLE transfer_ticks (
    household_id uuid NOT NULL REFERENCES households (id) ON DELETE CASCADE,
    month        date NOT NULL,
    key          text NOT NULL,
    PRIMARY KEY (household_id, month, key)
);
