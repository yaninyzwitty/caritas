package share

import (
	"math/big"
	"testing"
)

// TestGuaranteeAvailable protects the monetary boundary, especially fractional
// shares where rounding up would reserve more than the available balance.
func TestGuaranteeAvailable(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		balance, pledged, want int64
	}{
		{"unpledged", 10_000_000_000_000, 0, 10_000_000_000_000},
		{"existing pledges", 10_000_000_000_000, 1_000_000_000_000, 9_000_000_000_000},
		{"fully pledged", 10_000_000_000_000, 10_000_000_000_000, 0},
		{"overpledged", 10_000_000_000_000, 11_000_000_000_000, 0},
		{"round down to ledger precision", 10_000_199_999, 0, 10_000_100_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			balance, pledged := big.NewInt(tc.balance), big.NewInt(tc.pledged)
			if got := guaranteeAvailable(balance, pledged); got.Cmp(big.NewInt(tc.want)) != 0 {
				t.Fatalf("available nanos = %s, want %d", got, tc.want)
			}
			if balance.Int64() != tc.balance || pledged.Int64() != tc.pledged {
				t.Fatal("capacity calculation mutated its inputs")
			}
		})
	}
}
