-- +goose Up
CREATE TYPE cashier_session_status AS ENUM ('open', 'closed', 'handed_over', 'deposited');
CREATE TYPE cash_deposit_status AS ENUM ('recorded', 'verified');

CREATE TABLE cashier_sessions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    branch_id BIGINT NOT NULL,
    cashier_id UUID NOT NULL REFERENCES staff_users(id) ON DELETE RESTRICT,
    status cashier_session_status NOT NULL DEFAULT 'open',
    expected_amount NUMERIC(19,4),
    counted_amount NUMERIC(19,4),
    variance NUMERIC(19,4),
    variance_reason TEXT,
    opened_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    closed_at TIMESTAMPTZ,
    closed_by UUID REFERENCES staff_users(id) ON DELETE RESTRICT,
    handed_over_at TIMESTAMPTZ,
    handed_over_to UUID REFERENCES staff_users(id) ON DELETE RESTRICT,
    deposited_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT chk_cashier_sessions_expected_nonnegative CHECK (expected_amount IS NULL OR expected_amount >= 0),
    CONSTRAINT chk_cashier_sessions_counted_nonnegative CHECK (counted_amount IS NULL OR counted_amount >= 0),
    CONSTRAINT chk_cashier_sessions_variance_reason CHECK (
        variance IS NULL OR variance = 0 OR (variance_reason IS NOT NULL AND btrim(variance_reason) <> '')
    )
);

CREATE UNIQUE INDEX idx_cashier_sessions_one_open
    ON cashier_sessions (cashier_id)
    WHERE status = 'open';

ALTER TABLE contribution_receipts
    ADD COLUMN internal_receipt_reference TEXT,
    ADD COLUMN idempotency_key TEXT,
    ADD COLUMN cashier_session_id UUID REFERENCES cashier_sessions(id) ON DELETE RESTRICT,
    ADD CONSTRAINT contribution_receipts_internal_reference_key UNIQUE (internal_receipt_reference),
    ADD CONSTRAINT contribution_receipts_idempotency_key_key UNIQUE (idempotency_key),
    ADD CONSTRAINT chk_contribution_receipts_internal_reference_not_blank
        CHECK (internal_receipt_reference IS NULL OR btrim(internal_receipt_reference) <> ''),
    ADD CONSTRAINT chk_contribution_receipts_idempotency_not_blank
        CHECK (idempotency_key IS NULL OR btrim(idempotency_key) <> ''),
    ADD CONSTRAINT chk_contribution_receipts_cash_custody
        CHECK (
            source_channel <> 'cash'
            OR (
                internal_receipt_reference IS NOT NULL
                AND idempotency_key IS NOT NULL
                AND cashier_session_id IS NOT NULL
                AND received_by IS NOT NULL
                AND external_transaction_id IS NULL
                AND checkout_request_id IS NULL
            )
        );

CREATE INDEX idx_contribution_receipts_cashier_session
    ON contribution_receipts (cashier_session_id, created_at, id)
    WHERE source_channel = 'cash' AND is_deleted = FALSE;

CREATE TABLE cash_deposits (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    branch_id BIGINT NOT NULL,
    amount NUMERIC(19,4) NOT NULL CHECK (amount > 0),
    bank_reference TEXT NOT NULL UNIQUE CHECK (btrim(bank_reference) <> ''),
    status cash_deposit_status NOT NULL DEFAULT 'recorded',
    recorded_by UUID NOT NULL REFERENCES staff_users(id) ON DELETE RESTRICT,
    recorded_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    verified_by UUID REFERENCES staff_users(id) ON DELETE RESTRICT,
    verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE cash_deposit_sessions (
    deposit_id UUID NOT NULL REFERENCES cash_deposits(id) ON DELETE RESTRICT,
    session_id UUID NOT NULL UNIQUE REFERENCES cashier_sessions(id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (deposit_id, session_id)
);

-- +goose Down
DROP TABLE IF EXISTS cash_deposit_sessions;
DROP TABLE IF EXISTS cash_deposits;
DROP INDEX IF EXISTS idx_contribution_receipts_cashier_session;
ALTER TABLE contribution_receipts
    DROP CONSTRAINT IF EXISTS chk_contribution_receipts_cash_custody,
    DROP CONSTRAINT IF EXISTS chk_contribution_receipts_idempotency_not_blank,
    DROP CONSTRAINT IF EXISTS chk_contribution_receipts_internal_reference_not_blank,
    DROP CONSTRAINT IF EXISTS contribution_receipts_idempotency_key_key,
    DROP CONSTRAINT IF EXISTS contribution_receipts_internal_reference_key,
    DROP COLUMN IF EXISTS cashier_session_id,
    DROP COLUMN IF EXISTS idempotency_key,
    DROP COLUMN IF EXISTS internal_receipt_reference;
DROP INDEX IF EXISTS idx_cashier_sessions_one_open;
DROP TABLE IF EXISTS cashier_sessions;
DROP TYPE IF EXISTS cash_deposit_status;
DROP TYPE IF EXISTS cashier_session_status;
