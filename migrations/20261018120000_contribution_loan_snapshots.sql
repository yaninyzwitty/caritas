-- +goose Up
ALTER TABLE contribution_receipts ADD COLUMN loan_snapshot jsonb;

-- +goose Down
ALTER TABLE contribution_receipts DROP COLUMN loan_snapshot;
