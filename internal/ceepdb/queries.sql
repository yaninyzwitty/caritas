-- name: GetCEEPReport :one
SELECT (r.metadata || jsonb_build_object('rows', COALESCE((
    SELECT jsonb_agg(s.data ORDER BY (s.data->>'memberNumber')::bigint, s.member_id)
    FROM ceep_snapshots s WHERE s.report_id = r.id
), '[]'::jsonb)))::jsonb AS report
FROM ceep_reports r WHERE r.branch_id = $1 AND r.period = $2;

-- name: SaveCEEPReport :exec
WITH saved AS (
    INSERT INTO ceep_reports (branch_id, period, metadata, finalized_by)
    VALUES (sqlc.arg(branch_id), sqlc.arg(period), sqlc.arg(report)::jsonb - 'rows', sqlc.arg(officer_id))
    ON CONFLICT DO NOTHING RETURNING id, period
)
INSERT INTO ceep_snapshots (report_id, member_id, period, data)
SELECT saved.id, (row.data->>'memberId')::uuid, saved.period, row.data
FROM saved CROSS JOIN jsonb_array_elements(sqlc.arg(report)::jsonb->'rows') AS row(data);

-- A single statement gives every source the same MVCC snapshot. Money remains
-- decimal text through JSON and exports, avoiding binary floating-point rounding.
-- name: BuildCEEPReport :one
WITH bounds AS (
    SELECT sqlc.arg(branch_id)::bigint AS branch_id, sqlc.arg(period)::date AS period,
        sqlc.arg(cutoff)::timestamptz AS cutoff,
        sqlc.arg(period)::timestamp AT TIME ZONE 'Africa/Nairobi' AS starts,
        (sqlc.arg(period)::date + INTERVAL '1 month') AT TIME ZONE 'Africa/Nairobi' AS ends
), receipts AS (
    SELECT r.*, (r.status = 'completed' AND r.updated_at <= b.cutoff
        AND EXISTS (SELECT 1 FROM contribution_allocations a WHERE a.receipt_id = r.id)
        AND NOT EXISTS (SELECT 1 FROM contribution_allocations a WHERE a.receipt_id = r.id
            AND (a.status <> 'completed' OR a.updated_at > b.cutoff))) AS included
    FROM contribution_receipts r CROSS JOIN bounds b
    WHERE r.branch_id = b.branch_id AND r.contribution_period = b.period AND r.created_at <= b.cutoff
      AND NOT r.is_deleted
), members_in_period AS (
    SELECT m.*, COALESCE(p.full_name, '') AS full_name
    FROM members m LEFT JOIN member_profiles p ON p.member_id = m.id CROSS JOIN bounds b
    WHERE m.branch_id = b.branch_id AND m.created_at < LEAST(b.ends, b.cutoff)
      AND (COALESCE(
          (SELECT h.to_status FROM member_status_history h WHERE h.member_id = m.id AND h.created_at <= b.starts
              ORDER BY h.created_at DESC, h.id DESC LIMIT 1),
          (SELECT h.from_status FROM member_status_history h WHERE h.member_id = m.id AND h.created_at > b.starts
              ORDER BY h.created_at, h.id LIMIT 1), m.status) = 'active'
        OR EXISTS (SELECT 1 FROM member_status_history h WHERE h.member_id = m.id AND h.to_status = 'active'
            AND h.created_at >= b.starts AND h.created_at < LEAST(b.ends, b.cutoff))
        OR EXISTS (SELECT 1 FROM receipts r WHERE r.member_id = m.id))
), allocations AS (
    SELECT a.*, r.member_id, c.category, c.reason, t.allocation_breakdown AS split,
        t.id AS loan_transaction_id, s.id AS share_transaction_id,
        CASE WHEN a.type IN ('loan_principal', 'loan_interest') THEN t.id IS NOT NULL AND t.loan_id = a.target_id AND t.amount = a.amount
                AND t.created_at <= b.cutoff AND EXISTS (SELECT 1 FROM loans l WHERE l.id = t.loan_id AND l.member_id = r.member_id AND l.branch_id = r.branch_id)
            WHEN a.type = 'share_purchase' THEN s.id IS NOT NULL AND s.share_account_id = a.target_id AND s.amount = a.amount AND s.type = 'purchase'
                AND s.created_at <= b.cutoff AND EXISTS (SELECT 1 FROM share_accounts sa WHERE sa.id = s.share_account_id AND sa.member_id = r.member_id AND sa.branch_id = r.branch_id)
            WHEN a.type IN ('other_charge', 'penalty') THEN c.id IS NOT NULL AND c.member_id = r.member_id AND c.branch_id = r.branch_id
                AND c.created_at <= b.cutoff AND a.authoritative_reference_id = c.id AND ((a.type = 'penalty') = (c.category = 'penalty'))
            ELSE true END AS source_valid
    FROM contribution_allocations a JOIN receipts r ON r.id = a.receipt_id AND r.included CROSS JOIN bounds b
    LEFT JOIN contribution_charges c ON c.id = a.target_id AND a.type IN ('other_charge', 'penalty')
    LEFT JOIN loan_transactions t ON t.id = a.authoritative_reference_id AND a.type IN ('loan_principal', 'loan_interest') AND t.type = 'repayment'
    LEFT JOIN share_transactions s ON s.id = a.authoritative_reference_id AND a.type = 'share_purchase'
), share_movements AS (
    SELECT sa.member_id, t.share_account_id, t.id, t.type, t.amount, t.balance_after, t.created_at,
        CASE WHEN t.type = 'withdrawal' THEN -t.amount ELSE t.amount END AS delta,
        COALESCE(r.contribution_period, date_trunc('month', t.created_at AT TIME ZONE 'Africa/Nairobi')::date) AS period
    FROM share_transactions t JOIN share_accounts sa ON sa.id = t.share_account_id
    CROSS JOIN bounds b
    LEFT JOIN contribution_allocations a ON a.authoritative_reference_id = t.id AND a.type = 'share_purchase'
    LEFT JOIN contribution_receipts r ON r.id = a.receipt_id
    WHERE sa.branch_id = b.branch_id AND t.created_at <= b.cutoff
), share_checks AS (
    SELECT s.member_id, s.share_account_id, SUM(s.delta) AS expected_balance, MAX(s.created_at) AS latest
    FROM share_movements s GROUP BY s.member_id, s.share_account_id
), loan_movements AS (
    SELECT t.*, l.member_id,
        COALESCE((t.allocation_breakdown->>'period')::date,
            date_trunc('month', COALESCE(CASE WHEN t.type = 'disbursement' THEN l.disbursed_at END, t.created_at) AT TIME ZONE 'Africa/Nairobi')::date) AS period
    FROM loan_transactions t JOIN loans l ON l.id = t.loan_id CROSS JOIN bounds b
    WHERE l.branch_id = b.branch_id AND t.created_at <= b.cutoff
), missing_loan_history AS (
    SELECT l.id, l.member_id FROM loans l CROSS JOIN bounds b
    WHERE l.branch_id = b.branch_id AND l.created_at <= b.cutoff
      AND (l.disbursed_at < b.ends OR (l.disbursed_at IS NULL AND l.created_at < b.ends
        AND l.status IN ('active','disbursed','delinquent','closed','written_off','restructuring')))
      AND NOT EXISTS (SELECT 1 FROM loan_movements t WHERE t.loan_id = l.id AND t.type = 'disbursement' AND t.period <= b.period)
), loan_positions AS (
    SELECT l.id, l.member_id, COALESCE((SELECT i.rate FROM loan_monthly_interest i
        WHERE i.loan_id = l.id AND i.period = b.period AND i.created_at <= b.cutoff), l.interest_rate) AS interest_rate,
        COALESCE(SUM(t.amount) FILTER (WHERE t.type = 'disbursement'), 0) AS disbursed,
        COALESCE(SUM(CASE WHEN t.type = 'disbursement' THEN t.amount
            WHEN t.type = 'repayment' THEN -COALESCE((t.allocation_breakdown->>'principal')::numeric,
                t.amount - COALESCE((t.allocation_breakdown->>'credit')::numeric, 0)) ELSE 0 END) FILTER (WHERE t.period < b.period), 0) AS opening,
        COALESCE(SUM(CASE WHEN t.type = 'disbursement' THEN t.amount
            WHEN t.type = 'repayment' THEN -COALESCE((t.allocation_breakdown->>'principal')::numeric,
                t.amount - COALESCE((t.allocation_breakdown->>'credit')::numeric, 0)) ELSE 0 END), 0) AS closing,
        COALESCE(SUM(t.amount) FILTER (WHERE t.type = 'disbursement' AND t.period = b.period), 0) AS new_disbursements,
        (SELECT i.amount::text FROM loan_monthly_interest i WHERE i.loan_id = l.id AND i.period = b.period AND i.created_at <= b.cutoff) AS interest_assessed,
        GREATEST(COALESCE((SELECT SUM(i.amount) FROM loan_monthly_interest i
            WHERE i.loan_id = l.id AND i.period <= b.period AND i.created_at <= b.cutoff), 0)
            - COALESCE(SUM((t.allocation_breakdown->>'interest')::numeric) FILTER (WHERE t.type = 'repayment'), 0), 0)::text AS unpaid_assessed_interest,
        COALESCE((SELECT jsonb_agg(jsonb_build_object('id', x.id, 'type', x.type, 'amount', x.amount::text, 'split', x.allocation_breakdown) ORDER BY x.created_at, x.id)
            FROM loan_movements x WHERE x.loan_id = l.id AND x.period = b.period), '[]'::jsonb) AS movements
    FROM loans l CROSS JOIN bounds b JOIN loan_movements t ON t.loan_id = l.id AND t.period <= b.period
    GROUP BY l.id, b.period, b.cutoff
    HAVING COALESCE(SUM(CASE WHEN t.type = 'disbursement' THEN t.amount WHEN t.type = 'repayment'
        THEN -COALESCE((t.allocation_breakdown->>'principal')::numeric, t.amount - COALESCE((t.allocation_breakdown->>'credit')::numeric, 0)) ELSE 0 END)
        FILTER (WHERE t.period < b.period), 0) > 0 OR BOOL_OR(t.period = b.period)
), payments AS (
    SELECT m.id,
        COALESCE(SUM(a.amount) FILTER (WHERE a.type = 'com'), 0) AS com,
        COALESCE(SUM(a.amount) FILTER (WHERE a.type = 'lgom'), 0) AS lgom,
        COALESCE(SUM(a.amount) FILTER (WHERE a.type = 'share_purchase'), 0) AS shares,
        COALESCE(SUM(a.amount) FILTER (WHERE a.category = 'literature'), 0) AS literature,
        COALESCE(SUM(a.amount) FILTER (WHERE a.category = 'caritas_registration'), 0) AS registration,
        COALESCE(SUM(a.amount) FILTER (WHERE a.category = 'lsf'), 0) AS lsf,
        COALESCE(SUM(a.amount) FILTER (WHERE a.category = 'laptop'), 0) AS laptop,
        COALESCE(SUM(a.amount) FILTER (WHERE a.category = 'other'), 0) AS other_charges,
        COALESCE(SUM((a.split->>'principal')::numeric), 0) AS principal,
        COALESCE(SUM((a.split->>'interest')::numeric), 0) AS interest,
        COALESCE(SUM((a.split->>'credit')::numeric), 0) + COALESCE(SUM(a.amount) FILTER (WHERE a.type = 'overpayment_credit'), 0) AS credit,
        COALESCE(SUM((a.split->>'penalty')::numeric), 0) + COALESCE(SUM(a.amount) FILTER (WHERE a.category = 'penalty'), 0) AS penalty,
        COALESCE(jsonb_agg(jsonb_build_object('id', a.id, 'receiptId', a.receipt_id, 'type', a.type,
            'targetId', a.target_id, 'amount', a.amount::text, 'transactionId', a.authoritative_reference_id,
            'category', a.category, 'reason', a.reason) ORDER BY a.created_at, a.id) FILTER (WHERE a.id IS NOT NULL), '[]'::jsonb) AS sources
    FROM members_in_period m LEFT JOIN allocations a ON a.member_id = m.id GROUP BY m.id
), rows AS (
    SELECT m.id, m.member_number,
        jsonb_build_object('memberId', m.id, 'memberNumber', lpad(m.member_number::text, GREATEST(3, length(m.member_number::text)), '0'), 'fullName', m.full_name,
            'recNo', 'BO' || (8000 + row_number() OVER (ORDER BY m.member_number, m.id))::text,
            'com', p.com::text, 'lgom', p.lgom::text,
            'previousShares', CASE WHEN missing.share_history THEN NULL ELSE sh.opening::text END, 'sharesPaid', p.shares::text,
            'totalShares', CASE WHEN missing.share_history THEN NULL ELSE sh.closing::text END,
            'otherShareMovements', CASE WHEN missing.share_history THEN NULL ELSE (sh.closing - sh.opening - p.shares)::text END,
            'literature', p.literature::text, 'registration', p.registration::text, 'groupRegistration', NULL,
            'lsf', p.lsf::text, 'laptop', p.laptop::text, 'penalty', p.penalty::text, 'otherCharges', p.other_charges::text,
            'principalDisbursed', CASE WHEN missing.history THEN NULL ELSE lp.disbursed::text END, 'principalPaid', p.principal::text,
            'openingPrincipal', CASE WHEN missing.history THEN NULL ELSE lp.opening::text END,
            'loanBalance', CASE WHEN missing.history THEN NULL ELSE (lp.opening + lp.new_disbursements)::text END,
            'closingPrincipal', CASE WHEN missing.history THEN NULL ELSE lp.closing::text END,
            'newDisbursements', CASE WHEN missing.history THEN NULL ELSE lp.new_disbursements::text END,
            'otherPrincipalMovements', CASE WHEN missing.history THEN NULL ELSE (lp.closing - lp.opening - lp.new_disbursements + p.principal)::text END,
            'interestAssessed', CASE WHEN missing.history THEN NULL ELSE round((lp.opening + lp.new_disbursements) * 0.01, 4)::text END, 'interestPaid', p.interest::text, 'credit', p.credit::text,
            'totalPaid', COALESCE((SELECT SUM(r.received_amount)::text FROM receipts r WHERE r.member_id = m.id AND r.included), '0'),
            'loans', lp.details, 'allocations', p.sources,
            'receipts', COALESCE((SELECT jsonb_agg(jsonb_build_object('id', r.id, 'reference',
                COALESCE(r.internal_receipt_reference, r.external_transaction_id, r.id::text), 'amount', r.received_amount::text,
                'source', r.source_channel, 'receivedAt', r.received_at, 'status', r.status, 'included', r.included,
                'completedAt', CASE WHEN r.status = 'completed' THEN r.updated_at END) ORDER BY r.created_at, r.id)
                FROM receipts r WHERE r.member_id = m.id), '[]'::jsonb),
            'shareMovements', COALESCE((SELECT jsonb_agg(jsonb_build_object('id', x.id, 'type', x.type, 'amount', x.delta::text) ORDER BY x.created_at, x.id)
                FROM share_movements x CROSS JOIN bounds z WHERE x.member_id = m.id AND x.period = z.period), '[]'::jsonb),
            'fees', COALESCE((SELECT jsonb_agg(jsonb_build_object('type', f.type, 'assessed', f.amount::text,
                'paid', (CASE WHEN f.type = 'com' THEN p.com ELSE p.lgom END)::text,
                'outstanding', GREATEST(f.amount - COALESCE((SELECT SUM(a.amount) FROM contribution_allocations a
                    JOIN contribution_receipts r ON r.id = a.receipt_id CROSS JOIN bounds z
                    WHERE r.member_id = m.id AND r.contribution_period = z.period AND a.type = f.type
                        AND a.status = 'completed' AND a.updated_at <= z.cutoff), 0), 0)::text,
                'exemptionReason', f.exemption_reason, 'approvedBy', f.approved_by) ORDER BY f.type)
                FROM contribution_monthly_fees f CROSS JOIN bounds z WHERE f.member_id = m.id AND f.period = z.period AND f.created_at <= z.cutoff), '[]'::jsonb),
            'charges', COALESCE((SELECT jsonb_agg(jsonb_build_object('id', c.id, 'category', c.category, 'reason', c.reason,
                'assessed', c.amount::text, 'paid', cp.paid::text, 'outstanding', (c.amount - cp.paid)::text) ORDER BY c.created_at, c.id)
                FROM contribution_charges c CROSS JOIN bounds z CROSS JOIN LATERAL (
                    SELECT COALESCE(SUM(a.amount), 0) AS paid FROM contribution_allocations a WHERE a.target_id = c.id
                        AND a.status = 'completed' AND a.updated_at <= z.cutoff
                ) cp WHERE c.member_id = m.id AND c.branch_id = z.branch_id AND c.created_at <= z.cutoff), '[]'::jsonb)
        ) AS data
    FROM members_in_period m JOIN payments p ON p.id = m.id CROSS JOIN bounds b
    CROSS JOIN LATERAL (SELECT EXISTS (SELECT 1 FROM missing_loan_history x WHERE x.member_id = m.id) AS history,
        EXISTS (SELECT 1 FROM share_checks c WHERE c.member_id = m.id AND NOT EXISTS (
            SELECT 1 FROM share_movements x WHERE x.share_account_id = c.share_account_id
                AND x.created_at = c.latest AND x.balance_after = c.expected_balance)) AS share_history) missing
    CROSS JOIN LATERAL (
        SELECT COALESCE(SUM(s.delta) FILTER (WHERE s.period < b.period), 0) AS opening,
            COALESCE(SUM(s.delta) FILTER (WHERE s.period <= b.period), 0) AS closing
        FROM share_movements s WHERE s.member_id = m.id
    ) sh CROSS JOIN LATERAL (
        SELECT COALESCE(SUM(l.disbursed), 0) AS disbursed, COALESCE(SUM(l.opening), 0) AS opening,
            COALESCE(SUM(l.closing), 0) AS closing, COALESCE(SUM(l.new_disbursements), 0) AS new_disbursements,
            COALESCE(jsonb_agg(jsonb_build_object('id', l.id, 'disbursed', l.disbursed::text, 'opening', l.opening::text,
                'closing', l.closing::text, 'rate', l.interest_rate::text, 'interestAssessed', l.interest_assessed,
                'unpaidAssessedInterest', l.unpaid_assessed_interest, 'movements', l.movements) ORDER BY l.id), '[]'::jsonb) AS details
        FROM loan_positions l WHERE l.member_id = m.id
    ) lp
), issues AS (
    SELECT 'error'::text AS severity, r.id::text AS reference, 'Completed receipt does not reconcile to its allocations'::text AS message
    FROM receipts r WHERE r.included AND r.received_amount <> (SELECT COALESCE(SUM(a.amount), 0) FROM allocations a WHERE a.receipt_id = r.id)
    UNION ALL SELECT 'error', r.id::text, 'Completed receipt has missing or incomplete allocations'
        FROM receipts r CROSS JOIN bounds b WHERE r.status = 'completed' AND r.updated_at <= b.cutoff AND NOT r.included
    UNION ALL SELECT 'error', r.id::text, 'Receipt member is missing from the branch reporting population'
        FROM receipts r WHERE NOT EXISTS (SELECT 1 FROM members_in_period m WHERE m.id = r.member_id)
    UNION ALL SELECT 'error', a.id::text, 'Allocation is missing its authoritative source or has a mismatched target/amount'
        FROM allocations a WHERE a.source_valid IS NOT TRUE
    UNION ALL SELECT 'error', a.authoritative_reference_id::text, 'Ledger transaction is linked to more than one completed allocation'
        FROM allocations a WHERE a.type IN ('loan_principal','loan_interest','share_purchase') AND a.authoritative_reference_id IS NOT NULL
        GROUP BY a.authoritative_reference_id HAVING COUNT(*) > 1
    UNION ALL SELECT 'error', a.id::text, 'Loan allocation is missing a complete, balanced principal/interest/penalty/credit split'
        FROM allocations a WHERE a.type IN ('loan_principal', 'loan_interest') AND
        (NOT COALESCE(a.split ?& ARRAY['principal','interest','penalty','credit'], false)
         OR a.amount <> COALESCE((a.split->>'principal')::numeric, 0) + COALESCE((a.split->>'interest')::numeric, 0)
            + COALESCE((a.split->>'penalty')::numeric, 0) + COALESCE((a.split->>'credit')::numeric, 0))
    UNION ALL SELECT 'warning', r.id::text, 'Receipt excluded from completed totals at cutoff: ' || r.status::text
        || ', KES ' || r.received_amount::text || '; completed allocations may still affect ledgers'
        FROM receipts r WHERE NOT r.included
    UNION ALL SELECT 'error', l.id::text, 'Negative historical principal requires Loans reconciliation'
        FROM loan_positions l WHERE l.opening < 0 OR l.closing < 0
    UNION ALL SELECT 'error', l.id::text, 'Disbursed loan has no authoritative disbursement history; balances are unavailable'
        FROM missing_loan_history l
    UNION ALL SELECT 'error', r.id::text, 'Negative historical Shares balance requires ledger reconciliation'
        FROM rows r WHERE (r.data->>'previousShares')::numeric < 0 OR (r.data->>'totalShares')::numeric < 0
    UNION ALL SELECT 'error', c.share_account_id::text, 'Share movement history does not reconcile to its recorded ledger balance; balances are unavailable'
        FROM share_checks c WHERE EXISTS (SELECT 1 FROM members_in_period m WHERE m.id = c.member_id)
            AND NOT EXISTS (SELECT 1 FROM share_movements x WHERE x.share_account_id = c.share_account_id
                AND x.created_at = c.latest AND x.balance_after = c.expected_balance)
    UNION ALL SELECT 'error', t.id::text, 'Loan reversal or unsupported movement requires explicit principal reconciliation'
        FROM loan_movements t CROSS JOIN bounds b WHERE t.period <= b.period AND t.type NOT IN ('disbursement','repayment','penalty','credit_withdrawal')
    UNION ALL SELECT 'warning', m.id::text, 'COM/LGOM assessment missing; standard expectation is KES 30 each, subject to eligibility/exemption'
        FROM members_in_period m CROSS JOIN bounds b WHERE (SELECT COUNT(*) FROM contribution_monthly_fees f
            WHERE f.member_id = m.id AND f.period = b.period AND f.created_at <= b.cutoff) < 2
    UNION ALL SELECT 'warning', m.id::text, 'Member name unavailable' FROM members_in_period m WHERE m.full_name = ''
    UNION ALL SELECT 'error', r.id::text, 'Payment components do not reconcile to completed cash received'
        FROM rows r WHERE (r.data->>'totalPaid')::numeric <> (r.data->>'com')::numeric + (r.data->>'lgom')::numeric
            + (r.data->>'sharesPaid')::numeric + (r.data->>'principalPaid')::numeric + (r.data->>'interestPaid')::numeric
            + (r.data->>'credit')::numeric + (r.data->>'literature')::numeric + (r.data->>'registration')::numeric
            + (r.data->>'lsf')::numeric + (r.data->>'laptop')::numeric + (r.data->>'penalty')::numeric + (r.data->>'otherCharges')::numeric
)
SELECT jsonb_build_object('branchId', b.branch_id::text, 'period', b.period::text, 'cutoff', b.cutoff,
    'rows', COALESCE((SELECT jsonb_agg(r.data ORDER BY r.member_number, r.id) FROM rows r), '[]'::jsonb),
    'issues', COALESCE((SELECT jsonb_agg(jsonb_build_object('severity', i.severity, 'reference', i.reference, 'message', i.message)
        ORDER BY i.severity, i.reference, i.message) FROM issues i), '[]'::jsonb),
    'hasErrors', EXISTS (SELECT 1 FROM issues WHERE severity = 'error')) AS report
FROM bounds b;
