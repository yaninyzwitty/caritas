-- +goose Up
CREATE TABLE member_status_history (
    id BIGSERIAL PRIMARY KEY,
    member_id BIGINT NOT NULL,
    from_status TEXT,
    to_status TEXT NOT NULL,
    reason TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT member_status_history_member_id_fkey FOREIGN KEY (member_id) REFERENCES members(id)
);

CREATE INDEX idx_member_status_history_member_id ON member_status_history (member_id);

-- +goose Down
DROP INDEX IF EXISTS idx_member_status_history_member_id;
DROP TABLE IF EXISTS member_status_history;