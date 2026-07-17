-- +goose Up
CREATE TYPE repayment_schedule_status AS ENUM ('upcoming', 'due', 'paid', 'missed', 'partial', 'superseded');

CREATE TABLE repayment_schedules (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    loan_id UUID NOT NULL,
    installment_no INTEGER NOT NULL,
    due_date DATE NOT NULL,
    amount_due NUMERIC(19,4) NOT NULL,
    status repayment_schedule_status NOT NULL DEFAULT 'upcoming',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT fk_repayment_schedules_loan FOREIGN KEY (loan_id) REFERENCES loans(id) ON DELETE RESTRICT,
    CONSTRAINT chk_repayment_schedules_installment_positive CHECK (installment_no > 0),
    CONSTRAINT chk_repayment_schedules_amount_due_positive CHECK (amount_due > 0),
    UNIQUE (loan_id, installment_no, status)
);

CREATE INDEX idx_repayment_schedules_loan ON repayment_schedules (loan_id, installment_no);
CREATE INDEX idx_repayment_schedules_due ON repayment_schedules (due_date, loan_id) WHERE status IN ('due', 'missed');

-- +goose Down
DROP INDEX IF EXISTS idx_repayment_schedules_due;
DROP INDEX IF EXISTS idx_repayment_schedules_loan;
DROP TABLE IF EXISTS repayment_schedules;
DROP TYPE IF EXISTS repayment_schedule_status;
