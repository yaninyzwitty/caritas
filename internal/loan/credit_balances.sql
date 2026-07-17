-- name: CreateCreditBalance :one
INSERT INTO credit_balances (member_id, loan_id, amount, source, status)
VALUES ($1, $2, $3, $4, 'available')
RETURNING id, member_id, loan_id, amount, source, status, created_at, last_activity_at;

-- name: GetCreditBalanceByID :one
SELECT id, member_id, loan_id, amount, source, status, created_at, last_activity_at
FROM credit_balances
WHERE id = $1;

-- name: ListCreditBalancesByMember :many
SELECT id, member_id, loan_id, amount, source, status, created_at, last_activity_at
FROM credit_balances
WHERE member_id = $1
  AND ($2::credit_balance_status IS NULL OR status = $2)
ORDER BY created_at DESC, id DESC
LIMIT $3;

-- name: LockCreditBalanceByID :one
SELECT id, member_id, loan_id, amount, source, status, created_at, last_activity_at
FROM credit_balances
WHERE id = $1
FOR UPDATE;

-- name: UpdateCreditBalanceStatus :one
UPDATE credit_balances
SET status = $2,
    last_activity_at = NOW()
WHERE id = $1
RETURNING id, member_id, loan_id, amount, source, status, created_at, last_activity_at;
