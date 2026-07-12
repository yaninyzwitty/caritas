-- +goose Up
CREATE TYPE share_transaction_type AS ENUM ('purchase', 'withdrawal', 'dividend', 'reversal', 'adjustment');

CREATE TABLE share_transactions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    share_account_id UUID NOT NULL,
    type share_transaction_type NOT NULL,
    amount NUMERIC(19, 4) NOT NULL,
    balance_after NUMERIC(19, 4) NOT NULL,
    reference_id UUID NOT NULL,
    reversal_of UUID,
    reason TEXT,
    originator_id UUID,
    created_at TIMESTAMPZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_share_transactions_account FOREIGN KEY (share_account_id) REFERENCES share_accounts(id) ON DELETE RESTRICT,
    CONSTRAINT chk_share_transactions_balance_non_negative CHECK (balance_after >= 0),
    CONSTRAINT uq_share_transactions_no_double_reversal UNIQUE (reversal_of),
    CONSTRAINT uq_share_transactions_idempotency UNIQUE (share_account_id, reference_id, type),
    CONSTRAINT chk_share_transactions_valid_reversal CHECK (type != 'reversal' OR reversal_of IN (
      SELECT id FROM share_transactions WHERE type != 'reversal'
    )),
    CONSTRAINT chk_share_transactions_no_reverse_reversal CHECK (type != 'reversal' OR NOT EXISTS (
      SELECT 1 FROM share_transactions t2
      WHERE t2.id = share_transactions.reversal_of AND t2.type = 'reversal'
    ))
);

CREATE INDEX idx_share_transactions_account_latest ON share_transactions (share_account_id, created_at DESC);
CREATE INDEX idx_share_transactions_cursor ON share_transactions (created_at DESC, id DESC);
CREATE INDEX idx_share_transactions_reference ON share_transactions (share_account_id, reference_id, type);

-- +goose Down
DROP INDEX IF EXISTS idx_share_transactions_reference;
DROP INDEX IF EXISTS idx_share_transactions_cursor;
DROP INDEX IF EXISTS idx_share_transactions_account_latest;
DROP TABLE IF EXISTS share_transactions;
DROP TYPE IF EXISTS share_transaction_type;