-- +goose Up
ALTER TABLE staff_users ADD COLUMN auth_user_id TEXT;

 CREATE UNIQUE INDEX staff_users_auth_user_id_key
  ON staff_users (auth_user_id)
  WHERE auth_user_id IS NOT NULL;


-- +goose Down
DROP INDEX IF EXISTS staff_users_auth_user_id_key;

ALTER TABLE staff_users
DROP COLUMN IF EXISTS auth_user_id;