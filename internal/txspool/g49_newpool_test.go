package txspool

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

// g49ConstructableChainStub adds Config()/DB() on top of the existing
// txpoolChainStub (block_number_test.go) so NewTxsPoolWithConfig can
// actually construct a pool: it calls bc.Config(), bc.DB() (fed into
// StateClient), and bc.CurrentBlock() (fed into reset()).
type g49ConstructableChainStub struct {
	txpoolChainStub
	db kv.RwDB
}

func (c *g49ConstructableChainStub) Config() *params.ChainConfig { return params.TestChainConfig }
func (c *g49ConstructableChainStub) DB() kv.RwDB                 { return c.db }

// TestNewTxsPoolWithConfig_ConstructsAndStops drives the full
// production constructor (goroutines, journal restore, dynamic sizing
// off) against a minimal stub chain backed by a real (temp-dir) MDBX
// pool db, then stops it cleanly.
func TestNewTxsPoolWithConfig_ConstructsAndStops(t *testing.T) {
	current := &txpoolBlockStub{
		header:   &txpoolHeaderStub{number: uint256.NewInt(1), hash: types.HexToHash("0x1")},
		body:     &txpoolBodyStub{},
		gasLimit: 30_000_000,
	}
	stub := &g49ConstructableChainStub{
		txpoolChainStub: txpoolChainStub{current: current, blocks: map[types.Hash]block.IBlock{}},
		db:              memdb.NewTestPoolDB(t),
	}

	cfg := DefaultTxPoolConfig
	cfg.DynamicSizing = false
	cfg.NoLocals = true // skip the journal file entirely

	pool, err := NewTxsPoolWithConfig(context.Background(), stub, nil, cfg)
	if err != nil {
		t.Fatalf("NewTxsPoolWithConfig: %v", err)
	}
	if pool == nil {
		t.Fatal("expected a non-nil pool")
	}

	if err := pool.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}
