-- +goose Up
CREATE TYPE loan_transaction_type AS ENUM ('disbursement', 'repayment', 'penalty', 'reversal', 'credit_withdrawal');

CREATE TABLE loan_transactions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    loan_id UUID NOT NULL,
    type loan_transaction_type NOT NULL,
    amount NUMERIC(19,4) NOT NULL,
    reference_id UUID NOT NULL,
    payment_gateway_transaction_id TEXT,
    allocation_breakdown JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_by UUID, -- loan officer id or something

    CONSTRAINT fk_loan_transactions_loan FOREIGN KEY (loan_id) REFERENCES loans(id) ON DELETE RESTRICT,
    CONSTRAINT chk_loan_transactions_amount_positive CHECK (amount > 0),
    CONSTRAINT chk_loan_transactions_gateway_id_not_blank CHECK (payment_gateway_transaction_id IS NULL OR btrim(payment_gateway_transaction_id) <> '')
);

CREATE INDEX idx_loan_transactions_loan_cursor ON loan_transactions (loan_id, created_at DESC, id DESC);
CREATE UNIQUE INDEX idx_loan_transactions_one_disbursement ON loan_transactions (loan_id) WHERE type = 'disbursement';
CREATE UNIQUE INDEX idx_loan_transactions_gateway_id ON loan_transactions (loan_id, payment_gateway_transaction_id) WHERE payment_gateway_transaction_id IS NOT NULL;
CREATE UNIQUE INDEX idx_loan_transactions_reference ON loan_transactions (loan_id, reference_id, type);

-- +goose Down
DROP INDEX IF EXISTS idx_loan_transactions_reference;
DROP INDEX IF EXISTS idx_loan_transactions_gateway_id;
DROP INDEX IF EXISTS idx_loan_transactions_one_disbursement;
DROP INDEX IF EXISTS idx_loan_transactions_loan_cursor;
DROP TABLE IF EXISTS loan_transactions;
DROP TYPE IF EXISTS loan_transaction_type;
