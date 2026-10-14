-- +goose Up
ALTER TYPE share_account_status ADD VALUE IF NOT EXISTS 'frozen';

-- +goose Down
-- PostgreSQL cannot remove an enum value without recreating the type.
SELECT 1;
