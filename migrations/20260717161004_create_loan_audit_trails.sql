-- +goose Up
CREATE TABLE loan_audit_trails (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    loan_id UUID NOT NULL,
    field_changed TEXT NOT NULL,
    previous_value TEXT,
    new_value TEXT NOT NULL,
    changed_by UUID NOT NULL,
    change_reason TEXT NOT NULL,
    approval_reference TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_loan_audit_trails_loan FOREIGN KEY (loan_id) REFERENCES loans(id) ON DELETE RESTRICT,
    CONSTRAINT chk_loan_audit_trails_field_changed_not_blank CHECK (btrim(field_changed) <> ''),
    CONSTRAINT chk_loan_audit_trails_change_reason_not_blank CHECK (btrim(change_reason) <> '')
);

CREATE INDEX idx_loan_audit_trails_loan ON loan_audit_trails (loan_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_loan_audit_trails_loan;
DROP TABLE IF EXISTS loan_audit_trails;
