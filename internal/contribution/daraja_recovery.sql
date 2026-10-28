-- name: InsertDarajaCallback :exec
INSERT INTO daraja_callbacks (checkout_request_id, payload, payload_hash)
VALUES ($1, $2, $3) ON CONFLICT DO NOTHING;

-- name: ClaimDarajaCallback :one
UPDATE daraja_callbacks SET attempts = attempts + 1, next_attempt_at = NOW() + INTERVAL '2 minutes'
WHERE id = (SELECT id FROM daraja_callbacks WHERE processed_at IS NULL AND attempts < 8
    AND next_attempt_at <= NOW() ORDER BY next_attempt_at, id FOR UPDATE SKIP LOCKED LIMIT 1)
RETURNING *;

-- name: FinishDarajaCallback :exec
UPDATE daraja_callbacks SET processed_at = CASE WHEN sqlc.arg(last_error)::text = '' THEN NOW() ELSE NULL END,
    last_error = NULLIF(sqlc.arg(last_error), ''),
    next_attempt_at = NOW() + (LEAST(1800, 15 * (1 << LEAST(attempts, 7))) * INTERVAL '1 second')
WHERE id = sqlc.arg(id);

-- name: ClaimDarajaQuery :one
UPDATE contribution_payment_requests SET query_attempts = query_attempts + 1,
    next_query_at = NOW() + (LEAST(1800, 30 * (1 << LEAST(query_attempts, 6))) * INTERVAL '1 second')
WHERE id = (SELECT id FROM contribution_payment_requests
    WHERE status = 'pending' AND checkout_request_id IS NOT NULL AND query_attempts < 8
    AND next_query_at <= NOW() AND recovery_status NOT IN ('received_needs_receipt', 'received_needs_review')
    ORDER BY next_query_at, id FOR UPDATE SKIP LOCKED LIMIT 1)
RETURNING *;

-- name: RecordDarajaQueryResult :exec
UPDATE contribution_payment_requests SET provider_result = $2,
    recovery_status = $3, updated_at = NOW()
WHERE id = $1 AND status <> 'completed'
    -- A late query must not erase callback evidence.
    AND (recovery_status NOT LIKE 'received%' OR $3 = 'received_needs_review')
    AND (status = 'pending' OR $3 IN ('received_needs_review', 'received_needs_receipt', 'provider_failed'));

-- name: ScheduleDarajaQuery :exec
UPDATE contribution_payment_requests SET next_query_at = NOW(), query_attempts = 0, updated_at = NOW()
WHERE id = $1 AND branch_id = $2 AND status = 'pending'
    AND (query_attempts >= 8 OR next_query_at > NOW() + INTERVAL '30 seconds')
    AND updated_at < NOW() - INTERVAL '30 seconds';

-- name: RestoreDarajaCheckout :exec
UPDATE contribution_payment_requests
SET checkout_request_id = $2, next_query_at = NOW(), query_attempts = 0,
    recovery_status = 'awaiting_confirmation', updated_at = NOW()
WHERE id = $1 AND status = 'pending' AND checkout_request_id IS NULL AND receipt_id IS NULL;

-- name: ListUnresolvedDarajaPayments :many
SELECT p.*, COALESCE(r.status::text, '')::text AS receipt_status,
    COALESCE(r.external_transaction_id, '')::text AS mpesa_reference,
    COALESCE(r.failure_reason, callback.last_error, '')::text AS posting_error
FROM contribution_payment_requests p
LEFT JOIN contribution_receipts r ON r.id = p.receipt_id
LEFT JOIN LATERAL (SELECT last_error FROM daraja_callbacks c WHERE c.checkout_request_id = p.checkout_request_id
    AND c.processed_at IS NULL ORDER BY c.created_at DESC, c.id DESC LIMIT 1) callback ON TRUE
WHERE p.branch_id = sqlc.arg(branch_id)
    AND (p.status <> 'completed' OR r.status <> 'completed')
    AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
        OR (p.created_at, p.id) < (sqlc.narg(cursor_created_at), sqlc.narg(cursor_id)::uuid))
ORDER BY p.created_at DESC, p.id DESC LIMIT 51;

-- name: LockDarajaRequest :one
SELECT * FROM contribution_payment_requests WHERE id = $1 AND branch_id = $2 FOR UPDATE;

-- name: InsertPaymentReconciliation :one
INSERT INTO payment_reconciliations
    (payment_request_id, external_transaction_id, amount, received_at, evidence, reason, reconciled_by)
VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING RETURNING *;

-- name: GetPaymentReconciliation :one
SELECT * FROM payment_reconciliations WHERE external_transaction_id = $1;

-- name: RetryDarajaReceipt :exec
UPDATE contribution_receipts SET status = 'pending', failure_reason = NULL, updated_at = NOW()
WHERE id = $1 AND branch_id = $2 AND source_channel = 'daraja_stk'
    AND status IN ('failed', 'manual_review');

-- name: InsertPaymentRecoveryAction :exec
INSERT INTO payment_recovery_actions (payment_request_id, action, reason, performed_by)
VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING;

-- name: RequeueDarajaCallbacks :exec
UPDATE daraja_callbacks SET attempts = 0, next_attempt_at = NOW()
WHERE checkout_request_id = $1 AND processed_at IS NULL AND attempts >= 8;
