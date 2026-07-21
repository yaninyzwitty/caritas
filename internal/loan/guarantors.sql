-- name: CreateLoanGuarantor :one
INSERT INTO loan_guarantors (loan_id, guarantor_id, guaranteed_amount, status, approved_by)
VALUES ($1, $2, $3, $4, $5)
RETURNING loan_id, guarantor_id, guaranteed_amount, status, approved_at, approved_by, created_at;

-- name: GetLoanGuarantor :one
SELECT loan_id, guarantor_id, guaranteed_amount, status, approved_at, approved_by, created_at
FROM loan_guarantors
WHERE loan_id = $1 AND guarantor_id = $2;

-- name: ListLoanGuarantors :many
SELECT loan_id, guarantor_id, guaranteed_amount, status, approved_at, approved_by, created_at
FROM loan_guarantors
WHERE loan_id = $1
ORDER BY created_at DESC;

-- name: ListGuarantorLoans :many
SELECT lg.loan_id, lg.guarantor_id, lg.guaranteed_amount, lg.status, lg.approved_at, lg.approved_by, lg.created_at,
       l.member_id, l.principal, l.status as loan_status
FROM loan_guarantors lg
JOIN loans l ON lg.loan_id = l.id
WHERE lg.guarantor_id = $1 AND l.is_deleted = FALSE
ORDER BY lg.created_at DESC;

-- name: UpdateGuarantorStatus :one
UPDATE loan_guarantors
SET status = $2,
    approved_at = CASE WHEN $2 = 'approved' THEN NOW() ELSE approved_at END,
    approved_by = $3
WHERE loan_id = $1 AND guarantor_id = $4
RETURNING loan_id, guarantor_id, guaranteed_amount, status, approved_at, approved_by, created_at;

-- name: GetTotalGuaranteedAmount :one
SELECT COALESCE(SUM(guaranteed_amount), 0) as total_guaranteed
FROM loan_guarantors
WHERE loan_id = $1 AND status = 'approved';

-- name: CountApprovedGuarantors :one
SELECT COUNT(*) as count
FROM loan_guarantors
WHERE loan_id = $1 AND status = 'approved';

-- name: LockGuarantor :one
SELECT loan_id, guarantor_id, guaranteed_amount, status, approved_at, approved_by, created_at
FROM loan_guarantors
WHERE loan_id = $1 AND guarantor_id = $2
FOR UPDATE;