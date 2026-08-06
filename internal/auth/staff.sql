-- name: CountStaffUsers :one
SELECT count(*) FROM staff_users;

-- name: CreateStaffUser :one
INSERT INTO staff_users (name, branch_id, email, password_hash, role)
VALUES (sqlc.arg(name), sqlc.arg(branch_id), lower(sqlc.arg(email)), sqlc.arg(password_hash), sqlc.arg(role))
ON CONFLICT (email) DO NOTHING
RETURNING *;

-- name: DeactivateStaffUser :one
UPDATE staff_users
SET is_active = FALSE,
    updated_at = NOW()
WHERE id = $1
  AND is_active = TRUE
RETURNING *;

-- name: GetActiveStaffByEmail :one
SELECT *
FROM staff_users
WHERE lower(email) = lower(sqlc.arg(email)) AND is_active = TRUE;

-- name: GetActiveStaffByID :one
SELECT *
FROM staff_users
WHERE id = sqlc.arg(id) AND is_active = TRUE;
