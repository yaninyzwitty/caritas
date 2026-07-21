-- +goose Up

CREATE TYPE guarantor_status AS ENUM ('pending', 'approved', 'rejected');

CREATE TABLE loan_guarantors (
    loan_id UUID NOT NULL,
    guarantor_id UUID NOT NULL,

    guaranteed_amount NUMERIC(19,4) NOT NULL,

    status guarantor_status NOT NULL DEFAULT 'pending',

    approved_at TIMESTAMPTZ,
    approved_by UUID,

    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    PRIMARY KEY (loan_id, guarantor_id),
    UNIQUE (loan_id, guarantor_id),

    FOREIGN KEY (loan_id)
        REFERENCES loans(id)
        ON DELETE RESTRICT,

    FOREIGN KEY (guarantor_id)
        REFERENCES members(id)
        ON DELETE RESTRICT,

    CONSTRAINT chk_guaranteed_amount_positive
        CHECK (guaranteed_amount > 0)

);

CREATE INDEX idx_loan_guarantors_guarantor
    ON loan_guarantors (guarantor_id);

-- +goose Down

DROP INDEX idx_loan_guarantors_guarantor;
DROP TABLE loan_guarantors;
DROP TYPE IF EXISTS guarantor_status;