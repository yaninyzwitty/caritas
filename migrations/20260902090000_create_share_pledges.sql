-- +goose Up
CREATE TYPE share_pledge_type AS ENUM ('applicant_security', 'guarantor_security');
CREATE TYPE share_pledge_status AS ENUM ('pending', 'active', 'released', 'cancelled');

CREATE TABLE share_pledges (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    share_account_id UUID NOT NULL REFERENCES share_accounts(id) ON DELETE RESTRICT,
    loan_id UUID NOT NULL REFERENCES loans(id) ON DELETE RESTRICT,
    pledged_amount NUMERIC(19,4) NOT NULL CHECK (pledged_amount > 0),
    type share_pledge_type NOT NULL,
    status share_pledge_status NOT NULL DEFAULT 'pending',
    approved_by UUID REFERENCES staff_users(id) ON DELETE RESTRICT,
    approved_at TIMESTAMPTZ,
    released_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (share_account_id, loan_id),
    CONSTRAINT chk_share_pledge_approval CHECK (
        (status IN ('active', 'released') AND approved_by IS NOT NULL AND approved_at IS NOT NULL)
        OR
        (status IN ('pending', 'cancelled') AND approved_by IS NULL AND approved_at IS NULL)
    ),
    CONSTRAINT chk_share_pledge_release CHECK (
        (status = 'released' AND released_at IS NOT NULL)
        OR
        (status <> 'released' AND released_at IS NULL)
    ),
    CONSTRAINT chk_share_pledge_timestamp_order CHECK (
        (approved_at IS NULL OR approved_at >= created_at)
        AND
        (released_at IS NULL OR released_at >= approved_at)
    )
);

CREATE INDEX idx_share_pledges_account_active
    ON share_pledges (share_account_id)
    WHERE status = 'active';

-- +goose Down
DROP INDEX IF EXISTS idx_share_pledges_account_active;
DROP TABLE IF EXISTS share_pledges;
DROP TYPE IF EXISTS share_pledge_status;
DROP TYPE IF EXISTS share_pledge_type;
