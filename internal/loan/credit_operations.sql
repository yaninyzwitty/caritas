-- name: GetCreditFunds :one
SELECT c.amount,
    COALESCE(SUM(o.amount) FILTER (WHERE o.status IN ('pending','approved')), 0)::numeric AS reserved,
    COALESCE(SUM(o.amount) FILTER (WHERE o.status = 'completed'), 0)::numeric AS used
FROM credit_balances c LEFT JOIN credit_operations o ON o.credit_balance_id = c.id
WHERE c.id = $1 GROUP BY c.id;

-- name: GetCreditMemberBranch :one
SELECT branch_id FROM members WHERE id = $1 AND NOT is_deleted;

-- name: InsertCreditOperation :one
INSERT INTO credit_operations (id, credit_balance_id, amount, action, target_id, contribution_period,
    status, reason, consent_reference, requested_by)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING RETURNING *;

-- name: GetCreditOperation :one
SELECT * FROM credit_operations WHERE id = $1;

-- name: ReviewCreditOperation :one
UPDATE credit_operations SET status = $2, reviewed_by = $3, reviewed_at = NOW(), review_reason = $4
WHERE id = $1 RETURNING *;

-- name: CompleteCreditOperation :one
UPDATE credit_operations SET status = 'completed', completed_by = $2, completed_at = NOW(),
    payout_reference = $3, payout_evidence = $4
WHERE id = $1 RETURNING *;

-- name: ListCreditOperations :many
SELECT * FROM credit_operations WHERE credit_balance_id = sqlc.arg(credit_balance_id)
    AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
        OR (created_at, id) < (sqlc.narg(cursor_created_at), sqlc.narg(cursor_id)::uuid))
ORDER BY created_at DESC, id DESC LIMIT 51;
