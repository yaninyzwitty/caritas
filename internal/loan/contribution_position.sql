-- name: LockMemberContributionLoans :many
SELECT id FROM loans
WHERE member_id = $1 AND branch_id = $2 AND NOT is_deleted
  AND status IN ('active', 'disbursed', 'delinquent')
ORDER BY id FOR UPDATE;

-- name: ListContributionLoanPositions :many
WITH positions AS (
    SELECT l.id, l.principal, COALESCE(a.period_rate, l.interest_rate)::numeric AS interest_rate, l.disbursed_at,
        COALESCE(a.latest, (sqlc.arg(period)::date - INTERVAL '1 month')::date) AS latest_assessment,
        r.latest AS latest_payment,
        GREATEST(l.principal - r.principal_paid, 0)::numeric AS principal_due,
        COALESCE(a.opening_balance, GREATEST(l.principal - r.prior_principal_paid, 0))::numeric AS opening_principal,
        r.interest_paid,
        GREATEST(p.amount - r.penalty_paid, 0)::numeric AS penalty_due,
        a.assessed, a.earlier_assessed,
        COALESCE((SELECT amount FROM loan_monthly_interest WHERE loan_id = l.id AND period = sqlc.arg(period)::date),
            ROUND(GREATEST(l.principal - r.prior_principal_paid, 0) * l.interest_rate, 4))::numeric AS period_interest,
        COALESCE((SELECT SUM(ROUND(GREATEST(l.principal - (
            SELECT COALESCE(SUM(COALESCE((t.allocation_breakdown->>'principal')::numeric,
                t.amount - COALESCE((t.allocation_breakdown->>'credit')::numeric, 0))), 0)
            FROM loan_transactions t WHERE t.loan_id = l.id AND t.type = 'repayment'
              AND COALESCE((t.allocation_breakdown->>'period')::date,
                  date_trunc('month', t.created_at AT TIME ZONE 'Africa/Nairobi')::date) < months.period::date
        ), 0) * l.interest_rate, 4))
        FROM generate_series(COALESCE(a.latest + INTERVAL '1 month', sqlc.arg(period)::date),
            sqlc.arg(period)::date, INTERVAL '1 month') AS months(period)), 0)::numeric AS unposted
    FROM loans l
    CROSS JOIN LATERAL (
        SELECT COALESCE(SUM(amount), 0)::numeric AS assessed,
            MAX(previous_balance) FILTER (WHERE period = sqlc.arg(period)::date)::numeric AS opening_balance,
            MAX(rate) FILTER (WHERE period = sqlc.arg(period)::date)::numeric AS period_rate,
            COALESCE(SUM(amount) FILTER (WHERE period < sqlc.arg(period)::date), 0)::numeric AS earlier_assessed,
            MAX(period) AS latest
        FROM loan_monthly_interest WHERE loan_id = l.id
    ) a
    CROSS JOIN LATERAL (
        SELECT COALESCE(SUM(COALESCE((allocation_breakdown->>'principal')::numeric,
                amount - COALESCE((allocation_breakdown->>'credit')::numeric, 0))), 0)::numeric AS principal_paid,
            COALESCE(SUM(COALESCE((allocation_breakdown->>'principal')::numeric,
                amount - COALESCE((allocation_breakdown->>'credit')::numeric, 0))) FILTER (
                WHERE COALESCE((allocation_breakdown->>'period')::date,
                    date_trunc('month', created_at AT TIME ZONE 'Africa/Nairobi')::date) < sqlc.arg(period)::date), 0)::numeric AS prior_principal_paid,
            COALESCE(SUM((allocation_breakdown->>'interest')::numeric), 0)::numeric AS interest_paid,
            COALESCE(SUM((allocation_breakdown->>'penalty')::numeric), 0)::numeric AS penalty_paid,
            MAX(COALESCE((allocation_breakdown->>'period')::date,
                date_trunc('month', created_at AT TIME ZONE 'Africa/Nairobi')::date)) AS latest
        FROM loan_transactions WHERE loan_id = l.id AND type = 'repayment'
    ) r
    CROSS JOIN LATERAL (
        SELECT COALESCE(SUM(amount), 0)::numeric AS amount FROM loan_transactions WHERE loan_id = l.id AND type = 'penalty'
    ) p
    WHERE l.member_id = sqlc.arg(member_id) AND l.branch_id = sqlc.arg(branch_id)
      AND NOT l.is_deleted AND l.status IN ('active', 'disbursed', 'delinquent')
)
SELECT id, interest_rate, opening_principal, principal_due, period_interest, penalty_due,
    GREATEST(assessed + unposted - interest_paid, 0)::numeric AS interest_due,
    GREATEST(earlier_assessed + unposted
        - CASE WHEN latest_assessment < sqlc.arg(period)::date THEN period_interest ELSE 0 END
        - interest_paid, 0)::numeric AS earlier_interest_due,
    (latest_assessment <= sqlc.arg(period)::date
        AND (latest_payment IS NULL OR latest_payment <= sqlc.arg(period)::date)
        AND (disbursed_at IS NULL OR date_trunc('month', disbursed_at AT TIME ZONE 'Africa/Nairobi')::date <= sqlc.arg(period)::date))::boolean AS payable
FROM positions ORDER BY id;

-- name: ListContributionLoanRepayments :many
SELECT loan_id, allocation_breakdown, amount
FROM loan_transactions
WHERE type = 'repayment' AND payment_gateway_transaction_id = ANY(sqlc.arg(allocation_ids)::text[]);
