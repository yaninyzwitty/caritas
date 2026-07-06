-- +goose Up
INSERT INTO member_branch_counters (branch_id, next_member_number)
VALUES (1, 1)
ON CONFLICT (branch_id) DO NOTHING;

-- +goose Down
DELETE FROM member_branch_counters WHERE branch_id = 1;