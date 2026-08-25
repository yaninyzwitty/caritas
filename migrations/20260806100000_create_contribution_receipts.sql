-- +goose Up
CREATE TYPE contribution_source_channel AS ENUM ('daraja_stk', 'cash', 'manual');
CREATE TYPE contribution_receipt_status AS ENUM ('pending', 'processing', 'completed', 'failed', 'manual_review');
CREATE TYPE contribution_allocation_type AS ENUM ('com', 'lgom', 'share_purchase', 'loan_principal', 'loan_interest', 'penalty', 'other_charge', 'overpayment_credit');
CREATE TYPE contribution_allocation_status AS ENUM ('pending', 'completed', 'failed');

CREATE TABLE contribution_receipts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    source_channel contribution_source_channel NOT NULL,
    external_transaction_id TEXT,
    checkout_request_id TEXT,
    member_id UUID NOT NULL,
    branch_id BIGINT NOT NULL,
    contribution_period DATE NOT NULL,
    received_amount NUMERIC(19,4) NOT NULL,
    allocation_plan JSONB NOT NULL,
    status contribution_receipt_status NOT NULL DEFAULT 'pending',
    failure_reason TEXT,
    received_by UUID,
    received_at TIMESTAMPTZ NOT NULL,
    is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    deleted_by UUID,
    deletion_reason TEXT,

    CONSTRAINT fk_contribution_receipts_member FOREIGN KEY (member_id) REFERENCES members(id) ON DELETE RESTRICT,
    CONSTRAINT chk_contribution_receipts_amount_positive CHECK (received_amount > 0),
    CONSTRAINT chk_contribution_receipts_plan_object CHECK (jsonb_typeof(allocation_plan) = 'object'),
    CONSTRAINT chk_contribution_receipts_external_not_blank CHECK (external_transaction_id IS NULL OR btrim(external_transaction_id) <> ''),
    CONSTRAINT chk_contribution_receipts_checkout_not_blank CHECK (checkout_request_id IS NULL OR btrim(checkout_request_id) <> ''),
    CONSTRAINT contribution_receipts_external_transaction_id_key UNIQUE (external_transaction_id),
    CONSTRAINT contribution_receipts_checkout_request_id_key UNIQUE (checkout_request_id)
);

CREATE TABLE contribution_allocations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    receipt_id UUID NOT NULL,
    type contribution_allocation_type NOT NULL,
    target_id UUID,
    amount NUMERIC(19,4) NOT NULL,
    status contribution_allocation_status NOT NULL DEFAULT 'pending',
    authoritative_reference_id UUID,
    external_reference TEXT,
    failure_reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_contribution_allocations_receipt FOREIGN KEY (receipt_id) REFERENCES contribution_receipts(id) ON DELETE RESTRICT,
    CONSTRAINT chk_contribution_allocations_amount_positive CHECK (amount > 0),
    CONSTRAINT chk_contribution_allocations_external_not_blank CHECK (external_reference IS NULL OR btrim(external_reference) <> '')
);

CREATE INDEX idx_contribution_receipts_member_period ON contribution_receipts (member_id, contribution_period DESC) WHERE is_deleted = FALSE;
CREATE INDEX idx_contribution_receipts_status_created ON contribution_receipts (status, created_at DESC, id DESC) WHERE is_deleted = FALSE;
CREATE INDEX idx_contribution_receipts_cursor ON contribution_receipts (created_at DESC, id DESC) WHERE is_deleted = FALSE;
CREATE INDEX idx_contribution_allocations_receipt ON contribution_allocations (receipt_id, created_at ASC, id ASC);
CREATE UNIQUE INDEX idx_contribution_allocations_target ON contribution_allocations (receipt_id, type, target_id) WHERE target_id IS NOT NULL;
CREATE UNIQUE INDEX idx_contribution_allocations_no_target ON contribution_allocations (receipt_id, type) WHERE target_id IS NULL;

-- +goose Down
DROP INDEX IF EXISTS idx_contribution_allocations_no_target;
DROP INDEX IF EXISTS idx_contribution_allocations_target;
DROP INDEX IF EXISTS idx_contribution_allocations_receipt;
DROP INDEX IF EXISTS idx_contribution_receipts_cursor;
DROP INDEX IF EXISTS idx_contribution_receipts_status_created;
DROP INDEX IF EXISTS idx_contribution_receipts_member_period;
DROP TABLE IF EXISTS contribution_allocations;
DROP TABLE IF EXISTS contribution_receipts;
DROP TYPE IF EXISTS contribution_allocation_status;
DROP TYPE IF EXISTS contribution_allocation_type;
DROP TYPE IF EXISTS contribution_receipt_status;
DROP TYPE IF EXISTS contribution_source_channel;
