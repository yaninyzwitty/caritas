package share

import (
	"math/big"
	"testing"
)

// TestGuaranteeAvailable protects the monetary boundary, especially fractional
// shares where rounding up would reserve more than the permitted 30%.
func TestGuaranteeAvailable(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		balance, pledged, want int64
	}{
		{"unpledged", 10_000_000_000_000, 0, 3_000_000_000_000},
		{"existing pledges", 10_000_000_000_000, 1_000_000_000_000, 2_000_000_000_000},
		{"fully pledged allowance", 10_000_000_000_000, 3_000_000_000_000, 0},
		{"above allowance", 10_000_000_000_000, 4_000_000_000_000, 0},
		{"round down to ledger precision", 10_000_100_000, 0, 3_000_000_000},
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
