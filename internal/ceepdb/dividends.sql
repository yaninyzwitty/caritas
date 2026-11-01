-- name: BuildDividendPreview :one
WITH settings AS (
    SELECT sqlc.arg(branch_id)::bigint AS branch_id, sqlc.arg(financial_year)::integer AS year,
           sqlc.arg(declaration_date)::date AS declared, sqlc.arg(rate)::numeric AS rate
), months AS (
    SELECT generate_series(date_trunc('month', s.declared::timestamp) - INTERVAL '6 months',
           date_trunc('month', s.declared::timestamp) - INTERVAL '1 month', INTERVAL '1 month')::date AS month
    FROM settings s
), qualifying AS (
    SELECT r.member_id, date_trunc('month', r.received_at AT TIME ZONE 'Africa/Nairobi')::date AS month,
           SUM(t.amount) AS amount, jsonb_agg(jsonb_build_object('receiptId', r.id,
               'transactionId', t.id, 'amount', t.amount::text) ORDER BY r.received_at, t.id) AS sources
    FROM settings s
    JOIN contribution_receipts r ON r.branch_id = s.branch_id AND NOT r.is_deleted AND r.status = 'completed'
    JOIN contribution_allocations a ON a.receipt_id = r.id AND a.type = 'share_purchase' AND a.status = 'completed'
    JOIN share_transactions t ON t.id = a.authoritative_reference_id AND t.share_account_id = a.target_id
        AND t.type = 'purchase' AND t.amount = a.amount
    JOIN share_accounts sa ON sa.id = t.share_account_id AND sa.member_id = r.member_id AND sa.branch_id = r.branch_id
    WHERE r.received_at >= ((SELECT MIN(month) FROM months)::timestamp AT TIME ZONE 'Africa/Nairobi')
      AND r.received_at < (date_trunc('month', s.declared::timestamp) AT TIME ZONE 'Africa/Nairobi')
      AND date_trunc('month', r.contribution_period::timestamp)::date = date_trunc('month', r.received_at AT TIME ZONE 'Africa/Nairobi')::date
      AND NOT EXISTS (SELECT 1 FROM share_transactions rev WHERE rev.reversal_of = t.id)
    GROUP BY r.member_id, date_trunc('month', r.received_at AT TIME ZONE 'Africa/Nairobi')::date
), balances AS (
    -- Historical calculation uses signed ledger effects, including corrections;
    -- current balances and tied posting timestamps cannot change the year-end base.
    SELECT m.id, m.member_number, COALESCE(p.full_name, '') AS name, m.status,
           sa.id AS account_id,
           (SELECT COUNT(*) FROM share_accounts x WHERE x.member_id = m.id AND NOT x.is_deleted) AS account_count,
           COALESCE((SELECT SUM(CASE WHEN t.type = 'withdrawal' THEN -t.amount ELSE t.amount END)
               FROM share_transactions t WHERE t.share_account_id = sa.id
               AND t.created_at < (make_date(s.year + 1, 1, 1)::timestamp AT TIME ZONE 'Africa/Nairobi')), 0)::numeric AS balance,
           (SELECT jsonb_agg(jsonb_build_object('month', mo.month, 'amount', COALESCE(q.amount, 0)::text,
               'qualifies', COALESCE(q.amount, 0) >= 100, 'sources', COALESCE(q.sources, '[]'::jsonb)) ORDER BY mo.month)
            FROM months mo LEFT JOIN qualifying q ON q.member_id = m.id AND q.month = mo.month) AS months,
           (SELECT COUNT(*) FROM qualifying q WHERE q.member_id = m.id AND q.amount >= 100) AS qualifying_months
    FROM settings s JOIN members m ON m.branch_id = s.branch_id AND NOT m.is_deleted
    LEFT JOIN member_profiles p ON p.member_id = m.id
    LEFT JOIN LATERAL (SELECT x.id FROM share_accounts x WHERE x.member_id = m.id AND x.branch_id = m.branch_id AND NOT x.is_deleted
        ORDER BY x.created_at, x.id LIMIT 1) sa ON TRUE
), evaluated AS (
    SELECT b.*, b.status = 'active' AND b.account_id IS NOT NULL AND b.account_count = 1 AND b.balance >= 0 AND b.qualifying_months = 6 AS eligible
    FROM balances b
)
SELECT jsonb_build_object('financialYear', s.year, 'declarationDate', s.declared, 'rate', s.rate::text,
    'status', 'draft', 'allocations', COALESCE((SELECT jsonb_agg(jsonb_build_object(
        'memberId', e.id, 'memberNumber', e.member_number::text, 'name', e.name,
        'shareAccountId', e.account_id, 'memberStatus', e.status, 'months', e.months,
        'eligible', e.eligible, 'reason', CASE WHEN e.status <> 'active' THEN 'Member is inactive'
            WHEN e.account_id IS NULL THEN 'No share account' WHEN e.account_count > 1 THEN 'Duplicate share accounts require review'
            WHEN e.balance < 0 THEN 'Negative historical balance requires review'
            WHEN e.qualifying_months <> 6 THEN 'Six qualifying months required' ELSE '' END,
        'shareBalance', e.balance::text, 'amount', CASE WHEN e.eligible THEN round(e.balance * s.rate / 100, 2) ELSE 0 END::text
    ) ORDER BY e.member_number, e.id) FROM evaluated e), '[]'::jsonb),
    'approvedTotal', COALESCE((SELECT SUM(round(e.balance * s.rate / 100, 2)) FROM evaluated e WHERE e.eligible), 0)::text,
    'hasErrors', EXISTS (SELECT 1 FROM evaluated e WHERE e.balance < 0 OR e.account_count > 1))::jsonb AS report
FROM settings s;

-- name: GetDividendRegister :one
SELECT jsonb_build_object('financialYear', d.financial_year, 'declarationDate', d.declaration_date,
    'rate', d.rate::text, 'status', 'approved', 'approvedTotal', d.approved_total::text,
    'resolutionReference', d.resolution_reference, 'approvedBy', d.approved_by, 'approvedAt', d.created_at, 'previewHash', d.preview_hash,
    'allocations', COALESCE((SELECT jsonb_agg(a.evidence || jsonb_build_object(
        'id', a.id, 'choice', a.choice, 'choiceBy', a.choice_by, 'choiceAt', a.choice_at,
        'voucherReference', a.voucher_reference, 'paymentDate', a.payment_date,
        'postedTransactionId', a.posted_transaction_id, 'settledBy', a.settled_by, 'settledAt', a.settled_at
    ) ORDER BY (a.evidence->>'memberNumber')::bigint, a.member_id)
    FROM dividend_allocations a WHERE a.declaration_id = d.id), '[]'::jsonb),
    'cashPaid', COALESCE((SELECT SUM(a.amount) FROM dividend_allocations a WHERE a.declaration_id = d.id AND a.settled_at IS NOT NULL AND a.choice = 'cash'), 0)::text,
    'reinvested', COALESCE((SELECT SUM(a.amount) FROM dividend_allocations a WHERE a.declaration_id = d.id AND a.settled_at IS NOT NULL AND a.choice = 'shares'), 0)::text,
    'outstanding', COALESCE((SELECT SUM(a.amount) FROM dividend_allocations a WHERE a.declaration_id = d.id AND a.settled_at IS NULL), 0)::text
)::jsonb AS report FROM dividend_declarations d WHERE d.branch_id = $1 AND d.financial_year = $2;

-- name: SaveDividendDeclaration :one
INSERT INTO dividend_declarations (branch_id, financial_year, declaration_date, rate, approved_total, resolution_reference, preview_hash, approved_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
ON CONFLICT (branch_id, financial_year) DO NOTHING RETURNING id;

-- name: SaveDividendAllocations :exec
INSERT INTO dividend_allocations (declaration_id, branch_id, member_id, share_account_id, evidence, eligible, share_balance, amount)
SELECT d.id, d.branch_id, (e->>'memberId')::uuid, (e->>'shareAccountId')::uuid, e,
       (e->>'eligible')::boolean, (e->>'shareBalance')::numeric, (e->>'amount')::numeric
FROM dividend_declarations d CROSS JOIN jsonb_array_elements(sqlc.arg(allocations)::jsonb) e
WHERE d.id = sqlc.arg(declaration_id);

-- name: LockDividendAllocation :one
SELECT a.*, d.financial_year, d.branch_id, d.declaration_date
FROM dividend_allocations a JOIN dividend_declarations d ON d.id = a.declaration_id
WHERE a.id = $1 AND d.branch_id = $2 FOR UPDATE OF a;

-- name: RecordDividendChoice :exec
UPDATE dividend_allocations SET choice = $2, choice_by = $3, choice_at = NOW()
WHERE id = $1 AND settled_at IS NULL AND choice IS NULL;

-- name: SettleDividendAllocation :exec
UPDATE dividend_allocations SET voucher_reference = $2, payment_date = $3, posted_transaction_id = $4,
    settled_by = $5, settled_at = NOW()
WHERE id = $1 AND settled_at IS NULL;
