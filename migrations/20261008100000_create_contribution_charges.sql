-- +goose Up
-- Charges are immutable obligations. Existing allocation rows are their payments;
-- a second payment ledger would duplicate receipt accounting.
CREATE TABLE contribution_charges (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key TEXT NOT NULL UNIQUE CHECK (length(btrim(idempotency_key)) > 0),
    member_id UUID NOT NULL REFERENCES members(id) ON DELETE RESTRICT,
    branch_id BIGINT NOT NULL,
    category TEXT NOT NULL CHECK (category IN ('literature', 'caritas_registration', 'lsf', 'laptop', 'penalty', 'other')),
    amount NUMERIC(19,4) NOT NULL CHECK (amount > 0),
    reason TEXT NOT NULL CHECK (length(btrim(reason)) > 0),
    created_by UUID NOT NULL REFERENCES staff_users(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_contribution_charges_member_cursor ON contribution_charges (member_id, branch_id, created_at DESC, id DESC);
CREATE INDEX idx_contribution_allocations_charge ON contribution_allocations (target_id)
    WHERE type IN ('other_charge', 'penalty') AND status = 'completed';

-- +goose Down
DROP INDEX idx_contribution_allocations_charge;
DROP TABLE contribution_charges;
