-- name: CreateSharePledge :one
INSERT INTO share_pledges (share_account_id, loan_id, pledged_amount, type)
VALUES ($1, $2, $3, $4)
ON CONFLICT (share_account_id, loan_id) DO NOTHING
RETURNING id, share_account_id, loan_id, pledged_amount, type, status, approved_by, approved_at, released_at, created_at, updated_at;

-- name: GetApplicantSharePledge :one
SELECT id, share_account_id, loan_id, pledged_amount, type, status, approved_by, approved_at, released_at, created_at, updated_at
FROM share_pledges
WHERE loan_id = $1 AND type = 'applicant_security';

-- name: ActivateSharePledge :one
UPDATE share_pledges
SET status = 'active', approved_by = $2, approved_at = NOW(), updated_at = NOW()
WHERE id = $1 AND status = 'pending'
RETURNING id, share_account_id, loan_id, pledged_amount, type, status, approved_by, approved_at, released_at, created_at, updated_at;

-- name: GetActivePledgedAmount :one
SELECT COALESCE(SUM(pledged_amount), 0)::NUMERIC AS pledged_amount
FROM share_pledges
WHERE share_account_id = $1 AND status = 'active';

-- name: GetActiveApplicantPledgeAmount :one
SELECT COALESCE(SUM(pledged_amount), 0)::NUMERIC AS pledged_amount
FROM share_pledges
WHERE loan_id = $1 AND type = 'applicant_security' AND status = 'active';

-- name: ReleaseApplicantSharePledge :exec
UPDATE share_pledges
SET status = CASE WHEN status = 'pending' THEN 'cancelled'::share_pledge_status ELSE 'released'::share_pledge_status END,
    released_at = CASE WHEN status = 'active' THEN NOW() ELSE released_at END,
    updated_at = NOW()
WHERE loan_id = $1
  AND type = 'applicant_security'
  AND status IN ('pending', 'active');
