-- +goose Up
CREATE TABLE dividend_drafts (
    branch_id BIGINT NOT NULL,
    financial_year INTEGER NOT NULL CHECK (financial_year BETWEEN 2000 AND 9998),
    declaration_date DATE NOT NULL,
    rate NUMERIC(7,4) NOT NULL CHECK (rate > 0 AND rate <= 100),
    updated_by UUID NOT NULL REFERENCES staff_users(id),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (branch_id, financial_year),
    CHECK (declaration_date > make_date(financial_year, 12, 31))
);

CREATE TABLE dividend_draft_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    branch_id BIGINT NOT NULL,
    financial_year INTEGER NOT NULL,
    actor_id UUID NOT NULL REFERENCES staff_users(id),
    changed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    reason TEXT NOT NULL CHECK (btrim(reason) <> ''),
    before_rate NUMERIC(7,4),
    before_declaration_date DATE,
    after_rate NUMERIC(7,4) NOT NULL,
    after_declaration_date DATE NOT NULL,
    FOREIGN KEY (branch_id, financial_year) REFERENCES dividend_drafts(branch_id, financial_year)
);
CREATE INDEX dividend_draft_history_lookup ON dividend_draft_history(branch_id, financial_year, changed_at DESC, id DESC);

-- +goose StatementBegin
CREATE FUNCTION protect_dividend_draft_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'dividend draft history is immutable';
END $$;
-- +goose StatementEnd
CREATE TRIGGER protect_dividend_draft_history_before_write
BEFORE UPDATE OR DELETE ON dividend_draft_history FOR EACH ROW EXECUTE FUNCTION protect_dividend_draft_history();

-- +goose Down
DROP TRIGGER protect_dividend_draft_history_before_write ON dividend_draft_history;
DROP FUNCTION protect_dividend_draft_history();
DROP TABLE dividend_draft_history;
DROP TABLE dividend_drafts;
