package contribution

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestDividendVoucherError verifies that only the cash-reference constraint is
// exposed as a duplicate payment; unrelated database failures must stay private.
func TestDividendVoucherError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		code       string
		constraint string
		want       codes.Code
	}{
		{"duplicate voucher", "23505", "dividend_cash_voucher_branch_key", codes.AlreadyExists},
		{"different constraint", "23505", "other_constraint", codes.Internal},
		{"different error", "23503", "dividend_cash_voucher_branch_key", codes.Internal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := dividendResponse(t.Context(), nil, true, fmt.Errorf("exec tx: %w", &pgconn.PgError{
				Code: tc.code, ConstraintName: tc.constraint, Message: "private database detail",
			}))
			if status.Code(err) != tc.want {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.want == codes.AlreadyExists && status.Convert(err).Message() != "this cash payment reference has already been used; use a unique voucher number or register page and row within your branch" {
				t.Fatalf("missing actionable duplicate-reference message: %v", err)
			}
		})
	}
}
