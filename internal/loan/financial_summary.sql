-- name: GetLoanFinancialSummary :one
-- One statement keeps balances and status on the same database snapshot.
WITH RECURSIVE repayments AS (
    SELECT COALESCE(SUM(amount), 0)::numeric AS received,
        COALESCE(SUM(COALESCE((allocation_breakdown->>'principal')::numeric,
            amount - COALESCE((allocation_breakdown->>'credit')::numeric, 0))), 0)::numeric AS principal_paid,
        COALESCE(SUM((allocation_breakdown->>'interest')::numeric), 0)::numeric AS interest_paid,
        COALESCE(SUM((allocation_breakdown->>'penalty')::numeric), 0)::numeric AS penalty_paid,
        COALESCE(SUM((allocation_breakdown->>'credit')::numeric), 0)::numeric AS overpayment
    FROM loan_transactions WHERE loan_id = $1 AND type = 'repayment'
), assessed AS (
    SELECT COALESCE(SUM(amount), 0)::numeric AS interest, MAX(period)::date AS latest
    FROM loan_monthly_interest WHERE loan_id = $1
), projected(period, total) AS (
    SELECT (COALESCE(a.latest + INTERVAL '1 month', date_trunc('month', NOW() AT TIME ZONE 'Africa/Nairobi')) - INTERVAL '1 month')::date,
        0::numeric
    FROM assessed a
    UNION ALL
    SELECT (p.period + INTERVAL '1 month')::date,
        p.total + ROUND((GREATEST(l.principal - r.principal_paid, 0)
            + GREATEST(a.interest - r.interest_paid + p.total, 0)) * l.interest_rate, 4)
    FROM projected p CROSS JOIN loans l CROSS JOIN repayments r CROSS JOIN assessed a
    WHERE l.id = $1 AND l.status IN ('active', 'disbursed', 'delinquent')
      AND p.period < date_trunc('month', NOW() AT TIME ZONE 'Africa/Nairobi')::date
), balances AS (
    SELECT l.status, l.previous_status, l.updated_at, l.disbursed_at,
        GREATEST(l.principal - r.principal_paid, 0)::numeric AS principal_outstanding,
        GREATEST(a.interest - r.interest_paid, 0)::numeric AS assessed_interest_outstanding,
        (SELECT total FROM projected ORDER BY period DESC LIMIT 1)::numeric AS unposted_interest,
        GREATEST((SELECT COALESCE(SUM(amount), 0) FROM loan_transactions
            WHERE loan_id = l.id AND type = 'penalty') - r.penalty_paid, 0)::numeric AS penalty_outstanding,
        ROUND((GREATEST(l.principal - r.principal_paid, 0)
            + GREATEST(a.interest - r.interest_paid + (SELECT total FROM projected ORDER BY period DESC LIMIT 1), 0))
            * l.interest_rate, 4)::numeric AS next_month_interest,
        r.received, r.principal_paid, r.interest_paid, r.penalty_paid, r.overpayment
    FROM loans l CROSS JOIN repayments r CROSS JOIN assessed a
    WHERE l.id = $1 AND NOT l.is_deleted
)
SELECT b.*,
    (b.assessed_interest_outstanding + b.unposted_interest)::numeric AS interest_outstanding,
    (b.principal_outstanding + b.assessed_interest_outstanding + b.unposted_interest + b.penalty_outstanding)::numeric AS total_outstanding,
    COALESCE((SELECT SUM(GREATEST(c.amount - (
        SELECT COALESCE(SUM(o.amount), 0) FROM credit_operations o
        WHERE o.credit_balance_id = c.id AND o.status IN ('pending', 'approved', 'completed')
    ), 0)) FROM credit_balances c WHERE c.loan_id = $1 AND c.status = 'available'), 0)::numeric AS available_credit,
    date_trunc('month', NOW() AT TIME ZONE 'Africa/Nairobi')::date AS as_of_period
FROM balances b;

-- name: GetLoanPenaltyDue :one
SELECT GREATEST(
    COALESCE(SUM(amount) FILTER (WHERE type = 'penalty'), 0)
    - COALESCE(SUM((allocation_breakdown->>'penalty')::numeric) FILTER (WHERE type = 'repayment'), 0),
    0)::numeric AS due
FROM loan_transactions WHERE loan_id = $1;
