-- name: InsertContributionPaymentRequest :one
INSERT INTO contribution_payment_requests (
    idempotency_key,
    checkout_request_id,
    member_id,
    branch_id,
    contribution_period,
    expected_amount,
    allocation_plan,
    requested_by
) VALUES (
    sqlc.arg(idempotency_key),
    sqlc.narg(checkout_request_id),
    sqlc.arg(member_id),
    sqlc.arg(branch_id),
    sqlc.arg(contribution_period),
    sqlc.arg(expected_amount),
    sqlc.arg(allocation_plan),
    sqlc.arg(requested_by)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetContributionPaymentRequestByIdempotencyKey :one
SELECT *
FROM contribution_payment_requests
WHERE idempotency_key = $1;

-- name: GetContributionPaymentRequestByCheckoutID :one
SELECT *
FROM contribution_payment_requests
WHERE checkout_request_id = $1;

-- name: LockContributionPaymentRequestByCheckoutID :one
SELECT *
FROM contribution_payment_requests
WHERE checkout_request_id = $1
FOR UPDATE;

-- name: UpdateContributionPaymentRequestCheckoutID :one
UPDATE contribution_payment_requests
SET checkout_request_id = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateContributionPaymentRequestCompleted :one
UPDATE contribution_payment_requests
SET status = 'completed',
    receipt_id = $2,
    failure_reason = NULL,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateContributionPaymentRequestFailed :one
UPDATE contribution_payment_requests
SET status = 'failed',
    failure_reason = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING *;
