-- name: InsertLoanTransaction :one
INSERT INTO loan_transactions (loan_id, type, amount, reference_id, payment_gateway_transaction_id, allocation_breakdown, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT DO NOTHING
RETURNING id, loan_id, type, amount, reference_id, payment_gateway_transaction_id, allocation_breakdown, created_at, created_by;

-- name: GetLoanTransactionByGatewayID :one
SELECT id, loan_id, type, amount, reference_id, payment_gateway_transaction_id, allocation_breakdown, created_at, created_by
FROM loan_transactions
WHERE loan_id = $1 AND payment_gateway_transaction_id = $2;

-- name: GetLoanTransactionByReference :one
SELECT id, loan_id, type, amount, reference_id, payment_gateway_transaction_id, allocation_breakdown, created_at, created_by
FROM loan_transactions
WHERE loan_id = $1 AND reference_id = $2 AND type = $3;

-- name: GetLoanDisbursement :one
SELECT id, loan_id, type, amount, reference_id, payment_gateway_transaction_id, allocation_breakdown, created_at, created_by
FROM loan_transactions
WHERE loan_id = $1 AND type = 'disbursement';

-- name: ListLoanTransactions :many
SELECT id, loan_id, type, amount, reference_id, payment_gateway_transaction_id, allocation_breakdown, created_at, created_by
FROM loan_transactions
WHERE loan_id = $1
  AND ($2::timestamptz IS NULL OR created_at < $2 OR (created_at = $2 AND id < $3))
ORDER BY created_at DESC, id DESC
LIMIT $4;

-- name: SumLoanTransactionsByType :one
SELECT COALESCE(SUM(amount), 0)::numeric AS total
FROM loan_transactions
WHERE loan_id = $1 AND type = $2;
