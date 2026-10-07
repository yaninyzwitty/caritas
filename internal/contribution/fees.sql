-- name: AssessMonthlyFees :exec
INSERT INTO contribution_monthly_fees (member_id, period, type)
SELECT m.id, sqlc.arg(period)::date, fee.type
FROM members m
CROSS JOIN (VALUES ('com'::contribution_allocation_type), ('lgom'::contribution_allocation_type)) fee(type)
WHERE m.id = sqlc.arg(member_id) AND NOT m.is_deleted
  AND m.created_at < sqlc.arg(period)::date + INTERVAL '1 month'
  AND COALESCE(
      (SELECT h.to_status FROM member_status_history h
       WHERE h.member_id = m.id AND h.created_at <= (sqlc.arg(period)::timestamp AT TIME ZONE 'Africa/Nairobi')
       ORDER BY h.created_at DESC, h.id DESC LIMIT 1),
      (SELECT h.from_status FROM member_status_history h
       WHERE h.member_id = m.id AND h.created_at > (sqlc.arg(period)::timestamp AT TIME ZONE 'Africa/Nairobi')
       ORDER BY h.created_at, h.id LIMIT 1), m.status) = 'active'
ON CONFLICT DO NOTHING;

-- name: LockMonthlyFees :many
SELECT * FROM contribution_monthly_fees
WHERE member_id = $1 AND period = $2
ORDER BY type
FOR UPDATE;

-- name: SumMonthlyFeePayments :one
SELECT COALESCE(SUM(a.amount), 0)::numeric AS paid
FROM contribution_allocations a
JOIN contribution_receipts r ON r.id = a.receipt_id
WHERE r.member_id = sqlc.arg(member_id)
  AND r.contribution_period >= sqlc.arg(period)::date
  AND r.contribution_period < sqlc.arg(period)::date + INTERVAL '1 month'
  AND a.type = sqlc.arg(type) AND a.status = 'completed'
  AND (sqlc.narg(exclude_receipt)::uuid IS NULL OR r.id <> sqlc.narg(exclude_receipt));
