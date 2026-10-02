-- name: GetAccountByMemberID :one
SELECT id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at FROM share_accounts
WHERE member_id = $1 AND is_deleted = FALSE;

-- name: GetAccountByMemberIdentifier :one
SELECT sqlc.embed(sa), m.member_number, mp.full_name AS member_name
FROM share_accounts AS sa
JOIN members AS m ON m.id = sa.member_id
LEFT JOIN member_profiles AS mp ON mp.member_id = m.id
WHERE sa.is_deleted = FALSE
  AND m.is_deleted = FALSE
  AND sa.branch_id = sqlc.arg(branch_id)
  AND m.branch_id = sqlc.arg(branch_id)
  AND (m.member_number = sqlc.narg(member_number)::bigint
       OR m.national_id = sqlc.narg(national_id)::text);

-- name: ListShareAccounts :many
SELECT
    sa.id,
    sa.member_id,
    sa.branch_id,
    sa.status,
    sa.opened_at,
    sa.created_at,
    sa.updated_at,
    m.member_number,
    mp.full_name AS member_name
FROM share_accounts AS sa
JOIN members AS m ON m.id = sa.member_id
LEFT JOIN member_profiles AS mp ON mp.member_id = m.id
WHERE sa.is_deleted = FALSE
  AND m.is_deleted = FALSE
  AND sa.branch_id = sqlc.arg(branch_id)
  AND (sqlc.narg(status_filter)::share_account_status IS NULL OR sa.status = sqlc.narg(status_filter))
  AND (
      sqlc.narg(cursor_member_number)::bigint IS NULL
      OR (m.member_number, sa.id) > (
          sqlc.narg(cursor_member_number)::bigint,
          sqlc.narg(cursor_id)::uuid
      )
  )
ORDER BY m.member_number ASC, sa.id ASC
LIMIT sqlc.arg(fetch_limit);

-- name: GetAccountByID :one
SELECT sqlc.embed(sa), m.member_number, mp.full_name AS member_name
FROM share_accounts AS sa
JOIN members AS m ON m.id = sa.member_id
LEFT JOIN member_profiles AS mp ON mp.member_id = m.id
WHERE sa.id = $1 AND sa.is_deleted = FALSE;

-- name: CreateShareAccount :one
INSERT INTO share_accounts (member_id, branch_id, status, opened_at)
VALUES ($1, $2, 'active', NOW())
RETURNING id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at;

-- name: LockAndReadAccount :one
SELECT id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at FROM share_accounts
WHERE id = $1 AND is_deleted = FALSE
FOR UPDATE;

-- name: LockAccountByMemberID :one
SELECT id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at FROM share_accounts
WHERE member_id = $1 AND is_deleted = FALSE
FOR UPDATE;

-- name: UpdateAccountStatus :one
UPDATE share_accounts
    SET status = $2, updated_at = NOW()
WHERE id = $1 AND is_deleted = FALSE
RETURNING id, member_id, branch_id, status, opened_at, is_deleted, created_at, updated_at;
