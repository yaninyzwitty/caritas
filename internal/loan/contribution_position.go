package loan

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
	loansqlc "github.com/yaninyzwitty/caritas-backend/internal/loan/repository/sqlc"
)

// ContributionLoanSummary keeps obligations separate from this receipt's payment.
// Receipt snapshots retain these values so subsequent repayments cannot rewrite history.
type ContributionLoanSummary struct {
	LoanID             string `json:"loan_id"`
	Period             string `json:"period"`
	OpeningPrincipal   string `json:"opening_principal"`
	InterestRate       string `json:"interest_rate"`
	PeriodInterest     string `json:"period_interest"`
	EarlierInterestDue string `json:"earlier_interest_due"`
	InterestDue        string `json:"interest_due"`
	PenaltyDue         string `json:"penalty_due"`
	PrincipalDue       string `json:"principal_due"`
	Payment            string `json:"payment"`
	PenaltyPaid        string `json:"penalty_paid"`
	InterestPaid       string `json:"interest_paid"`
	PrincipalPaid      string `json:"principal_paid"`
	Credit             string `json:"credit"`
	InterestRemaining  string `json:"interest_remaining"`
	PrincipalRemaining string `json:"principal_remaining"`
	Payable            bool   `json:"payable"`
}

// QuoteContributionLoans previews the same catch-up interest and period gates as
// RecordRepaymentForPeriodInTx, including loans with no proposed payment.
func QuoteContributionLoans(ctx context.Context, q loansqlc.Querier, memberID pgtype.UUID, branchID int64, period pgtype.Date) ([]ContributionLoanSummary, error) {
	rows, err := q.ListContributionLoanPositions(ctx, loansqlc.ListContributionLoanPositionsParams{MemberID: memberID, BranchID: branchID, Period: period})
	if err != nil {
		return nil, fmt.Errorf("quote loan interest: %w", err)
	}
	result := make([]ContributionLoanSummary, 0, len(rows))
	for _, row := range rows {
		result = append(result, ContributionLoanSummary{
			LoanID: row.ID.String(), Period: period.Time.Format("2006-01-02"),
			OpeningPrincipal: numericToString(row.OpeningPrincipal), InterestRate: numericToString(row.InterestRate),
			PeriodInterest: numericToString(row.PeriodInterest), EarlierInterestDue: numericToString(row.EarlierInterestDue),
			InterestDue: numericToString(row.InterestDue), PenaltyDue: numericToString(row.PenaltyDue), PrincipalDue: numericToString(row.PrincipalDue),
			Payment: "0", PenaltyPaid: "0", InterestPaid: "0", PrincipalPaid: "0", Credit: "0",
			InterestRemaining: numericToString(row.InterestDue), PrincipalRemaining: numericToString(row.PrincipalDue), Payable: row.Payable,
		})
	}
	return result, nil
}

// CompleteContributionLoanSnapshot uses the immutable repayment transactions,
// rather than the browser's estimate, before the receipt transaction commits.
func CompleteContributionLoanSnapshot(ctx context.Context, q loansqlc.Querier, summaries []ContributionLoanSummary, allocationIDs []string) ([]byte, error) {
	if len(allocationIDs) > 0 {
		payments, err := q.ListContributionLoanRepayments(ctx, allocationIDs)
		if err != nil {
			return nil, fmt.Errorf("read contribution repayments: %w", err)
		}
		if len(payments) != len(allocationIDs) {
			return nil, fmt.Errorf("contribution repayment snapshot is incomplete")
		}
		for _, payment := range payments {
			var split repaymentAllocation
			if err := json.Unmarshal(payment.AllocationBreakdown, &split); err != nil {
				return nil, fmt.Errorf("decode contribution repayment: %w", err)
			}
			found := false
			for i := range summaries {
				item := &summaries[i]
				if item.LoanID != payment.LoanID.String() {
					continue
				}
				found = true
				item.Payment = numericToString(payment.Amount)
				item.PenaltyPaid, item.InterestPaid, item.PrincipalPaid, item.Credit = split.Penalty, split.Interest, split.Principal, split.Credit
				for _, pair := range [][2]*string{{&item.InterestRemaining, &item.InterestPaid}, {&item.PrincipalRemaining, &item.PrincipalPaid}} {
					var due, paid pgtype.Numeric
					if err := due.Scan(*pair[0]); err != nil {
						return nil, err
					}
					if err := paid.Scan(*pair[1]); err != nil {
						return nil, err
					}
					remaining := new(big.Int).Sub(numericToScale(due, -4), numericToScale(paid, -4))
					if remaining.Sign() < 0 {
						return nil, fmt.Errorf("contribution repayment exceeds quoted balance")
					}
					*pair[0] = decimalStringFromScale(remaining, -4)
				}
			}
			if !found {
				return nil, fmt.Errorf("contribution repayment has no loan position")
			}
		}
	}
	return json.Marshal(summaries)
}
