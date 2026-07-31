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
ORDER BY created_at DESC, id DESC
LIMIT $2;

-- name: ListCreditBalancesByLoan :many
SELECT id, member_id, loan_id, amount, source, status, created_at, last_activity_at
FROM credit_balances
WHERE loan_id = $1
ORDER BY created_at DESC, id DESC;

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
