-- name: GetAccountByMemberID :one
SELECT id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at FROM share_accounts
WHERE member_id = $1 AND is_deleted = FALSE;

-- name: GetAccountByID :one
SELECT id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at FROM share_accounts
WHERE id = $1 AND is_deleted = FALSE;

-- name: ListAccounts :many
SELECT id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at FROM share_accounts
WHERE is_deleted = FALSE
  AND branch_id = $1
  AND ($2::timestamptz IS NULL OR created_at < $2 OR (created_at = $2 AND id < $3))
  AND (sqlc.narg('status_filter')::share_account_status IS NULL OR status = sqlc.narg('status_filter'))
ORDER BY created_at DESC, id DESC
LIMIT $4;

-- name: CreateShareAccount :one
INSERT INTO share_accounts (member_id, branch_id, status, opened_at)
VALUES ($1, $2, 'active', NOW())
RETURNING id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at;

-- name: LockAndReadAccount :one
SELECT id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at FROM share_accounts
WHERE id = $1 AND is_deleted = FALSE
FOR UPDATE;

-- name: UpdateAccountStatus :one
UPDATE share_accounts
    SET status = $2, updated_at = NOW()
WHERE id = $1 AND is_deleted = FALSE
RETURNING id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at;