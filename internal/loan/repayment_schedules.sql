-- name: CreateRepaymentSchedule :one
INSERT INTO repayment_schedules (loan_id, installment_no, due_date, amount_due, status)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (loan_id, installment_no, status) DO NOTHING
RETURNING id, loan_id, installment_no, due_date, amount_due, status, created_at, updated_at;

-- name: ListRepaymentSchedulesByLoan :many
SELECT id, loan_id, installment_no, due_date, amount_due, status, created_at, updated_at
FROM repayment_schedules
WHERE loan_id = $1
ORDER BY installment_no ASC;

-- name: LockRepaymentSchedulesByLoan :many
SELECT id, loan_id, installment_no, due_date, amount_due, status, created_at, updated_at
FROM repayment_schedules
WHERE loan_id = $1
ORDER BY installment_no ASC
FOR UPDATE;

-- name: UpdateRepaymentScheduleStatus :one
UPDATE repayment_schedules
SET status = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING id, loan_id, installment_no, due_date, amount_due, status, created_at, updated_at;

-- name: SupersedeRepaymentSchedules :exec
UPDATE repayment_schedules
SET status = 'superseded',
    updated_at = NOW()
WHERE loan_id = $1 AND status <> 'superseded';

-- name: ListDueRepaymentSchedules :many
SELECT id, loan_id, installment_no, due_date, amount_due, status, created_at, updated_at
FROM repayment_schedules
WHERE due_date <= $1 AND status IN ('due', 'missed')
ORDER BY due_date ASC, loan_id ASC;
