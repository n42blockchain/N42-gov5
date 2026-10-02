package txpool

import (
	"context"
	"testing"

	mapset "github.com/deckarep/golang-set/v2"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/gointerfaces"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/kvcache"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

// g49NewFundedPool mirrors TestNonceFromAddress's setup: a pool with
// OnNewBlock applied for one funded address, ready to accept local
// txns. Returns the pool, its memdb-backed pool db, and the funded
// address.
func g49NewFundedPool(t *testing.T) (*TxPool, kv.RwDB, [20]byte) {
	t.Helper()
	ch := make(chan types.Announcements, 100)
	db, coreDB := memdb.NewTestPoolDB(t), memdb.NewTestDB(t)

	cfg := txpoolcfg.DefaultConfig
	sendersCache := kvcache.New(kvcache.DefaultCoherentConfig)
	pool, err := New(ch, coreDB, cfg, sendersCache, *uint256.NewInt(1), nil, nil, nil, nil, nil, nil, nil, nil, log.New())
	require.NoError(t, err)
	require.NotNil(t, pool)

	ctx := context.Background()
	h1 := gointerfaces.ConvertHashToH256([32]byte{})
	change := &remote.StateChangeBatch{
		StateVersionId:      0,
		PendingBlockBaseFee: 200000,
		BlockGasLimit:       1000000,
		ChangeBatch: []*remote.StateChange{
			{BlockHeight: 0, BlockHash: h1},
		},
	}
	var addr [20]byte
	addr[0] = 0x42
	v := make([]byte, types.EncodeSenderLengthForStorage(0, *uint256.NewInt(10*common.Ether)))
	types.EncodeSender(0, *uint256.NewInt(10*common.Ether), v)
	change.ChangeBatch[0].Changes = append(change.ChangeBatch[0].Changes, &remote.AccountChange{
		Action:  remote.Action_UPSERT,
		Address: gointerfaces.ConvertAddressToH160(addr),
		Data:    v,
	})

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()
	require.NoError(t, pool.OnNewBlock(ctx, change, types.TxSlots{}, types.TxSlots{}, types.TxSlots{}, tx))

	return pool, db, addr
}

// TestSelection_YieldBestPeekBestCountContent populates pending with a
// few local txns, then exercises YieldBest/PeekBest/CountContent —
// the three selection.go entry points miners/the block builder call.
func TestSelection_YieldBestPeekBestCountContent(t *testing.T) {
	pool, db, addr := g49NewFundedPool(t)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	var txSlots types.TxSlots
	for i, nonce := range []uint64{0, 1, 2} {
		s := &types.TxSlot{
			Tip:    *uint256.NewInt(300000),
			FeeCap: *uint256.NewInt(300000),
			Gas:    100000,
			Nonce:  nonce,
		}
		s.IDHash[0] = byte(i + 1)
		s.Rlp = []byte{0xde, 0xad, byte(i + 1)}
		txSlots.Append(s, addr[:], true)
	}
	reasons, err := pool.AddLocalTxs(ctx, txSlots, tx)
	require.NoError(t, err)
	for _, r := range reasons {
		assert.Equal(t, txpoolcfg.Success, r, r.String())
	}

	pending, baseFeeN, queued := pool.CountContent()
	total := pending + baseFeeN + queued
	if total == 0 {
		t.Fatal("expected at least one txn counted after AddLocalTxs")
	}
	t.Logf("CountContent: pending=%d baseFee=%d queued=%d", pending, baseFeeN, queued)

	// PeekBest: onTopOf=0 avoids blocking on lastSeenBlock.
	var out types.TxsRlp
	onTime, err := pool.PeekBest(16, &out, tx, 0, 10_000_000, 10_000_000)
	require.NoError(t, err)
	if !onTime {
		t.Error("expected PeekBest onTime=true for onTopOf=0")
	}

	// YieldBest with an explicit "already yielded" set should return
	// fewer (or equal, if PeekBest returned none) results the second
	// time around for the same hashes.
	var out2 types.TxsRlp
	yielded := mapset.NewThreadUnsafeSet[[32]byte]()
	_, n1, err := pool.YieldBest(16, &out2, tx, 0, 10_000_000, 10_000_000, yielded)
	require.NoError(t, err)
	if n1 == 0 {
		t.Fatal("expected YieldBest to return at least one txn")
	}

	var out3 types.TxsRlp
	_, n2, err := pool.YieldBest(16, &out3, tx, 0, 10_000_000, 10_000_000, yielded)
	require.NoError(t, err)
	if n2 != 0 {
		t.Errorf("expected 0 new txns on second YieldBest with the same yielded set, got %d", n2)
	}
}

// TestSelection_YieldBest_GasLimitExhaustion exercises the early-break
// branch in best() when availableGas is below fixedgas.TxGas.
func TestSelection_YieldBest_GasLimitExhaustion(t *testing.T) {
	pool, db, addr := g49NewFundedPool(t)
	ctx := context.Background()
	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	s := &types.TxSlot{
		Tip:    *uint256.NewInt(300000),
		FeeCap: *uint256.NewInt(300000),
		Gas:    100000,
		Nonce:  0,
	}
	s.IDHash[0] = 0xAA
	s.Rlp = []byte{0xbe, 0xef}
	var txSlots types.TxSlots
	txSlots.Append(s, addr[:], true)
	_, err = pool.AddLocalTxs(ctx, txSlots, tx)
	require.NoError(t, err)

	var out types.TxsRlp
	yielded := mapset.NewThreadUnsafeSet[[32]byte]()
	_, n, err := pool.YieldBest(16, &out, tx, 0, 0 /* no gas available */, 0, yielded)
	require.NoError(t, err)
	if n != 0 {
		t.Errorf("expected 0 txns with availableGas=0, got %d", n)
	}
}
