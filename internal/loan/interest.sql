-- name: GetLoanInterestPeriod :one
SELECT MAX(period)::date AS latest_period
FROM loan_monthly_interest WHERE loan_id = $1;

-- name: AssessLoanInterest :exec
INSERT INTO loan_monthly_interest (loan_id, period, previous_balance, rate, amount)
SELECT l.id, sqlc.arg(period)::date,
       GREATEST(l.principal - paid.principal, 0), l.interest_rate,
       ROUND(GREATEST(l.principal - paid.principal, 0) * l.interest_rate, 4)
FROM loans l
CROSS JOIN LATERAL (
    SELECT COALESCE(SUM(COALESCE((t.allocation_breakdown->>'principal')::numeric,
        t.amount - COALESCE((t.allocation_breakdown->>'credit')::numeric, 0))), 0)::numeric AS principal
    FROM loan_transactions t WHERE t.loan_id = l.id AND t.type = 'repayment'
      AND COALESCE((t.allocation_breakdown->>'period')::date,
          date_trunc('month', t.created_at AT TIME ZONE 'Africa/Nairobi')::date) < sqlc.arg(period)::date
) paid
WHERE l.id = sqlc.arg(loan_id)
ON CONFLICT DO NOTHING;

-- name: GetLoanRepaymentTotals :one
SELECT
    COALESCE(SUM(COALESCE((allocation_breakdown->>'principal')::numeric,
        amount - COALESCE((allocation_breakdown->>'credit')::numeric, 0))), 0)::numeric AS principal,
    COALESCE(SUM((allocation_breakdown->>'interest')::numeric), 0)::numeric AS interest,
    MAX(COALESCE((allocation_breakdown->>'period')::date,
        date_trunc('month', created_at AT TIME ZONE 'Africa/Nairobi')::date))::date AS latest_period
FROM loan_transactions WHERE loan_id = $1 AND type = 'repayment';

-- name: SumLoanInterestDue :one
SELECT COALESCE(SUM(amount), 0)::numeric AS total
FROM loan_monthly_interest WHERE loan_id = $1;
