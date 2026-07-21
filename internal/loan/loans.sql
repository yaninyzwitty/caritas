-- name: CreateLoan :one
INSERT INTO loans (member_id, branch_id, principal, interest_rate, repayment_period_months, updated_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, member_id, branch_id, principal, interest_rate, repayment_period_months, status, disbursed_at, updated_by, previous_status, is_deleted, created_at, updated_at;

-- name: GetLoanByID :one
SELECT id, member_id, branch_id, principal, interest_rate, repayment_period_months, status, disbursed_at, updated_by, previous_status, is_deleted, created_at, updated_at
FROM loans
WHERE id = $1 AND is_deleted = FALSE;

-- name: ListLoansByBranch :many
SELECT id, member_id, branch_id, principal, interest_rate, repayment_period_months, status, disbursed_at, updated_by, previous_status, is_deleted, created_at, updated_at
FROM loans
WHERE branch_id = $1
  AND is_deleted = FALSE
  AND ($2::timestamptz IS NULL OR created_at < $2 OR (created_at = $2 AND id < $3))
  AND (sqlc.narg('status_filter')::loan_status IS NULL OR status = sqlc.narg('status_filter'))
ORDER BY created_at DESC, id DESC
LIMIT $4;

-- name: ListLoansByMember :many
SELECT id, member_id, branch_id, principal, interest_rate, repayment_period_months, status, disbursed_at, updated_by, previous_status, is_deleted, created_at, updated_at
FROM loans
WHERE member_id = $1
  AND is_deleted = FALSE
  AND ($2::timestamptz IS NULL OR created_at < $2 OR (created_at = $2 AND id < $3))
ORDER BY created_at DESC, id DESC
LIMIT $4;

-- name: LockLoanByID :one
SELECT id, member_id, branch_id, principal, interest_rate, repayment_period_months, status, disbursed_at, updated_by, previous_status, is_deleted, created_at, updated_at
FROM loans
WHERE id = $1 AND is_deleted = FALSE
FOR UPDATE;

-- name: UpdateLoanStatus :one
UPDATE loans
SET previous_status = status,
    status = $2,
    updated_by = $3,
    updated_at = NOW()
WHERE id = $1 AND is_deleted = FALSE
RETURNING id, member_id, branch_id, principal, interest_rate, repayment_period_months, status, disbursed_at, updated_by, previous_status, is_deleted, created_at, updated_at;

-- name: MarkLoanDisbursed :one
UPDATE loans
SET previous_status = status,
    status = 'disbursed',
    disbursed_at = NOW(),
    updated_by = $2,
    updated_at = NOW()
WHERE id = $1 AND status = 'approved' AND is_deleted = FALSE
RETURNING id, member_id, branch_id, principal, interest_rate, repayment_period_months, status, disbursed_at, updated_by, previous_status, is_deleted, created_at, updated_at;

-- name: SoftDeleteLoan :one
UPDATE loans
SET is_deleted = TRUE,
    updated_by = $2,
    updated_at = NOW()
WHERE id = $1 AND is_deleted = FALSE
RETURNING id, member_id, branch_id, principal, interest_rate, repayment_period_months, status, disbursed_at, updated_by, previous_status, is_deleted, created_at, updated_at;

-- name: ListLoansForDelinquencyUpdate :many
SELECT id, member_id, branch_id, principal, interest_rate, repayment_period_months, status, disbursed_at, updated_by, previous_status, is_deleted, created_at, updated_at
FROM loans
WHERE status IN ('active', 'delinquent')
  AND is_deleted = FALSE
ORDER BY created_at ASC, id ASC
LIMIT $1
FOR UPDATE SKIP LOCKED;
