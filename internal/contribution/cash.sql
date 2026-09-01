-- name: InsertCashierSession :one
INSERT INTO cashier_sessions (branch_id, cashier_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetOpenCashierSession :one
SELECT *
FROM cashier_sessions
WHERE cashier_id = $1 AND status = 'open';

-- name: LockCashierSession :one
SELECT *
FROM cashier_sessions
WHERE id = $1
FOR UPDATE;

-- name: LockCashierSessions :many
SELECT *
FROM cashier_sessions
WHERE id = ANY($1::UUID[])
ORDER BY id
FOR UPDATE;

-- name: SumCashReceiptsBySession :one
SELECT COALESCE(SUM(received_amount), 0)::NUMERIC(19,4)
FROM contribution_receipts
WHERE cashier_session_id = $1
  AND source_channel = 'cash'
  AND is_deleted = FALSE;

-- name: CloseCashierSession :one
UPDATE cashier_sessions
SET status = 'closed',
    expected_amount = $2,
    counted_amount = $3,
    variance = $3::NUMERIC(19,4) - $2::NUMERIC(19,4), --explict rumeric prevents errors
    variance_reason = $4,
    closed_at = NOW(),
    closed_by = $5,
    updated_at = NOW()
WHERE id = $1 AND status = 'open'
RETURNING *;

-- name: AcceptCashHandover :one
UPDATE cashier_sessions
SET status = 'handed_over',
    handed_over_at = NOW(),
    handed_over_to = $2,
    updated_at = NOW()
WHERE id = $1 AND status = 'closed'
RETURNING *;

-- name: InsertCashDeposit :one
INSERT INTO cash_deposits (branch_id, amount, bank_reference, recorded_by)
VALUES ($1, $2, $3, $4)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: GetCashDepositByBankReference :one
SELECT *
FROM cash_deposits
WHERE bank_reference = $1;

-- name: GetCashDepositByID :one
SELECT *
FROM cash_deposits
WHERE id = $1;

-- name: InsertCashDepositSession :one
INSERT INTO cash_deposit_sessions (deposit_id, session_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING
RETURNING *;

-- name: ListCashDepositSessions :many
SELECT session_id
FROM cash_deposit_sessions
WHERE deposit_id = $1
ORDER BY session_id;

-- name: MarkCashierSessionDeposited :one
UPDATE cashier_sessions
SET status = 'deposited',
    deposited_at = NOW(),
    updated_at = NOW()
WHERE id = $1 AND status = 'handed_over'
RETURNING *;

-- name: VerifyCashDeposit :one
UPDATE cash_deposits
SET status = 'verified',
    verified_by = $2,
    verified_at = NOW(),
    updated_at = NOW()
WHERE id = $1 AND status = 'recorded'
RETURNING *;
