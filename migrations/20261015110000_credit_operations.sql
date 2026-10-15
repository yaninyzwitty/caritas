-- +goose Up
CREATE TABLE credit_operations (
    id UUID PRIMARY KEY,
    credit_balance_id UUID NOT NULL REFERENCES credit_balances(id),
    amount NUMERIC(19,4) NOT NULL CHECK (amount > 0),
    action TEXT NOT NULL CHECK (action IN ('transfer_shares', 'apply_loan', 'request_refund')),
    target_id UUID,
    contribution_period DATE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'approved', 'completed', 'rejected')),
    reason TEXT NOT NULL CHECK (btrim(reason) <> ''),
    consent_reference TEXT NOT NULL CHECK (btrim(consent_reference) <> ''),
    requested_by UUID NOT NULL,
    reviewed_by UUID,
    reviewed_at TIMESTAMPTZ,
    review_reason TEXT,
    completed_by UUID,
    completed_at TIMESTAMPTZ,
    payout_reference TEXT UNIQUE,
    payout_evidence TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX credit_operations_credit ON credit_operations(credit_balance_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE credit_operations;
