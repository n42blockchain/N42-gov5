package ethel

import (
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"context"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func TestProcessBlockEmpty(t *testing.T) {
	// Process a block with no transactions (like early Ethereum blocks).
	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	header := &block.Header{
		Number:     uint256.NewInt(1),
		GasLimit:   5000,
		Difficulty: uint256.NewInt(17179869184),
		Time:       1438269988,
		Coinbase:   types.HexToAddress("0x05a56E2D52c817161883f50c441c3228CFe54d9f"),
	}

	db := memdb.New(t.TempDir())
	t.Cleanup(db.Close)
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()
	ibs := state.New(state.NewPlainStateReader(tx))
	blockHashFunc := func(n uint64) types.Hash { return types.Hash{} }

	result, err := ProcessBlock(chainCfg, engine, header,
		[]*transaction.Transaction{}, nil, ibs, blockHashFunc, nil)
	if err != nil {
		t.Fatalf("ProcessBlock empty: %v", err)
	}
	if result.GasUsed != 0 {
		t.Errorf("gasUsed: got %d, want 0", result.GasUsed)
	}
	if len(result.Receipts) != 0 {
		t.Errorf("receipts: got %d, want 0", len(result.Receipts))
	}
	if len(result.Senders) != 0 {
		t.Errorf("senders: got %d, want 0", len(result.Senders))
	}
}
