package txspool

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/params"
)

// Mirror the binary endpoint's native decode + sender trailer assignment at
// the actual pool validation boundary. Ingest's mock-pool transport tests
// intentionally accept unsigned data and cannot establish this property.
func TestNativeIngestSenderClaimRequiresSignature(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := types.HexToAddress("0x1234")
	for _, tc := range []struct {
		name     string
		chain    int64
		forged   bool
		unsigned bool
		want     bool
	}{
		{"valid", 1, false, false, true},
		{"forged trailer", 1, true, false, false},
		{"wrong chain", 94, false, false, false},
		{"unsigned", 1, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			txn := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(9), nil)
			if !tc.unsigned {
				txn, err = transaction.SignTx(txn, transaction.NewLondonSigner(big.NewInt(tc.chain)), key)
				if err != nil {
					t.Fatal(err)
				}
			}
			raw, err := txn.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			wire := new(transaction.Transaction)
			if err := wire.Unmarshal(raw); err != nil {
				t.Fatal(err)
			}
			claim := from
			if tc.forged {
				claim[0] ^= 1
			}
			wire.SetFrom(claim)
			pool := &TxsPool{chainconfig: &params.ChainConfig{ChainID: big.NewInt(1)}}
			if got := pool.validateSender(wire); got != tc.want {
				t.Fatalf("sender claim accepted=%v, want %v", got, tc.want)
			}
		})
	}
}
