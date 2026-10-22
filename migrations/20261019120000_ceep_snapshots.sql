-- +goose Up
-- Metadata and member rows are saved together. Separate rows enforce one finalized
-- snapshot per member/month; a JSON-only report cannot enforce that invariant.
CREATE TABLE ceep_reports (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    branch_id BIGINT NOT NULL,
    period DATE NOT NULL CHECK (EXTRACT(DAY FROM period) = 1),
    metadata JSONB NOT NULL,
    finalized_by UUID NOT NULL REFERENCES staff_users(id),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (branch_id, period)
);
CREATE TABLE ceep_snapshots (
    report_id UUID NOT NULL REFERENCES ceep_reports(id),
    member_id UUID NOT NULL REFERENCES members(id),
    period DATE NOT NULL,
    data JSONB NOT NULL,
    PRIMARY KEY (member_id, period)
);
CREATE INDEX idx_ceep_snapshots_report ON ceep_snapshots (report_id);

-- The API has no edit path, but database enforcement also protects finalized
-- reports from accidental maintenance updates that would change later exports.
-- +goose StatementBegin
CREATE FUNCTION reject_ceep_snapshot_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'Finalized CEEP records are immutable';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER ceep_reports_immutable BEFORE UPDATE OR DELETE ON ceep_reports
    FOR EACH ROW EXECUTE FUNCTION reject_ceep_snapshot_mutation();
CREATE TRIGGER ceep_snapshots_immutable BEFORE UPDATE OR DELETE ON ceep_snapshots
    FOR EACH ROW EXECUTE FUNCTION reject_ceep_snapshot_mutation();

-- +goose Down
DROP TABLE ceep_snapshots;
DROP TABLE ceep_reports;
DROP FUNCTION reject_ceep_snapshot_mutation();
