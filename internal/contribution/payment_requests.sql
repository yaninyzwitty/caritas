-- name: InsertContributionPaymentRequest :one
INSERT INTO contribution_payment_requests (
    idempotency_key,
    checkout_request_id,
    member_id,
    branch_id,
    contribution_period,
    expected_amount,
    allocation_plan,
    requested_by,
    phone_number
) VALUES (
    sqlc.arg(idempotency_key),
    sqlc.narg(checkout_request_id),
    sqlc.arg(member_id),
    sqlc.arg(branch_id),
    sqlc.arg(contribution_period),
    sqlc.arg(expected_amount),
    sqlc.arg(allocation_plan),
    sqlc.arg(requested_by),
    sqlc.arg(phone_number)
)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetContributionPaymentRequestByIdempotencyKey :one
SELECT *
FROM contribution_payment_requests
WHERE idempotency_key = $1;

-- name: ClaimUnsentDarajaRequest :one
UPDATE contribution_payment_requests
SET recovery_status = 'awaiting_confirmation', provider_result = NULL, updated_at = NOW()
WHERE id = $1 AND status = 'pending' AND checkout_request_id IS NULL
    AND recovery_status = 'not_sent'
RETURNING *;

-- name: GetContributionPaymentRequestByCheckoutID :one
SELECT *
FROM contribution_payment_requests
WHERE checkout_request_id = $1;

-- name: LookupContributionPaymentRequest :one
SELECT p.id, p.status, p.expected_amount, p.contribution_period,
       p.created_at, p.failure_reason, p.checkout_request_id, p.recovery_status, p.query_attempts, p.provider_result,
       r.id AS receipt_id, COALESCE(r.status::text, '')::text AS receipt_status,
       r.received_amount, r.external_transaction_id, r.received_at, r.loan_snapshot
FROM contribution_payment_requests p
LEFT JOIN contribution_receipts r
  ON r.id = p.receipt_id AND r.branch_id = p.branch_id AND r.is_deleted = FALSE
WHERE p.branch_id = sqlc.arg(branch_id)
  AND (p.id = sqlc.narg(payment_request_id)::uuid
       OR p.idempotency_key = sqlc.narg(idempotency_key)::text);

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
    recovery_status = 'received',
    receipt_id = $2,
    failure_reason = NULL,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: UpdateContributionPaymentRequestFailed :one
UPDATE contribution_payment_requests
SET status = 'failed',
    recovery_status = 'provider_failed',
    failure_reason = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING *;
