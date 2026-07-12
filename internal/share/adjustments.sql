-- name: GetAdjustmentByTransactionID :one
SELECT id, share_transaction_id, approver_id, reason, audit_report_id, created_at FROM share_adjustments
WHERE share_transaction_id = $1;

-- name: InsertAdjustment :one
INSERT INTO share_adjustments (share_transaction_id, approver_id, reason, audit_report_id)
VALUES ($1, $2, $3, $4)
RETURNING id, share_transaction_id, approver_id, reason, audit_report_id, created_at;