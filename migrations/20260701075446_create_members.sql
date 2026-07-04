-- +goose Up
CREATE TABLE members (
    id BIGSERIAL PRIMARY KEY,
    branch_id BIGINT NOT NULL,
    member_number BIGINT NOT NULL,
    national_id TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'pending',
    is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT members_branch_national_id_key UNIQUE (branch_id, national_id),
    CONSTRAINT members_branch_number_key UNIQUE (branch_id, member_number)
);

CREATE INDEX idx_members_cursor ON members (created_at DESC, id DESC) WHERE is_deleted = FALSE;

-- +goose Down
DROP INDEX IF EXISTS idx_members_cursor;
DROP TABLE IF EXISTS members;