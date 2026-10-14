-- +goose Up
-- Repair reservations left behind by the old guarantor-removal and loan-close paths.
UPDATE share_pledges sp
SET status = CASE WHEN sp.status = 'pending' THEN 'cancelled'::share_pledge_status ELSE 'released'::share_pledge_status END,
    released_at = CASE WHEN sp.status = 'active' THEN NOW() ELSE sp.released_at END,
    updated_at = NOW()
FROM loans l
WHERE l.id = sp.loan_id AND sp.status IN ('pending', 'active')
  AND (l.status IN ('closed', 'rejected') OR (
      sp.type = 'guarantor_security' AND EXISTS (
          SELECT 1 FROM loan_guarantors lg
          JOIN share_accounts sa ON sa.member_id = lg.guarantor_id
          WHERE lg.loan_id = sp.loan_id AND lg.status = 'rejected'
            AND sa.id = sp.share_account_id
      )
  ));

-- +goose Down
-- Released collateral cannot safely be reserved again: it may already secure another loan.
SELECT 1;
