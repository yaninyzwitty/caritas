-- +goose Up
CREATE TYPE contribution_payment_request_status AS ENUM ('pending', 'completed', 'failed');

CREATE TABLE contribution_payment_requests (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    checkout_request_id TEXT NOT NULL,
    member_id UUID NOT NULL,
    branch_id BIGINT NOT NULL,
    contribution_period DATE NOT NULL,
    expected_amount NUMERIC(19,4) NOT NULL,
    allocation_plan JSONB NOT NULL,
    status contribution_payment_request_status NOT NULL DEFAULT 'pending',
    receipt_id UUID,
    failure_reason TEXT,
    requested_by UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_contribution_payment_requests_member FOREIGN KEY (member_id) REFERENCES members(id) ON DELETE RESTRICT,
    CONSTRAINT fk_contribution_payment_requests_receipt FOREIGN KEY (receipt_id) REFERENCES contribution_receipts(id) ON DELETE RESTRICT,
    CONSTRAINT contribution_payment_requests_checkout_request_id_key UNIQUE (checkout_request_id),
    CONSTRAINT chk_contribution_payment_requests_checkout_not_blank CHECK (btrim(checkout_request_id) <> ''),
    CONSTRAINT chk_contribution_payment_requests_amount_positive CHECK (expected_amount > 0),
    CONSTRAINT chk_contribution_payment_requests_plan_object CHECK (jsonb_typeof(allocation_plan) = 'object')
);

CREATE INDEX idx_contribution_payment_requests_status_created ON contribution_payment_requests (status, created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_contribution_payment_requests_status_created;
DROP TABLE IF EXISTS contribution_payment_requests;
DROP TYPE IF EXISTS contribution_payment_request_status;
