-- +goose Up
CREATE TABLE share_adjustments (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    share_transaction_id UUID NOT NULL,
    approver_id UUID NOT NULL,
    reason TEXT NOT NULL,
    audit_report_id UUID,
    created_at TIMESTAMPZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_share_adjustments_transaction FOREIGN KEY (share_transaction_id) REFERENCES share_transactions(id) ON DELETE RESTRICT
);

CREATE INDEX idx_share_adjustments_transaction ON share_adjustments (share_transaction_id);

-- +goose Down
DROP INDEX IF EXISTS idx_share_adjustments_transaction;
DROP TABLE IF EXISTS share_adjustments;