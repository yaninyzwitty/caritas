-- name: GetAccountByMemberID :one
SELECT id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at FROM share_accounts
WHERE member_id = $1 AND is_deleted = FALSE;

-- name: GetAccountByID :one
SELECT id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at FROM share_accounts
WHERE id = $1 AND is_deleted = FALSE;

-- name: ListAccounts :many
SELECT id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at FROM share_accounts
WHERE is_deleted = FALSE
  AND (created_at < $1 OR (created_at = $1 AND id < $2))
ORDER BY created_at DESC, id DESC
LIMIT $3;

-- name: LockAndReadAccount :one
SELECT id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at FROM share_accounts
WHERE id = $1 AND is_deleted = FALSE
FOR UPDATE;

-- name: UpdateAccountStatus :one
UPDATE share_accounts
    SET status = $2, updated_at = NOW()
WHERE id = $1 AND is_deleted = FALSE
RETURNING id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at;