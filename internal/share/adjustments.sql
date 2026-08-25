-- name: GetAdjustmentByTransactionID :one
SELECT id, share_transaction_id, approver_id, reason, audit_report_id, created_at, share_account_id, amount, reference_id, requested_by, status, posted_transaction_id, approved_by, approved_at FROM share_adjustments
WHERE share_transaction_id = $1;

-- name: InsertAdjustment :one
INSERT INTO share_adjustments (share_account_id, amount, reference_id, requested_by, reason)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (share_account_id, reference_id) DO NOTHING
RETURNING id, share_transaction_id, approver_id, reason, audit_report_id, created_at, share_account_id, amount, reference_id, requested_by, status, posted_transaction_id, approved_by, approved_at;

-- name: GetAdjustmentByReference :one
SELECT id, share_transaction_id, approver_id, reason, audit_report_id, created_at, share_account_id, amount, reference_id, requested_by, status, posted_transaction_id, approved_by, approved_at FROM share_adjustments
WHERE share_account_id = $1 AND reference_id = $2;

-- name: LockAdjustmentByID :one
SELECT id, share_transaction_id, approver_id, reason, audit_report_id, created_at, share_account_id, amount, reference_id, requested_by, status, posted_transaction_id, approved_by, approved_at FROM share_adjustments
WHERE id = $1
FOR UPDATE;

-- name: UpdateAdjustmentApproved :one
UPDATE share_adjustments
SET status = 'approved',
    share_transaction_id = $2,
    posted_transaction_id = $2,
    approver_id = $3,
    approved_by = $3,
    reason = $4,
    audit_report_id = $5,
    approved_at = NOW()
WHERE id = $1
RETURNING id, share_transaction_id, approver_id, reason, audit_report_id, created_at, share_account_id, amount, reference_id, requested_by, status, posted_transaction_id, approved_by, approved_at;
