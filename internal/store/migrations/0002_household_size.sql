-- Households hold 1 to 4 members; slot 0 stays the joint pot.
ALTER TABLE members DROP CONSTRAINT members_slot_check,
    ADD CONSTRAINT members_slot_check CHECK (slot BETWEEN 1 AND 4);
ALTER TABLE transactions DROP CONSTRAINT transactions_actor_slot_check,
    ADD CONSTRAINT transactions_actor_slot_check CHECK (actor_slot BETWEEN 0 AND 4),
    DROP CONSTRAINT transactions_ledger_slot_check,
    ADD CONSTRAINT transactions_ledger_slot_check CHECK (ledger_slot BETWEEN 0 AND 4);
