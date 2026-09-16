-- +goose Up
ALTER TABLE staff_users
ALTER COLUMN password_hash DROP NOT NULL;

-- +goose Down
UPDATE staff_users
SET password_hash = ''
WHERE password_hash IS NULL;

ALTER TABLE staff_users
ALTER COLUMN password_hash SET NOT NULL;
