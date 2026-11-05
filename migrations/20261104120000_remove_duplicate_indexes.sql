-- +goose Up
DROP INDEX idx_share_transactions_reference;
-- +goose StatementBegin
DO $$
DECLARE
    duplicate_name name;
BEGIN
    SELECT unique_constraint.conname INTO duplicate_name
    FROM pg_constraint unique_constraint
    JOIN pg_constraint primary_key
      ON primary_key.conrelid = unique_constraint.conrelid
     AND primary_key.contype = 'p'
     AND primary_key.conkey = unique_constraint.conkey
    WHERE unique_constraint.conrelid = 'loan_guarantors'::regclass
      AND unique_constraint.contype = 'u';

    IF duplicate_name IS NOT NULL THEN
        EXECUTE format('ALTER TABLE loan_guarantors DROP CONSTRAINT %I', duplicate_name);
    END IF;
END;
$$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE loan_guarantors ADD CONSTRAINT loan_guarantors_loan_id_guarantor_id_key UNIQUE (loan_id, guarantor_id);
CREATE INDEX idx_share_transactions_reference ON share_transactions (share_account_id, reference_id, type);
