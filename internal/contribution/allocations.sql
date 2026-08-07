-- name: InsertContributionAllocation :one
INSERT INTO contribution_allocations (
    receipt_id,
    type,
    target_id,
    amount,
    status
) VALUES (
    $1, $2, $3, $4, 'pending'
)
ON CONFLICT DO NOTHING
RETURNING id, receipt_id, type, target_id, amount, status, authoritative_reference_id, external_reference, failure_reason, created_at, updated_at;

-- name: GetContributionAllocationByID :one
SELECT id, receipt_id, type, target_id, amount, status, authoritative_reference_id, external_reference, failure_reason, created_at, updated_at
FROM contribution_allocations
WHERE id = $1;

-- name: ListContributionAllocationsByReceipt :many
SELECT id, receipt_id, type, target_id, amount, status, authoritative_reference_id, external_reference, failure_reason, created_at, updated_at
FROM contribution_allocations
WHERE receipt_id = $1
ORDER BY created_at ASC, id ASC;

-- name: LockContributionAllocationsByReceipt :many
SELECT id, receipt_id, type, target_id, amount, status, authoritative_reference_id, external_reference, failure_reason, created_at, updated_at
FROM contribution_allocations
WHERE receipt_id = $1
ORDER BY created_at ASC, id ASC
FOR UPDATE;

-- name: UpdateContributionAllocationCompleted :one
UPDATE contribution_allocations
SET status = 'completed',
    authoritative_reference_id = $2,
    external_reference = $3,
    failure_reason = NULL,
    updated_at = NOW()
WHERE id = $1
RETURNING id, receipt_id, type, target_id, amount, status, authoritative_reference_id, external_reference, failure_reason, created_at, updated_at;

-- name: UpdateContributionAllocationFailed :one
UPDATE contribution_allocations
SET status = 'failed',
    failure_reason = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING id, receipt_id, type, target_id, amount, status, authoritative_reference_id, external_reference, failure_reason, created_at, updated_at;
