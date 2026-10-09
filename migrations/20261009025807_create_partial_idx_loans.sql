-- +goose Up

CREATE INDEX idx_loans_member_created
ON loans (member_id, created_at DESC, id DESC)
WHERE is_deleted = FALSE;

-- +goose Down

DROP INDEX IF EXISTS idx_loans_member_created;