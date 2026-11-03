package txspool

import (
	"errors"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/common/fixedgas"
	"github.com/n42blockchain/N42/params"
)

// The pool applies the per-transaction gas cap the state transition enforces:
// EIP-7825's 2^24 from Osaka, and EIP-8037's TX_MAX_TOTAL_GAS_LIMIT from
// Glamsterdam. Without it a transaction above the cap sits in the pool and
// fails every block build.
func TestValidateTx_TransactionGasCapByFork(t *testing.T) {
	osaka := *params.TestChainConfig
	osaka.OsakaTime = big.NewInt(0)
	osaka.GlamsterdamTime = nil
	amsterdam := osaka
	amsterdam.GlamsterdamTime = big.NewInt(0)
	preOsaka := osaka
	preOsaka.OsakaTime = nil

	for _, tc := range []struct {
		name    string
		config  *params.ChainConfig
		gas     uint64
		wantErr error
	}{
		{"pre-osaka above 2^24", &preOsaka, fixedgas.MaxTxnGasLimit + 1, nil},
		{"osaka at 2^24", &osaka, fixedgas.MaxTxnGasLimit, nil},
		{"osaka above 2^24", &osaka, fixedgas.MaxTxnGasLimit + 1, internal.ErrGasLimitTooHigh},
		{"amsterdam above 2^24", &amsterdam, fixedgas.MaxTxnGasLimit + 1, nil},
		{"amsterdam at total cap", &amsterdam, params.MaxTxTotalGasEIP8037, nil},
		{"amsterdam above total cap", &amsterdam, params.MaxTxTotalGasEIP8037 + 1, internal.ErrGasLimitTooHigh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := g49NewValidationPool()
			pool.chainconfig = tc.config
			pool.currentMaxGas = 1 << 40

			key, from := generateTestKey()
			pool.currentState.(*mockReadState).balances[from] = new(uint256.Int).Lsh(uint256.NewInt(1), 100)
			inner := &transaction.LegacyTx{
				GasPrice: uint256.NewInt(1),
				Gas:      tc.gas,
				To:       &from,
				Value:    uint256.NewInt(0),
			}
			tx, err := transaction.SignTx(transaction.NewTx(inner), transaction.NewLondonSigner(big.NewInt(1)), key)
			if err != nil {
				t.Fatalf("SignTx: %v", err)
			}
			tx.SetFrom(from)

			err = pool.validateTx(tx, true, nil)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
			} else if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
