-- +goose Up
CREATE TYPE share_account_status AS ENUM ('active', 'dormant', 'closed');

CREATE TABLE share_accounts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id UUID NOT NULL,
    branch_id BIGINT NOT NULL,
    status share_account_status NOT NULL DEFAULT 'active',
    opened_at TIMESTAMPZ NOT NULL,
    is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPZ NOT NULL DEFAULT NOW(),
    CONSTRAINT fk_share_accounts_member FOREIGN KEY (member_id) REFERENCES members(id) ON DELETE RESTRICT
);

CREATE INDEX idx_share_accounts_member ON share_accounts (member_id, is_deleted) WHERE is_deleted = FALSE;
CREATE INDEX idx_share_accounts_cursor ON share_accounts (created_at DESC, id DESC) WHERE is_deleted = FALSE;

-- +goose Down
DROP INDEX IF EXISTS idx_share_accounts_cursor;
DROP INDEX IF EXISTS idx_share_accounts_member;
DROP TABLE IF EXISTS share_accounts;
DROP TYPE IF EXISTS share_account_status;