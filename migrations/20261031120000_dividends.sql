-- +goose Up
CREATE TABLE dividend_declarations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    branch_id BIGINT NOT NULL,
    financial_year INTEGER NOT NULL CHECK (financial_year BETWEEN 2000 AND 9998),
    declaration_date DATE NOT NULL,
    rate NUMERIC(7,4) NOT NULL CHECK (rate > 0 AND rate <= 100),
    approved_total NUMERIC(19,2) NOT NULL CHECK (approved_total >= 0),
    resolution_reference TEXT NOT NULL CHECK (btrim(resolution_reference) <> ''),
    preview_hash TEXT NOT NULL, -- Hash of calculation preview used during the approval process
    approved_by UUID NOT NULL REFERENCES staff_users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (branch_id, financial_year),
    CHECK (declaration_date > make_date(financial_year, 12, 31))
);

ALTER TABLE dividend_declarations ADD CONSTRAINT dividend_declarations_id_branch_key UNIQUE (id, branch_id);
ALTER TABLE members ADD CONSTRAINT members_id_branch_key UNIQUE (id, branch_id);
ALTER TABLE share_accounts ADD CONSTRAINT share_accounts_id_member_branch_key UNIQUE (id, member_id, branch_id);

CREATE TABLE dividend_allocations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    declaration_id UUID NOT NULL REFERENCES dividend_declarations(id),
    member_id UUID NOT NULL REFERENCES members(id),
    branch_id BIGINT NOT NULL,
    share_account_id UUID REFERENCES share_accounts(id),
    evidence JSONB NOT NULL,
    eligible BOOLEAN NOT NULL,
    share_balance NUMERIC(19,4) NOT NULL CHECK (share_balance >= 0),
    amount NUMERIC(19,2) NOT NULL CHECK (amount >= 0),
    choice TEXT CHECK (choice IN ('cash', 'shares')),
    choice_by UUID REFERENCES staff_users(id),
    choice_at TIMESTAMPTZ,
    voucher_reference TEXT,
    payment_date DATE,
    posted_transaction_id UUID UNIQUE REFERENCES share_transactions(id),
    settled_by UUID REFERENCES staff_users(id),
    settled_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (declaration_id, member_id),
    FOREIGN KEY (declaration_id, branch_id) REFERENCES dividend_declarations(id, branch_id),
    FOREIGN KEY (member_id, branch_id) REFERENCES members(id, branch_id),
    FOREIGN KEY (share_account_id, member_id, branch_id) REFERENCES share_accounts(id, member_id, branch_id),
    CHECK (eligible OR amount = 0),
    CHECK (NOT eligible OR share_account_id IS NOT NULL),
    CHECK (choice IS NOT NULL OR (choice_by IS NULL AND choice_at IS NULL)),
    CHECK (choice IS NULL OR (eligible AND amount > 0 AND choice_by IS NOT NULL AND choice_at IS NOT NULL)),
    CHECK (settled_at IS NOT NULL OR (voucher_reference IS NULL AND payment_date IS NULL AND posted_transaction_id IS NULL AND settled_by IS NULL)),
    CHECK (settled_at IS NULL OR (eligible AND amount > 0 AND settled_by IS NOT NULL AND choice IS NOT NULL AND
        ((choice = 'cash' AND voucher_reference IS NOT NULL AND btrim(voucher_reference) <> '' AND payment_date IS NOT NULL AND posted_transaction_id IS NULL)
        OR (choice = 'shares' AND posted_transaction_id IS NOT NULL AND voucher_reference IS NULL AND payment_date IS NULL))))
);

CREATE UNIQUE INDEX dividend_cash_voucher_branch_key ON dividend_allocations (branch_id, voucher_reference)
    WHERE voucher_reference IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION check_dividend_total() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE declaration UUID;
BEGIN
    IF TG_TABLE_NAME = 'dividend_declarations' THEN
        declaration := NEW.id;
    ELSIF TG_OP = 'DELETE' THEN
        declaration := OLD.declaration_id;
    ELSE
        declaration := NEW.declaration_id;
    END IF;
    IF EXISTS (
        SELECT 1 FROM dividend_declarations d
        WHERE d.id = declaration AND d.approved_total <> COALESCE(
            (SELECT SUM(a.amount) FROM dividend_allocations a WHERE a.declaration_id = d.id), 0)
    ) THEN
        RAISE EXCEPTION 'dividend allocations do not equal approved total';
    END IF;
    RETURN NULL;
END $$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER dividend_total_on_declaration
AFTER INSERT OR UPDATE ON dividend_declarations DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION check_dividend_total();
CREATE CONSTRAINT TRIGGER dividend_total_on_allocation
AFTER INSERT OR UPDATE OR DELETE ON dividend_allocations DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION check_dividend_total();

-- +goose StatementBegin
CREATE FUNCTION protect_dividend_declaration() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'approved dividend declaration is immutable';
END $$;
-- +goose StatementEnd
CREATE TRIGGER protect_dividend_declaration_before_write
BEFORE UPDATE OR DELETE ON dividend_declarations FOR EACH ROW EXECUTE FUNCTION protect_dividend_declaration();

-- +goose StatementBegin
CREATE FUNCTION protect_dividend_allocation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'approved dividend allocation cannot be deleted';
    END IF;
    IF ROW(NEW.id, NEW.declaration_id, NEW.member_id, NEW.branch_id, NEW.share_account_id, NEW.evidence,
           NEW.eligible, NEW.share_balance, NEW.amount, NEW.created_at) IS DISTINCT FROM
       ROW(OLD.id, OLD.declaration_id, OLD.member_id, OLD.branch_id, OLD.share_account_id, OLD.evidence,
           OLD.eligible, OLD.share_balance, OLD.amount, OLD.created_at) THEN
        RAISE EXCEPTION 'approved dividend allocation is immutable';
    END IF;
    IF OLD.settled_at IS NOT NULL THEN
        RAISE EXCEPTION 'settled dividend allocation is immutable';
    END IF;
    IF OLD.choice IS NOT NULL AND ROW(NEW.choice, NEW.choice_by, NEW.choice_at) IS DISTINCT FROM
       ROW(OLD.choice, OLD.choice_by, OLD.choice_at) THEN
        RAISE EXCEPTION 'dividend choice is immutable';
    END IF;
    IF NEW.choice IS DISTINCT FROM OLD.choice AND
       (OLD.choice IS NOT NULL OR NEW.settled_at IS NOT NULL) THEN
        RAISE EXCEPTION 'invalid dividend choice transition';
    END IF;
    RETURN NEW;
END $$;
-- +goose StatementEnd
CREATE TRIGGER protect_dividend_allocation_before_write
BEFORE UPDATE OR DELETE ON dividend_allocations FOR EACH ROW EXECUTE FUNCTION protect_dividend_allocation();

-- +goose Down
DROP TRIGGER protect_dividend_allocation_before_write ON dividend_allocations;
DROP FUNCTION protect_dividend_allocation();
DROP TRIGGER protect_dividend_declaration_before_write ON dividend_declarations;
DROP FUNCTION protect_dividend_declaration();
DROP TRIGGER dividend_total_on_allocation ON dividend_allocations;
DROP TRIGGER dividend_total_on_declaration ON dividend_declarations;
DROP FUNCTION check_dividend_total();
DROP TABLE dividend_allocations;
DROP TABLE dividend_declarations;
ALTER TABLE share_accounts DROP CONSTRAINT share_accounts_id_member_branch_key;
ALTER TABLE members DROP CONSTRAINT members_id_branch_key;
