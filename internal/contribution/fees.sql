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

-- name: GetContributionMemberBranch :one
SELECT branch_id FROM members WHERE id = $1 AND NOT is_deleted;

-- Read-only mirror of AssessMonthlyFees eligibility and the schema default (KES 30).
-- Keep them together when changing fee policy, or the cashier quote and posting will disagree.
-- name: QuoteMonthlyFees :many
WITH eligible AS (
 SELECT m.id FROM members m WHERE m.id = sqlc.arg(member_id) AND NOT m.is_deleted
 AND m.created_at < sqlc.arg(period)::date + INTERVAL '1 month'
  AND COALESCE(
      (SELECT h.to_status FROM member_status_history h
       WHERE h.member_id = m.id AND h.created_at <= (sqlc.arg(period)::timestamp AT TIME ZONE 'Africa/Nairobi')
       ORDER BY h.created_at DESC, h.id DESC LIMIT 1),
      (SELECT h.from_status FROM member_status_history h
       WHERE h.member_id = m.id AND h.created_at > (sqlc.arg(period)::timestamp AT TIME ZONE 'Africa/Nairobi')
       ORDER BY h.created_at, h.id LIMIT 1), m.status) = 'active'
), fee_types AS (SELECT 'com'::contribution_allocation_type AS type UNION ALL SELECT 'lgom'::contribution_allocation_type), assessments AS (
 SELECT fee.type, COALESCE(f.amount, CASE WHEN e.id IS NOT NULL THEN 30 ELSE 0 END)::numeric AS amount
 FROM fee_types fee
 LEFT JOIN eligible e ON true
 LEFT JOIN contribution_monthly_fees f
   ON f.member_id = sqlc.arg(member_id) AND f.period = sqlc.arg(period)::date AND f.type = fee.type
)
SELECT x.type, GREATEST(x.amount - COALESCE(p.paid, 0), 0)::numeric AS outstanding
FROM assessments x
LEFT JOIN LATERAL (
 SELECT SUM(a.amount) AS paid FROM contribution_allocations a
 JOIN contribution_receipts r ON r.id = a.receipt_id
 WHERE r.member_id = sqlc.arg(member_id) AND r.contribution_period >= sqlc.arg(period)::date
   AND r.contribution_period < sqlc.arg(period)::date + INTERVAL '1 month'
   AND a.type = x.type AND a.status = 'completed'
) p ON true ORDER BY x.type;
