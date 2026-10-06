package internal

import (
	"context"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// Test-only bridge lets the external test use the real mobile read recorder,
// whose evmsdk dependency imports this package. No production API is added.
func AccountPrefetchReaderForTest(ctx context.Context, db kv.RwDB, tx kv.Tx, addresses []types.Address, workers int) (state.StateReader, error) {
	values, err := readSnapshotAccounts(ctx, db, tx.ViewID(), addresses, workers)
	if err != nil {
		return nil, err
	}
	return &blockAccountPrefetchReader{PlainStateReader: state.NewPlainStateReader(tx), accounts: values}, nil
}
func AccountPrefetchHitsForTest(reader state.StateReader) uint64 {
	return reader.(*blockAccountPrefetchReader).hits
}
func AccountPrefetchChainConfigForTest() *params.ChainConfig { return testStateTransitionChainConfig() }
