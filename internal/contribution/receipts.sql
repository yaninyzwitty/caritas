-- name: InsertContributionReceipt :one
INSERT INTO contribution_receipts (
    source_channel,
    external_transaction_id,
    checkout_request_id,
    member_id,
    branch_id,
    contribution_period,
    received_amount,
    allocation_plan,
    status,
    received_by,
    received_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, 'pending', $9, $10
)
ON CONFLICT DO NOTHING
RETURNING id, source_channel, external_transaction_id, checkout_request_id, member_id, branch_id, contribution_period, received_amount, allocation_plan, status, failure_reason, received_by, received_at, is_deleted, created_at, updated_at;

-- name: GetContributionReceiptByID :one
SELECT id, source_channel, external_transaction_id, checkout_request_id, member_id, branch_id, contribution_period, received_amount, allocation_plan, status, failure_reason, received_by, received_at, is_deleted, created_at, updated_at
FROM contribution_receipts
WHERE id = $1 AND is_deleted = FALSE;

-- name: GetContributionReceiptByExternalTransactionID :one
SELECT id, source_channel, external_transaction_id, checkout_request_id, member_id, branch_id, contribution_period, received_amount, allocation_plan, status, failure_reason, received_by, received_at, is_deleted, created_at, updated_at
FROM contribution_receipts
WHERE external_transaction_id = $1 AND is_deleted = FALSE;

-- name: GetContributionReceiptByCheckoutRequestID :one
SELECT id, source_channel, external_transaction_id, checkout_request_id, member_id, branch_id, contribution_period, received_amount, allocation_plan, status, failure_reason, received_by, received_at, is_deleted, created_at, updated_at
FROM contribution_receipts
WHERE checkout_request_id = $1 AND is_deleted = FALSE;

-- name: LockContributionReceiptByID :one
SELECT id, source_channel, external_transaction_id, checkout_request_id, member_id, branch_id, contribution_period, received_amount, allocation_plan, status, failure_reason, received_by, received_at, is_deleted, created_at, updated_at
FROM contribution_receipts
WHERE id = $1 AND is_deleted = FALSE
FOR UPDATE;

-- name: UpdateContributionReceiptStatus :one
UPDATE contribution_receipts
SET status = $2,
    failure_reason = $3,
    updated_at = NOW()
WHERE id = $1 AND is_deleted = FALSE
RETURNING id, source_channel, external_transaction_id, checkout_request_id, member_id, branch_id, contribution_period, received_amount, allocation_plan, status, failure_reason, received_by, received_at, is_deleted, created_at, updated_at;

-- name: ListContributionReceiptsByMember :many
SELECT id, source_channel, external_transaction_id, checkout_request_id, member_id, branch_id, contribution_period, received_amount, allocation_plan, status, failure_reason, received_by, received_at, is_deleted, created_at, updated_at
FROM contribution_receipts
WHERE member_id = $1
  AND is_deleted = FALSE
  AND (
    sqlc.narg('cursor_created_at')::timestamptz IS NULL
    OR created_at < sqlc.narg('cursor_created_at')
    OR (created_at = sqlc.narg('cursor_created_at') AND id < sqlc.narg('cursor_id'))
  )
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg('limit');

