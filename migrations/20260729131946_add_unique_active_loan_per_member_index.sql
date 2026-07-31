-- +goose Up

CREATE INDEX idx_loans_member_id
ON loans (member_id);

CREATE UNIQUE INDEX uq_loans_one_open_application_per_member
ON loans (member_id)
WHERE status IN ('pending', 'approved')
  AND is_deleted = FALSE;

-- +goose Down

DROP INDEX IF EXISTS idx_loans_member_id;
DROP INDEX IF EXISTS uq_loans_one_open_application_per_member;

