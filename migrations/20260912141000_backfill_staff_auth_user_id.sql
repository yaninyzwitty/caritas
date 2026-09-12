-- +goose Up
DO $$
BEGIN
    IF EXISTS (
        SELECT staff.id
        FROM staff_users AS staff
        LEFT JOIN "user" AS auth_user
            ON lower(auth_user.email) = lower(staff.email)
        WHERE staff.is_active = TRUE
          AND staff.auth_user_id IS NULL
        GROUP BY staff.id
        HAVING count(auth_user.id) <> 1
    ) THEN
        RAISE EXCEPTION 'every active staff user must match exactly one Better Auth user by email';
    END IF;
END $$;

UPDATE staff_users AS staff
SET auth_user_id = auth_user.id,
    updated_at = NOW()
FROM "user" AS auth_user
WHERE lower(auth_user.email) = lower(staff.email)
  AND staff.auth_user_id IS NULL;

-- +goose Down
SELECT 1;
