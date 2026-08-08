-- name: InsertContributionPaymentRequest :one
INSERT INTO contribution_payment_requests (
    checkout_request_id,
    member_id,
    branch_id,
    contribution_period,
    expected_amount,
    allocation_plan,
    requested_by
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
ON CONFLICT DO NOTHING
RETURNING id, checkout_request_id, member_id, branch_id, contribution_period, expected_amount, allocation_plan, status, receipt_id, failure_reason, requested_by, created_at, updated_at;

-- name: GetContributionPaymentRequestByCheckoutID :one
SELECT id, checkout_request_id, member_id, branch_id, contribution_period, expected_amount, allocation_plan, status, receipt_id, failure_reason, requested_by, created_at, updated_at
FROM contribution_payment_requests
WHERE checkout_request_id = $1;

-- name: LockContributionPaymentRequestByCheckoutID :one
SELECT id, checkout_request_id, member_id, branch_id, contribution_period, expected_amount, allocation_plan, status, receipt_id, failure_reason, requested_by, created_at, updated_at
FROM contribution_payment_requests
WHERE checkout_request_id = $1
FOR UPDATE;

-- name: UpdateContributionPaymentRequestCompleted :one
UPDATE contribution_payment_requests
SET status = 'completed',
    receipt_id = $2,
    failure_reason = NULL,
    updated_at = NOW()
WHERE id = $1
RETURNING id, checkout_request_id, member_id, branch_id, contribution_period, expected_amount, allocation_plan, status, receipt_id, failure_reason, requested_by, created_at, updated_at;

-- name: UpdateContributionPaymentRequestFailed :one
UPDATE contribution_payment_requests
SET status = 'failed',
    failure_reason = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING id, checkout_request_id, member_id, branch_id, contribution_period, expected_amount, allocation_plan, status, receipt_id, failure_reason, requested_by, created_at, updated_at;
