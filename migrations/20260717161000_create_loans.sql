-- +goose Up
CREATE TYPE loan_status AS ENUM (
    'pending',
    'approved',
    'rejected',
    'disbursed',
    'restructuring',
    'active',
    'delinquent',
    'closed',
    'written_off',
    'manual_review'
);

CREATE TYPE interest_period AS ENUM ('monthly', 'yearly');

CREATE TABLE loans (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    member_id UUID NOT NULL,
    branch_id BIGINT NOT NULL,
    
    principal NUMERIC(19,4) NOT NULL,
    interest_rate NUMERIC(9,4) NOT NULL,
    interest_period interest_period NOT NULL DEFAULT 'monthly',
    repayment_period_months INT NOT NULL,
    status loan_status NOT NULL DEFAULT 'pending',

    disbursed_at TIMESTAMPTZ,
    updated_by UUID,
    previous_status loan_status,
    is_deleted BOOLEAN NOT NULL DEFAULT FALSE,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

-- TODO-re-check these constrains
    CONSTRAINT fk_loans_member FOREIGN KEY (member_id) REFERENCES members(id) ON DELETE RESTRICT,
    CONSTRAINT chk_loans_principal_positive CHECK (principal > 0),
    CONSTRAINT chk_loans_interest_rate_non_negative CHECK (interest_rate >= 0),
    CONSTRAINT chk_repayment_period_valid CHECK (repayment_period_months > 0 AND repayment_period_months <= 36),
    CONSTRAINT chk_loans_disbursed_at CHECK ((status = 'disbursed' AND disbursed_at IS NOT NULL) OR status <> 'disbursed')
);

CREATE INDEX idx_loans_member ON loans (member_id, is_deleted) WHERE is_deleted = FALSE;
CREATE INDEX idx_loans_branch_status ON loans (branch_id, status, is_deleted) WHERE is_deleted = FALSE;
CREATE INDEX idx_loans_cursor ON loans (created_at DESC, id DESC) WHERE is_deleted = FALSE;

-- +goose Down
DROP INDEX IF EXISTS idx_loans_cursor;
DROP INDEX IF EXISTS idx_loans_branch_status;
DROP INDEX IF EXISTS idx_loans_member;
DROP TABLE IF EXISTS loans;
DROP TYPE IF EXISTS loan_status;
