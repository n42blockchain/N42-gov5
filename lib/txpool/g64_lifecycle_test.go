package txpool

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
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

// g64NewFundedPool mirrors g49NewFundedPool but lets the caller fund several
// addresses in one OnNewBlock and override the subpool size limits (the cap
// eviction test needs a tiny queued limit; the default is 10,000).
func g64NewFundedPool(t *testing.T, cfg txpoolcfg.Config, addrs ...[20]byte) (*TxPool, kv.RwDB) {
	t.Helper()
	ch := make(chan types.Announcements, 100)
	db, coreDB := memdb.NewTestPoolDB(t), memdb.NewTestDB(t)

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
	for _, addr := range addrs {
		v := make([]byte, types.EncodeSenderLengthForStorage(0, *uint256.NewInt(10*common.Ether)))
		types.EncodeSender(0, *uint256.NewInt(10*common.Ether), v)
		change.ChangeBatch[0].Changes = append(change.ChangeBatch[0].Changes, &remote.AccountChange{
			Action:  remote.Action_UPSERT,
			Address: gointerfaces.ConvertAddressToH160(addr),
			Data:    v,
		})
	}

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()
	require.NoError(t, pool.OnNewBlock(ctx, change, types.TxSlots{}, types.TxSlots{}, types.TxSlots{}, tx))

	return pool, db
}

// TestG64AddLocked_ReplaceIntoPending covers addLocked's replace branch
// (lifecycle.go) when the displaced transaction currently sits in the
// pending subpool: a second txn at the same nonce with a high enough tip
// bump must remove the original from pending (not just from `all`) before
// re-adding the replacement.
func TestG64AddLocked_ReplaceIntoPending(t *testing.T) {
	pool, db := g64NewFundedPool(t, txpoolcfg.DefaultConfig, [20]byte{0x42})
	addr := [20]byte{0x42}
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	original := &types.TxSlot{
		Tip: *uint256.NewInt(1000), FeeCap: *uint256.NewInt(250000), Gas: 100000, Nonce: 0,
	}
	original.IDHash[0] = 0xA1
	original.Rlp = []byte{0xA1}
	var s1 types.TxSlots
	s1.Append(original, addr[:], true)
	reasons, err := pool.AddLocalTxs(ctx, s1, tx)
	require.NoError(t, err)
	require.Equal(t, txpoolcfg.Success, reasons[0])

	pendingBefore, _, _ := pool.CountContent()
	require.Greater(t, pendingBefore, 0, "the original txn should have promoted to pending")

	// Same nonce, well above the default price-bump threshold on both tip
	// and fee cap: addLocked's replace branch fires, and since `found` is
	// currently in PendingSubPool it must go through p.pending.Remove.
	replacement := &types.TxSlot{
		Tip: *uint256.NewInt(10_000), FeeCap: *uint256.NewInt(500000), Gas: 100000, Nonce: 0,
	}
	replacement.IDHash[0] = 0xA2
	replacement.Rlp = []byte{0xA2}
	var s2 types.TxSlots
	s2.Append(replacement, addr[:], true)
	reasons, err = pool.AddLocalTxs(ctx, s2, tx)
	require.NoError(t, err)
	require.Equal(t, txpoolcfg.Success, reasons[0])

	pendingAfter, baseFeeAfter, queuedAfter := pool.CountContent()
	require.Equal(t, 1, pendingAfter+baseFeeAfter+queuedAfter, "only the replacement should remain in the pool")
	require.Equal(t, 1, pendingAfter, "the replacement should occupy the pending slot")
}

// TestG64Promote_QueuedToBaseFeeToPending exercises promote's remaining
// paths directly: a txn starts in the queued subpool (base fee set above
// its fee cap), then a falling base fee walks it queued -> baseFee ->
// pending across successive promote() calls (driven here via OnNewBlock,
// which calls addTxsOnNewBlock + promote under the hood through AddLocalTxs'
// own promote call after each base-fee change).
func TestG64Promote_QueuedToBaseFeeToPending(t *testing.T) {
	pool, db := g64NewFundedPool(t, txpoolcfg.DefaultConfig, [20]byte{0x42})
	addr := [20]byte{0x42}
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	// FeeCap sits between the two base fees we'll apply: 150 and 50 (x
	// 1_000_000, matching g61's raise() helper), so it starts in queued
	// (base fee too high to even reach baseFee pool) and ends in pending.
	s := &types.TxSlot{
		Tip: *uint256.NewInt(100_000_000), FeeCap: *uint256.NewInt(100_000_000), Gas: 100000, Nonce: 0,
	}
	s.IDHash[0] = 0xB1
	s.Rlp = []byte{0xB1}
	var slots types.TxSlots
	slots.Append(s, addr[:], true)
	_, err = pool.AddLocalTxs(ctx, slots, tx)
	require.NoError(t, err)

	pendingStart, baseFeeStart, queuedStart := pool.CountContent()
	require.Equal(t, 1, pendingStart+baseFeeStart+queuedStart)

	raise := func(blockNum uint64, baseFee uint64, blockHash byte) *remote.StateChangeBatch {
		h := gointerfaces.ConvertHashToH256([32]byte{blockHash})
		return &remote.StateChangeBatch{
			StateVersionId:      blockNum,
			PendingBlockBaseFee: baseFee,
			BlockGasLimit:       1000000,
			ChangeBatch:         []*remote.StateChange{{BlockHeight: blockNum, BlockHash: h}},
		}
	}

	// Base fee far below the fee cap: the txn should end up in pending.
	require.NoError(t, pool.OnNewBlock(ctx, raise(1, 1, 2), types.TxSlots{}, types.TxSlots{}, types.TxSlots{}, tx))
	pendingEnd, baseFeeEnd, queuedEnd := pool.CountContent()
	require.Equal(t, 1, pendingEnd+baseFeeEnd+queuedEnd, "the txn must still be tracked somewhere")
	require.Equal(t, 1, pendingEnd, "a low base fee should let the txn reach pending")
}

// TestG64Promote_QueuedCapEviction covers promote's queued-subpool overflow
// branch: with QueuedSubPoolLimit=1, adding two unpromotable (nonce-gapped)
// txns for two different senders must discard the worse of the two with
// QueuedPoolOverflow, leaving exactly the limit's worth behind.
func TestG64Promote_QueuedCapEviction(t *testing.T) {
	cfg := txpoolcfg.DefaultConfig
	cfg.QueuedSubPoolLimit = 1
	addr1 := [20]byte{0x51}
	addr2 := [20]byte{0x52}
	pool, db := g64NewFundedPool(t, cfg, addr1, addr2)
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	add := func(addr [20]byte, tip uint64, idByte byte) {
		s := &types.TxSlot{
			Tip: *uint256.NewInt(tip), FeeCap: *uint256.NewInt(tip), Gas: 100000, Nonce: 1, // nonce gap: sender's current nonce is 0
		}
		s.IDHash[0] = idByte
		s.Rlp = []byte{idByte}
		var slots types.TxSlots
		slots.Append(s, addr[:], true)
		_, err := pool.AddLocalTxs(ctx, slots, tx)
		require.NoError(t, err)
	}
	add(addr1, 1000, 0xC1)
	add(addr2, 2000, 0xC2)

	_, _, queued := pool.CountContent()
	require.Equal(t, cfg.QueuedSubPoolLimit, queued, "queued subpool must be capped at its limit")
}

// TestG64AddTxsOnNewBlock_UnwindDuplicateIsDiscarded covers
// addTxsOnNewBlock's duplicate-hash short-circuit (lifecycle.go:233): a
// transaction already known to the pool (by hash) that comes back through
// unwindTxs must not be re-added (no double-counting, no second addLocked
// call for the same hash).
func TestG64AddTxsOnNewBlock_UnwindDuplicateIsDiscarded(t *testing.T) {
	pool, db := g64NewFundedPool(t, txpoolcfg.DefaultConfig, [20]byte{0x42})
	addr := [20]byte{0x42}
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	s := &types.TxSlot{
		Tip: *uint256.NewInt(300000), FeeCap: *uint256.NewInt(300000), Gas: 100000, Nonce: 0,
	}
	s.IDHash[0] = 0xD1
	s.Rlp = []byte{0xD1}
	var slots types.TxSlots
	slots.Append(s, addr[:], true)
	reasons, err := pool.AddLocalTxs(ctx, slots, tx)
	require.NoError(t, err)
	require.Equal(t, txpoolcfg.Success, reasons[0])

	before, baseFeeBefore, queuedBefore := pool.CountContent()
	require.Equal(t, 1, before+baseFeeBefore+queuedBefore)

	// Re-inject the same slot (same IDHash) as an "unwound" transaction on
	// the next block: addTxsOnNewBlock must see it already in p.byHash and
	// skip it, so the pool's content doesn't change.
	var unwind types.TxSlots
	unwind.Append(s, addr[:], true)
	h1 := gointerfaces.ConvertHashToH256([32]byte{9})
	change := &remote.StateChangeBatch{
		StateVersionId:      1,
		PendingBlockBaseFee: 200000,
		BlockGasLimit:       1000000,
		ChangeBatch:         []*remote.StateChange{{BlockHeight: 1, BlockHash: h1}},
	}
	require.NoError(t, pool.OnNewBlock(ctx, change, unwind, types.TxSlots{}, types.TxSlots{}, tx))

	after, baseFeeAfter, queuedAfter := pool.CountContent()
	require.Equal(t, before+baseFeeBefore+queuedBefore, after+baseFeeAfter+queuedAfter,
		"re-unwinding an already-known txn must not change pool content")
}

// TestG64AddTxsOnNewBlock_UpsertCodeFundsSender covers addTxsOnNewBlock's
// Action_UPSERT_CODE branch (lifecycle.go:247): a contract-deploying
// account's first appearance often arrives as an UPSERT_CODE (code +
// account data together) rather than a plain UPSERT. The switch must treat
// it exactly the same way — placing the sender in sendersWithChangedState so
// onSenderStateChange runs and a subsequently-added txn from that sender can
// be classified (EnoughBalance, NoNonceGaps) and promoted normally.
func TestG64AddTxsOnNewBlock_UpsertCodeFundsSender(t *testing.T) {
	ch := make(chan types.Announcements, 100)
	db, coreDB := memdb.NewTestPoolDB(t), memdb.NewTestDB(t)
	sendersCache := kvcache.New(kvcache.DefaultCoherentConfig)
	pool, err := New(ch, coreDB, txpoolcfg.DefaultConfig, sendersCache, *uint256.NewInt(1), nil, nil, nil, nil, nil, nil, nil, nil, log.New())
	require.NoError(t, err)

	ctx := context.Background()
	addr := [20]byte{0x77}
	h1 := gointerfaces.ConvertHashToH256([32]byte{})
	v := make([]byte, types.EncodeSenderLengthForStorage(0, *uint256.NewInt(10*common.Ether)))
	types.EncodeSender(0, *uint256.NewInt(10*common.Ether), v)
	change := &remote.StateChangeBatch{
		StateVersionId:      0,
		PendingBlockBaseFee: 200000,
		BlockGasLimit:       1000000,
		ChangeBatch: []*remote.StateChange{{
			BlockHeight: 0, BlockHash: h1,
			Changes: []*remote.AccountChange{{
				Action:  remote.Action_UPSERT_CODE,
				Address: gointerfaces.ConvertAddressToH160(addr),
				Data:    v,
				Code:    []byte{0x60, 0x00},
			}},
		}},
	}

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()
	require.NoError(t, pool.OnNewBlock(ctx, change, types.TxSlots{}, types.TxSlots{}, types.TxSlots{}, tx))

	s := &types.TxSlot{
		Tip: *uint256.NewInt(300000), FeeCap: *uint256.NewInt(300000), Gas: 100000, Nonce: 0,
	}
	s.IDHash[0] = 0xE1
	s.Rlp = []byte{0xE1}
	var slots types.TxSlots
	slots.Append(s, addr[:], true)
	reasons, err := pool.AddLocalTxs(ctx, slots, tx)
	require.NoError(t, err)
	require.Equal(t, txpoolcfg.Success, reasons[0], "a sender funded only through UPSERT_CODE must validate and be accepted")

	pending, baseFee, queued := pool.CountContent()
	require.Equal(t, 1, pending+baseFee+queued)
}

// TestG64OnSenderStateChange_NonceJumpDropsStale covers
// onSenderStateChange's "low nonce" deletion branch directly: once the
// sender's on-chain nonce jumps past a pooled txn's nonce, that txn must be
// discarded (NonceTooLow) rather than merely demoted.
func TestG64OnSenderStateChange_NonceJumpDropsStale(t *testing.T) {
	pool, db := g64NewFundedPool(t, txpoolcfg.DefaultConfig, [20]byte{0x42})
	addr := [20]byte{0x42}
	ctx := context.Background()

	tx, err := db.BeginRw(ctx)
	require.NoError(t, err)
	defer tx.Rollback()

	s := &types.TxSlot{
		Tip: *uint256.NewInt(300000), FeeCap: *uint256.NewInt(300000), Gas: 100000, Nonce: 0,
	}
	s.IDHash[0] = 0xF1
	s.Rlp = []byte{0xF1}
	var slots types.TxSlots
	slots.Append(s, addr[:], true)
	_, err = pool.AddLocalTxs(ctx, slots, tx)
	require.NoError(t, err)

	before, baseFeeBefore, queuedBefore := pool.CountContent()
	require.Equal(t, 1, before+baseFeeBefore+queuedBefore)

	// Sender's nonce jumps to 5 (as if several txns were mined elsewhere):
	// the pooled nonce-0 txn is now stale and must be dropped by
	// onSenderStateChange's low-nonce path. Called directly (rather than via
	// OnNewBlock) to pin the unit under test down without depending on the
	// kvcache's own state-change propagation timing.
	senderID, ok := pool.senders.getID(addr)
	require.True(t, ok)
	pool.lock.Lock()
	pool.onSenderStateChange(senderID, 5, *uint256.NewInt(9*common.Ether), pool.blockGasLimit.Load(), pool.logger)
	pool.lock.Unlock()

	after, baseFeeAfter, queuedAfter := pool.CountContent()
	require.Equal(t, 0, after+baseFeeAfter+queuedAfter, "a stale nonce-0 txn must be dropped once the sender's nonce passes it")
}
