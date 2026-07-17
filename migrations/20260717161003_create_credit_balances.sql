-- +goose Up
CREATE TYPE credit_balance_source AS ENUM ('overpayment', 'refund', 'adjustment');
CREATE TYPE credit_balance_status AS ENUM ('available', 'frozen', 'withdrawn');

CREATE TABLE credit_balances (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id UUID NOT NULL,
    loan_id UUID,
    amount NUMERIC(19,4) NOT NULL,
    source credit_balance_source NOT NULL,
    status credit_balance_status NOT NULL DEFAULT 'available',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_activity_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_credit_balances_member FOREIGN KEY (member_id) REFERENCES members(id) ON DELETE RESTRICT,
    CONSTRAINT fk_credit_balances_loan FOREIGN KEY (loan_id) REFERENCES loans(id) ON DELETE RESTRICT,
    CONSTRAINT chk_credit_balances_amount_positive CHECK (amount > 0)
);

CREATE INDEX idx_credit_balances_member ON credit_balances (member_id, status);
CREATE INDEX idx_credit_balances_loan ON credit_balances (loan_id) WHERE loan_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_credit_balances_loan;
DROP INDEX IF EXISTS idx_credit_balances_member;
DROP TABLE IF EXISTS credit_balances;
DROP TYPE IF EXISTS credit_balance_status;
DROP TYPE IF EXISTS credit_balance_source;
