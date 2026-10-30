package txpool

import (
	"testing"

	"github.com/n42blockchain/N42/lib/common/fixedgas"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

// The EIP-7825 cap applies from Osaka; Glamsterdam replaces it with the
// EIP-8037 cap, so a transaction above 2^24 gas is then admitted.
func TestValidateTxGasCapByFork(t *testing.T) {
	zero := uint64(0)
	osaka := &TxPool{shanghaiTime: &zero, pragueTime: &zero, osakaTime: &zero}
	glamsterdam := &TxPool{shanghaiTime: &zero, pragueTime: &zero, osakaTime: &zero, glamsterdamTime: &zero}

	// A SetCode transaction without authorizations is rejected right after
	// the cap check, before any state is read.
	txn := func(gas uint64) *types.TxSlot {
		return &types.TxSlot{Type: types.SetCodeTxType, Gas: gas}
	}
	for _, tc := range []struct {
		name string
		pool *TxPool
		gas  uint64
		want txpoolcfg.DiscardReason
	}{
		{"osaka at cap", osaka, fixedgas.MaxTxnGasLimit, txpoolcfg.NoAuthorizations},
		{"osaka above cap", osaka, fixedgas.MaxTxnGasLimit + 1, txpoolcfg.GasLimitTooHigh},
		{"glamsterdam above 2^24", glamsterdam, fixedgas.MaxTxnGasLimit + 1, txpoolcfg.NoAuthorizations},
		{"glamsterdam at cap", glamsterdam, fixedgas.MaxTxnGasLimitEIP8037, txpoolcfg.NoAuthorizations},
		{"glamsterdam above cap", glamsterdam, fixedgas.MaxTxnGasLimitEIP8037 + 1, txpoolcfg.GasLimitTooHigh},
	} {
		if got := tc.pool.validateTx(txn(tc.gas), true, nil); got != tc.want {
			t.Errorf("%s: validateTx = %s, want %s", tc.name, got, tc.want)
		}
	}
}
