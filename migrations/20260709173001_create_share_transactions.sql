-- +goose Up
CREATE TYPE share_transaction_type AS ENUM ('purchase', 'withdrawal', 'dividend', 'reversal', 'adjustment');

CREATE TABLE share_transactions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),

    share_account_id UUID NOT NULL,
    type share_transaction_type NOT NULL,

    amount NUMERIC(19,4) NOT NULL CHECK (amount > 0),
    balance_after NUMERIC(19,4) NOT NULL CHECK (balance_after >= 0),

    reference_id UUID NOT NULL,
    reversal_of UUID,

    reason TEXT,
    originator_id UUID,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    FOREIGN KEY (share_account_id)
        REFERENCES share_accounts(id)
        ON DELETE RESTRICT,

    FOREIGN KEY (reversal_of)
        REFERENCES share_transactions(id)
        ON DELETE RESTRICT,

    UNIQUE (reversal_of),

    UNIQUE (share_account_id, reference_id, type)
);

-- TODO-check if redundant
CREATE INDEX idx_share_transactions_account_latest ON share_transactions (share_account_id, created_at DESC);
CREATE INDEX idx_share_transactions_cursor ON share_transactions (created_at DESC, id DESC);
CREATE INDEX idx_share_transactions_reference ON share_transactions (share_account_id, reference_id, type);

-- +goose Down
DROP INDEX IF EXISTS idx_share_transactions_reference;
DROP INDEX IF EXISTS idx_share_transactions_cursor;
DROP INDEX IF EXISTS idx_share_transactions_account_latest;
DROP TABLE IF EXISTS share_transactions;
DROP TYPE IF EXISTS share_transaction_type;