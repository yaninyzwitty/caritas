-- +goose Up
CREATE TABLE staff_users (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    branch_id BIGINT NOT NULL,
    email TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role TEXT NOT NULL CHECK (role IN ('system_admin', 'manager', 'loan_officer', 'cashier', 'auditor', 'chairperson', 'secretary')),
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT staff_users_email_lowercase CHECK (email = lower(email)),
    CONSTRAINT staff_users_email_key UNIQUE (email)
);

CREATE INDEX idx_staff_users_active ON staff_users (id) WHERE is_active = TRUE;

-- +goose Down
DROP INDEX IF EXISTS idx_staff_users_active;
DROP TABLE IF EXISTS staff_users;
