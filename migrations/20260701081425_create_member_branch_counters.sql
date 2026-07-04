-- +goose Up
CREATE TABLE member_branch_counters (
    branch_id BIGINT PRIMARY KEY,
    next_member_number BIGINT NOT NULL DEFAULT 1,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE IF EXISTS member_branch_counters;