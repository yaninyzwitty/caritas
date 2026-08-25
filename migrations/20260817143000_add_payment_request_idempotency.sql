-- +goose Up
ALTER TABLE contribution_payment_requests
    ADD COLUMN idempotency_key TEXT;

UPDATE contribution_payment_requests
SET idempotency_key = checkout_request_id
WHERE idempotency_key IS NULL;

ALTER TABLE contribution_payment_requests
    ALTER COLUMN idempotency_key SET NOT NULL,
    ALTER COLUMN checkout_request_id DROP NOT NULL,
    ADD CONSTRAINT contribution_payment_requests_idempotency_key_key UNIQUE (idempotency_key),
    ADD CONSTRAINT chk_contribution_payment_requests_idempotency_not_blank CHECK (btrim(idempotency_key) <> '');

-- +goose Down
ALTER TABLE contribution_payment_requests
    DROP CONSTRAINT IF EXISTS chk_contribution_payment_requests_idempotency_not_blank,
    DROP CONSTRAINT IF EXISTS contribution_payment_requests_idempotency_key_key,
    ALTER COLUMN checkout_request_id SET NOT NULL,
    DROP COLUMN IF EXISTS idempotency_key;
