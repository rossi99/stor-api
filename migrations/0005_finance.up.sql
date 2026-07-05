CREATE TABLE incomes (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id        uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    user_id             uuid NOT NULL REFERENCES users (id),
    gross_monthly_minor bigint NOT NULL CHECK (gross_monthly_minor >= 0),
    net_monthly_minor   bigint NOT NULL CHECK (net_monthly_minor >= 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz
);
CREATE UNIQUE INDEX incomes_user_active ON incomes (user_id) WHERE deleted_at IS NULL;
CREATE INDEX incomes_household ON incomes (household_id) WHERE deleted_at IS NULL;
CREATE TRIGGER incomes_updated_at BEFORE UPDATE ON incomes
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE savings_pots (
    id                         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id               uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    name                       text NOT NULL,
    current_minor              bigint NOT NULL DEFAULT 0 CHECK (current_minor >= 0),
    target_minor               bigint NOT NULL CHECK (target_minor > 0),
    monthly_contribution_minor bigint NOT NULL DEFAULT 0 CHECK (monthly_contribution_minor >= 0),
    emoji                      text NOT NULL DEFAULT '',
    created_at                 timestamptz NOT NULL DEFAULT now(),
    updated_at                 timestamptz NOT NULL DEFAULT now(),
    deleted_at                 timestamptz
);
CREATE INDEX savings_pots_household ON savings_pots (household_id) WHERE deleted_at IS NULL;
CREATE TRIGGER savings_pots_updated_at BEFORE UPDATE ON savings_pots
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE pensions (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id        uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    user_id             uuid NOT NULL REFERENCES users (id),
    current_value_minor bigint NOT NULL DEFAULT 0 CHECK (current_value_minor >= 0),
    target_minor        bigint NOT NULL CHECK (target_minor > 0),
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    deleted_at          timestamptz
);
CREATE UNIQUE INDEX pensions_user_active ON pensions (user_id) WHERE deleted_at IS NULL;
CREATE INDEX pensions_household ON pensions (household_id) WHERE deleted_at IS NULL;
CREATE TRIGGER pensions_updated_at BEFORE UPDATE ON pensions
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE isas (
    id                            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    household_id                  uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    user_id                       uuid NOT NULL REFERENCES users (id),
    type                          text NOT NULL DEFAULT 'Stocks & Shares ISA',
    balance_minor                 bigint NOT NULL DEFAULT 0 CHECK (balance_minor >= 0),
    contributions_this_year_minor bigint NOT NULL DEFAULT 0 CHECK (contributions_this_year_minor >= 0),
    annual_allowance_minor        bigint NOT NULL DEFAULT 2000000 CHECK (annual_allowance_minor >= 0),
    created_at                    timestamptz NOT NULL DEFAULT now(),
    updated_at                    timestamptz NOT NULL DEFAULT now(),
    deleted_at                    timestamptz
);
CREATE UNIQUE INDEX isas_user_active ON isas (user_id) WHERE deleted_at IS NULL;
CREATE INDEX isas_household ON isas (household_id) WHERE deleted_at IS NULL;
CREATE TRIGGER isas_updated_at BEFORE UPDATE ON isas
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE TABLE tax_info (
    user_id                     uuid PRIMARY KEY REFERENCES users (id),
    gross_monthly_minor         bigint NOT NULL DEFAULT 0 CHECK (gross_monthly_minor >= 0),
    tax_code                    text NOT NULL DEFAULT '',
    tax_system                  text NOT NULL DEFAULT 'PAYE',
    tax_year                    text NOT NULL DEFAULT '',
    marital_status              text NOT NULL DEFAULT '',
    pension_contribution_percent int NOT NULL DEFAULT 0 CHECK (pension_contribution_percent BETWEEN 0 AND 100),
    has_student_loan            boolean NOT NULL DEFAULT false,
    student_loan_plan           text,
    created_at                  timestamptz NOT NULL DEFAULT now(),
    updated_at                  timestamptz NOT NULL DEFAULT now(),
    deleted_at                  timestamptz
);
CREATE TRIGGER tax_info_updated_at BEFORE UPDATE ON tax_info
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Month-end totals powering the dashboard's "vs last month" comparison.
-- Upserted lazily whenever the summary endpoint is read.
CREATE TABLE monthly_snapshots (
    household_id         uuid NOT NULL REFERENCES households (id) ON DELETE RESTRICT,
    month                date NOT NULL,
    total_expenses_minor bigint NOT NULL DEFAULT 0,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    deleted_at           timestamptz,
    PRIMARY KEY (household_id, month)
);
CREATE TRIGGER monthly_snapshots_updated_at BEFORE UPDATE ON monthly_snapshots
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
