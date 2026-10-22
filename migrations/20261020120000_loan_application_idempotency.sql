-- +goose Up
ALTER TABLE loans
    ADD COLUMN application_key UUID,
    ADD COLUMN application_hash TEXT,
    ADD CONSTRAINT chk_loan_application_key_hash CHECK ((application_key IS NULL) = (application_hash IS NULL));

CREATE UNIQUE INDEX uq_loans_application_key ON loans (application_key)
WHERE application_key IS NOT NULL;

-- +goose Down
DROP INDEX uq_loans_application_key;
ALTER TABLE loans DROP CONSTRAINT chk_loan_application_key_hash,
    DROP COLUMN application_key, DROP COLUMN application_hash;
