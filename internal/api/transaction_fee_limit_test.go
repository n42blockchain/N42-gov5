package api

import (
	"testing"

	"github.com/holiman/uint256"
)

func TestRPCGasPriceCeiling(t *testing.T) {
	for _, tc := range []struct {
		name    string
		price   *uint256.Int
		ceiling uint64
		wantErr bool
	}{
		{"zero price", new(uint256.Int), 0, false},
		{"default boundary", uint256.NewInt(defaultRPCMaxGasPriceWei), 0, false},
		{"default exceeded", uint256.NewInt(defaultRPCMaxGasPriceWei + 1), 0, true},
		{"flagship default rejected", uint256.NewInt(100000000000000), 0, true},
		{"flagship explicit accepted", uint256.NewInt(100000000000000), 100000000000000, false},
		{"explicit exceeded", uint256.NewInt(100000000000001), 100000000000000, true},
		{"stricter operator ceiling", uint256.NewInt(1001), 1000, true},
		{"price cannot truncate to uint64", new(uint256.Int).Lsh(uint256.NewInt(1), 128), ^uint64(0), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkTxFee(*tc.price, tc.ceiling)
			if (err != nil) != tc.wantErr {
				t.Fatalf("checkTxFee error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
