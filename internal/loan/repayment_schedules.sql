-- name: CreateRepaymentSchedule :one
INSERT INTO repayment_schedules (loan_id, installment_no, due_date, amount_due, status)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (loan_id, installment_no, status) DO NOTHING
RETURNING id, loan_id, installment_no, due_date, amount_due, status, created_at, updated_at;

-- name: CreateRepaymentSchedules :execrows
INSERT INTO repayment_schedules (loan_id, installment_no, due_date, amount_due, status)
SELECT sqlc.arg(loan_id), item.installment_no, item.due_date, item.amount_due, 'upcoming'
FROM jsonb_to_recordset(sqlc.arg(schedules)::jsonb)
  AS item(installment_no integer, due_date date, amount_due numeric)
ON CONFLICT DO NOTHING;

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

-- name: LockActiveRepaymentSchedulesByLoan :many
SELECT id, loan_id, installment_no, due_date, amount_due, status, created_at, updated_at
FROM repayment_schedules
WHERE loan_id = $1
  AND status <> 'superseded'
ORDER BY installment_no ASC
FOR UPDATE;

-- name: UpdateRepaymentScheduleStatus :one
UPDATE repayment_schedules
SET status = $2,
    updated_at = NOW()
WHERE id = $1
RETURNING id, loan_id, installment_no, due_date, amount_due, status, created_at, updated_at;

-- name: UpdateRepaymentScheduleStatuses :execrows
UPDATE repayment_schedules AS s
SET status = changes.status::repayment_schedule_status,
    updated_at = NOW()
FROM jsonb_to_recordset(sqlc.arg(changes)::jsonb) AS changes(id uuid, status text)
WHERE s.id = changes.id;

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
