-- name: GetLatestBalance :one
SELECT balance_after FROM share_transactions
WHERE share_account_id = $1
ORDER BY created_at DESC
LIMIT 1;

-- name: LockAndGetLatestBalance :one
SELECT balance_after FROM share_transactions
WHERE share_account_id = $1
ORDER BY created_at DESC
LIMIT 1
FOR UPDATE;

-- name: ListTransactions :many
SELECT id, share_account_id, type, amount, balance_after, reference_id, reversal_of, reason, originator_id, created_at FROM share_transactions
WHERE share_account_id = $1
  AND (created_at < $2 OR (created_at = $2 AND id < $3))
ORDER BY created_at DESC, id DESC
LIMIT $4;

-- name: GetTransactionByID :one
SELECT id, share_account_id, type, amount, balance_after, reference_id, reversal_of, reason, originator_id, created_at FROM share_transactions
WHERE id = $1;

-- name: GetTransactionByReference :one
SELECT id, share_account_id, type, amount, balance_after, reference_id, reversal_of, reason, originator_id, created_at FROM share_transactions
WHERE share_account_id = $1 AND reference_id = $2 AND type = $3;

-- name: GetReversalTransactions :many
SELECT id, share_account_id, type, amount, balance_after, reference_id, reversal_of, reason, originator_id, created_at FROM share_transactions
WHERE reversal_of = $1;

-- name: InsertShareTransaction :one
INSERT INTO share_transactions (share_account_id, type, amount, balance_after, reference_id, reversal_of, reason, originator_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (share_account_id, reference_id, type) DO NOTHING
RETURNING id, share_account_id, type, amount, balance_after, reference_id, reversal_of, reason, originator_id, created_at;