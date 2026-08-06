-- +goose Up
ALTER TABLE staff_users ADD COLUMN name TEXT;
UPDATE staff_users SET name = email WHERE name IS NULL;
ALTER TABLE staff_users ALTER COLUMN name SET NOT NULL;
-- +goose Down
ALTER TABLE staff_users DROP COLUMN IF EXISTS name;
