-- name: CountStaffUsers :one
SELECT count(*) FROM staff_users;

-- name: CreateStaffUser :one
INSERT INTO staff_users (auth_user_id, name, branch_id, email, role)
VALUES (sqlc.arg(auth_user_id), sqlc.arg(name), sqlc.arg(branch_id), lower(sqlc.arg(email)), sqlc.arg(role))
ON CONFLICT DO NOTHING
RETURNING *;

-- name: DeactivateStaffUser :one
UPDATE staff_users
SET is_active = FALSE,
    updated_at = NOW()
WHERE id = $1
  AND is_active = TRUE
RETURNING *;

-- name: GetActiveStaffByAuthUserID :one
SELECT *
FROM staff_users
WHERE auth_user_id = sqlc.arg(auth_user_id)
  AND is_active = TRUE;

-- name: GetActiveStaffByID :one
SELECT *
FROM staff_users
WHERE id = sqlc.arg(id) AND is_active = TRUE;

-- name: ListStaffUsers :many
SELECT * FROM staff_users
WHERE (sqlc.arg(query)::text = '' OR name ILIKE '%' || sqlc.arg(query)::text || '%' OR email ILIKE '%' || sqlc.arg(query)::text || '%')
  AND (sqlc.arg(role)::text = '' OR role = sqlc.arg(role)::text)
  AND (created_at, id) < (sqlc.arg(before_created_at)::timestamptz, sqlc.arg(before_id)::uuid)
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit);
