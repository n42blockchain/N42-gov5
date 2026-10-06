package txpool

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/chain"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/types"
)

// TestChainConfigRoundTrip covers PutChainConfig/ChainConfig: absent
// (nil, nil), round-trip, and invalid-JSON rejection.
func TestChainConfigRoundTrip(t *testing.T) {
	db := memdb.NewTestPoolDB(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	cfg, err := ChainConfig(tx)
	require.NoError(t, err)
	if cfg != nil {
		t.Fatal("expected nil config before any PutChainConfig")
	}

	want := &chain.Config{ChainName: "g49-test", ChainID: big.NewInt(12345)}
	require.NoError(t, PutChainConfig(tx, want, nil))

	got, err := ChainConfig(tx)
	require.NoError(t, err)
	if got == nil || got.ChainName != want.ChainName {
		t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
	}
	if got.ChainID == nil || got.ChainID.Cmp(want.ChainID) != 0 {
		t.Fatalf("ChainID mismatch: got %v want %v", got.ChainID, want.ChainID)
	}

	// Invalid JSON directly in the table must surface as an error.
	require.NoError(t, tx.Put(kv.PoolInfo, PoolChainConfigKey, []byte("{not json")))
	if _, err := ChainConfig(tx); err == nil {
		t.Error("expected error for invalid chain config JSON")
	}
}

// readPoolStateVersion reads the PoolStateVersion counter flush's
// fsync step increments.
func readPoolStateVersion(t *testing.T, db kv.RwDB) uint64 {
	t.Helper()
	tx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()
	v, err := tx.GetOne(kv.PoolInfo, PoolStateVersion)
	require.NoError(t, err)
	if len(v) != 8 {
		return 0
	}
	var n uint64
	for _, b := range v {
		n = n<<8 | uint64(b)
	}
	return n
}

// TestFlushAndFlushNoFsync exercises flush/flushNoFsync end to end:
// add a local txn to the pool, flush to a real MDBX-backed db, and
// confirm the txn's row lands in kv.PoolTransaction and
// PoolStateVersion increments (the "fsync" step flush adds on top of
// flushNoFsync).
func TestFlushAndFlushNoFsync(t *testing.T) {
	pool, poolDB, addr := g49NewFundedPool(t)
	ctx := context.Background()

	tx, err := poolDB.BeginRw(ctx)
	require.NoError(t, err)
	s := &types.TxSlot{
		Tip:    *uint256.NewInt(300000),
		FeeCap: *uint256.NewInt(300000),
		Gas:    100000,
		Nonce:  0,
	}
	s.IDHash[0] = 0x77
	s.Rlp = []byte{0x01, 0x02, 0x03}
	var txSlots types.TxSlots
	txSlots.Append(s, addr[:], true)
	_, err = pool.AddLocalTxs(ctx, txSlots, tx)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	written1, err := pool.flushNoFsync(ctx, poolDB)
	require.NoError(t, err)
	t.Logf("flushNoFsync wrote %d bytes dirty", written1)

	roTx, err := poolDB.BeginRo(ctx)
	require.NoError(t, err)
	v, err := roTx.GetOne(kv.PoolTransaction, s.IDHash[:])
	roTx.Rollback()
	require.NoError(t, err)
	if len(v) == 0 {
		t.Error("expected the flushed txn's row to be present in PoolTransaction")
	}

	versionBefore := readPoolStateVersion(t, poolDB)
	_, err = pool.flush(ctx, poolDB)
	require.NoError(t, err)
	versionAfter := readPoolStateVersion(t, poolDB)
	if versionAfter <= versionBefore {
		t.Errorf("expected flush's fsync step to bump PoolStateVersion: before=%d after=%d", versionBefore, versionAfter)
	}
}
