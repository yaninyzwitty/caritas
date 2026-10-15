-- +goose Up
ALTER TABLE contribution_payment_requests
    ADD COLUMN phone_number TEXT NOT NULL DEFAULT '',
    ADD COLUMN recovery_status TEXT NOT NULL DEFAULT 'awaiting_confirmation',
    ADD COLUMN query_attempts INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN next_query_at TIMESTAMPTZ NOT NULL DEFAULT NOW() + INTERVAL '2 minutes',
    ADD COLUMN provider_result TEXT;
CREATE INDEX contribution_payment_requests_query ON contribution_payment_requests(next_query_at, id)
    WHERE status = 'pending' AND checkout_request_id IS NOT NULL;

CREATE TABLE daraja_callbacks (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    checkout_request_id TEXT NOT NULL,
    payload JSONB NOT NULL,
    payload_hash TEXT NOT NULL UNIQUE,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    processed_at TIMESTAMPTZ,
    last_error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX daraja_callbacks_pending ON daraja_callbacks(next_attempt_at) WHERE processed_at IS NULL;
CREATE INDEX daraja_callbacks_checkout ON daraja_callbacks(checkout_request_id, created_at DESC, id DESC);

CREATE TABLE payment_reconciliations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_request_id UUID NOT NULL UNIQUE REFERENCES contribution_payment_requests(id),
    external_transaction_id TEXT NOT NULL UNIQUE,
    amount NUMERIC(19,4) NOT NULL CHECK (amount > 0),
    received_at TIMESTAMPTZ NOT NULL,
    evidence TEXT NOT NULL CHECK (btrim(evidence) <> ''),
    reason TEXT NOT NULL CHECK (btrim(reason) <> ''),
    reconciled_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE payment_recovery_actions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    payment_request_id UUID NOT NULL REFERENCES contribution_payment_requests(id),
    action TEXT NOT NULL,
    reason TEXT NOT NULL,
    performed_by UUID NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE payment_recovery_actions;
DROP TABLE payment_reconciliations;
DROP TABLE daraja_callbacks;
DROP INDEX contribution_payment_requests_query;
ALTER TABLE contribution_payment_requests DROP COLUMN phone_number, DROP COLUMN recovery_status,
    DROP COLUMN query_attempts, DROP COLUMN next_query_at, DROP COLUMN provider_result;
