-- +goose Up
CREATE TABLE contribution_monthly_fees (
    member_id UUID NOT NULL REFERENCES members(id),
    period DATE NOT NULL CHECK (EXTRACT(DAY FROM period) = 1),
    type contribution_allocation_type NOT NULL CHECK (type IN ('com', 'lgom')),
    amount NUMERIC(19,4) NOT NULL DEFAULT 30 CHECK (amount >= 0),
    exemption_reason TEXT,
    approved_by UUID REFERENCES staff_users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (member_id, period, type),
    CHECK (amount > 0 OR (approved_by IS NOT NULL AND exemption_reason IS NOT NULL AND length(btrim(exemption_reason)) > 0))
);

CREATE TABLE loan_monthly_interest (
    loan_id UUID NOT NULL REFERENCES loans(id),
    period DATE NOT NULL CHECK (EXTRACT(DAY FROM period) = 1),
    previous_balance NUMERIC(19,4) NOT NULL CHECK (previous_balance >= 0),
    rate NUMERIC NOT NULL CHECK (rate >= 0),
    amount NUMERIC(19,4) NOT NULL CHECK (amount >= 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (loan_id, period)
);

-- +goose Down
DROP TABLE loan_monthly_interest;
DROP TABLE contribution_monthly_fees;
