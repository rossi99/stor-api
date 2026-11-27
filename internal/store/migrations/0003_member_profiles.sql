ALTER TABLE members
    ADD COLUMN name          text NOT NULL DEFAULT '' CHECK (char_length(name) <= 60),
    ADD COLUMN net_pay_pence bigint NOT NULL DEFAULT 0 CHECK (net_pay_pence >= 0);
