-- +goose Up
CREATE TYPE share_adjustment_status AS ENUM ('pending', 'approved', 'rejected');

ALTER TABLE share_transactions
    DROP CONSTRAINT share_transactions_amount_check,
    ADD CONSTRAINT share_transactions_amount_check
        CHECK (amount > 0 OR type IN ('reversal', 'adjustment'));

ALTER TABLE share_adjustments
    ADD COLUMN share_account_id UUID,
    ADD COLUMN amount NUMERIC(19,4),
    ADD COLUMN reference_id UUID,
    ADD COLUMN requested_by UUID,
    ADD COLUMN status share_adjustment_status NOT NULL DEFAULT 'pending',
    ADD COLUMN posted_transaction_id UUID,
    ADD COLUMN approved_by UUID,
    ADD COLUMN approved_at TIMESTAMPTZ;

UPDATE share_adjustments a
SET share_account_id = t.share_account_id,
    amount = t.amount,
    reference_id = t.reference_id,
    requested_by = t.originator_id,
    status = 'approved',
    posted_transaction_id = t.id,
    approved_by = a.approver_id,
    approved_at = a.created_at
FROM share_transactions t
WHERE a.share_transaction_id = t.id;

ALTER TABLE share_adjustments
    ALTER COLUMN share_transaction_id DROP NOT NULL,
    ALTER COLUMN approver_id DROP NOT NULL,
    ALTER COLUMN share_account_id SET NOT NULL,
    ALTER COLUMN amount SET NOT NULL,
    ALTER COLUMN reference_id SET NOT NULL;

ALTER TABLE share_adjustments
    ADD CONSTRAINT fk_share_adjustments_account FOREIGN KEY (share_account_id) REFERENCES share_accounts(id) ON DELETE RESTRICT,
    ADD CONSTRAINT fk_share_adjustments_posted_transaction FOREIGN KEY (posted_transaction_id) REFERENCES share_transactions(id) ON DELETE RESTRICT,
    ADD CONSTRAINT share_adjustments_posted_consistency CHECK (
        (status = 'approved' AND posted_transaction_id IS NOT NULL AND approved_by IS NOT NULL AND approved_at IS NOT NULL)
        OR (status <> 'approved' AND posted_transaction_id IS NULL)
    ),
    ADD CONSTRAINT share_adjustments_legacy_transaction_consistency CHECK (
        share_transaction_id IS NULL OR share_transaction_id = posted_transaction_id
    ),
    ADD CONSTRAINT share_adjustments_unique_reference UNIQUE (share_account_id, reference_id);

-- +goose Down
ALTER TABLE share_adjustments
    DROP CONSTRAINT IF EXISTS share_adjustments_unique_reference,
    DROP CONSTRAINT IF EXISTS share_adjustments_legacy_transaction_consistency,
    DROP CONSTRAINT IF EXISTS share_adjustments_posted_consistency,
    DROP CONSTRAINT IF EXISTS fk_share_adjustments_posted_transaction,
    DROP CONSTRAINT IF EXISTS fk_share_adjustments_account;

ALTER TABLE share_adjustments
    ALTER COLUMN share_transaction_id SET NOT NULL,
    ALTER COLUMN approver_id SET NOT NULL,
    DROP COLUMN approved_at,
    DROP COLUMN approved_by,
    DROP COLUMN posted_transaction_id,
    DROP COLUMN status,
    DROP COLUMN requested_by,
    DROP COLUMN reference_id,
    DROP COLUMN amount,
    DROP COLUMN share_account_id;

ALTER TABLE share_transactions
    DROP CONSTRAINT share_transactions_amount_check,
    ADD CONSTRAINT share_transactions_amount_check CHECK (amount > 0);

DROP TYPE IF EXISTS share_adjustment_status;
