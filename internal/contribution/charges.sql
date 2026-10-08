-- name: CreateContributionCharge :one
INSERT INTO contribution_charges (idempotency_key, member_id, branch_id, category, amount, reason, created_by)
SELECT sqlc.arg(idempotency_key), m.id, m.branch_id, sqlc.arg(category), sqlc.arg(amount), sqlc.arg(reason), sqlc.arg(created_by)
FROM members m
WHERE m.id = sqlc.arg(member_id) AND m.branch_id = sqlc.arg(branch_id) AND NOT m.is_deleted
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetContributionChargeByKey :one
SELECT * FROM contribution_charges WHERE idempotency_key = $1;

-- name: LockContributionCharges :many
SELECT * FROM contribution_charges WHERE id = ANY(sqlc.arg(ids)::uuid[])
ORDER BY id FOR UPDATE;

-- name: SumContributionChargePayments :one
SELECT COALESCE(SUM(amount), 0)::numeric AS paid
FROM contribution_allocations
WHERE target_id = sqlc.arg(charge_id) AND type IN ('other_charge', 'penalty') AND status = 'completed'
  AND (sqlc.narg(exclude_receipt)::uuid IS NULL OR receipt_id <> sqlc.narg(exclude_receipt));

-- name: ListContributionCharges :many
SELECT c.*, COALESCE(p.paid, 0)::numeric AS paid
FROM contribution_charges c
LEFT JOIN LATERAL (
    SELECT SUM(a.amount) AS paid FROM contribution_allocations a
    WHERE a.target_id = c.id AND a.type IN ('other_charge', 'penalty') AND a.status = 'completed'
) p ON true
WHERE c.member_id = sqlc.arg(member_id) AND c.branch_id = sqlc.arg(branch_id)
  AND (sqlc.narg(cursor_created_at)::timestamptz IS NULL
       OR (c.created_at, c.id) < (sqlc.narg(cursor_created_at)::timestamptz, sqlc.narg(cursor_id)::uuid))
ORDER BY c.created_at DESC, c.id DESC
LIMIT sqlc.arg(fetch_limit);
