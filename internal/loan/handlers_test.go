package loan

import (
	"math/big"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestNumericToStringAppliesNumericExponent(t *testing.T) {
	tests := []struct {
		name string
		in   pgtype.Numeric
		want string
	}{
		{
			name: "whole money",
			in:   pgtype.Numeric{Int: big.NewInt(50000), Exp: -2, Valid: true},
			want: "500",
		},
		{
			name: "fractional money",
			in:   pgtype.Numeric{Int: big.NewInt(50050), Exp: -2, Valid: true},
			want: "500.5",
		},
		{
			name: "zero",
			in:   pgtype.Numeric{Int: big.NewInt(0), Exp: -4, Valid: true},
			want: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := numericToString(tt.in); got != tt.want {
				t.Fatalf("numericToString() = %q, want %q", got, tt.want)
			}
		})
	}
}
