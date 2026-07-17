-- name: InsertLoanAuditTrail :one
INSERT INTO loan_audit_trails (loan_id, field_changed, previous_value, new_value, changed_by, change_reason, approval_reference)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, loan_id, field_changed, previous_value, new_value, changed_by, change_reason, approval_reference, created_at;

-- name: ListLoanAuditTrail :many
SELECT id, loan_id, field_changed, previous_value, new_value, changed_by, change_reason, approval_reference, created_at
FROM loan_audit_trails
WHERE loan_id = $1
  AND ($2::timestamptz IS NULL OR created_at < $2 OR (created_at = $2 AND id < $3))
ORDER BY created_at DESC, id DESC
LIMIT $4;
