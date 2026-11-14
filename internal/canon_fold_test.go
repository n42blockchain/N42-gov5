// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// canonFoldFixture is a stored chain g, A1..A5 with a sibling branch
// S2..S5 off A1, every block carrying one transaction (lookup rows).
type canonFoldFixture struct {
	g, a [6]*block.Block // a[0] = genesis
	s    [6]*block.Block // s[2..5]
}

func newCanonFoldFixture() *canonFoldFixture {
	to := types.HexToAddress("0x200")
	f := &canonFoldFixture{}
	mk := func(n uint64, parent types.Hash, root byte) *block.Block {
		txn := transaction.NewTransaction(n, types.HexToAddress("0x100"), &to, uint256.NewInt(uint64(root)), 21000, uint256.NewInt(1), nil)
		return block.NewBlock(&block.Header{
			Number:     uint256.NewInt(n),
			ParentHash: parent,
			Difficulty: uint256.NewInt(1),
			Root:       types.Hash{root},
		}, []*transaction.Transaction{txn}).(*block.Block)
	}
	f.a[0] = mk(0, types.Hash{}, 0xf0)
	for n := uint64(1); n <= 5; n++ {
		f.a[n] = mk(n, f.a[n-1].Hash(), byte(n))
	}
	f.s[1] = f.a[1]
	for n := uint64(2); n <= 5; n++ {
		f.s[n] = mk(n, f.s[n-1].Hash(), byte(0x50+n))
	}
	return f
}

// seed stores every block, canonical rows for canon (by height), the head
// at the last of them and the applied marker at applied.
func (f *canonFoldFixture) seed(t *testing.T, db kv.RwDB, canon []*block.Block, applied *block.Block, marked *block.Block) {
	t.Helper()
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		for n := 0; n <= 5; n++ {
			if err := rawdb.WriteBlock(tx, f.a[n]); err != nil {
				return err
			}
			if n >= 2 {
				if err := rawdb.WriteBlock(tx, f.s[n]); err != nil {
					return err
				}
			}
		}
		for _, b := range canon {
			if err := rawdb.WriteCanonicalHash(tx, b.Hash(), b.Number64().Uint64()); err != nil {
				return err
			}
		}
		head := canon[len(canon)-1]
		rawdb.WriteHeadBlockHash(tx, head.Hash())
		if err := rawdb.WriteHeadHeaderHash(tx, head.Hash()); err != nil {
			return err
		}
		if marked != nil {
			if err := rawdb.WriteOwnUnverifiedMark(tx, marked.Hash(), marked.Number64().Uint64()); err != nil {
				return err
			}
		}
		return rawdb.WriteQMDBApplied(tx, applied.Number64().Uint64(), applied.Hash())
	}))
}

// TestCanonFoldApplyMatchesCommit: a canonical commit folded into a write
// transaction (canonFoldApply) leaves every table exactly as
// CommitToCanonicalWith's own transaction does -- a plain advance, a rewrite
// of a sibling branch with the dead rows above it truncated, a duplicate,
// a commit ahead of the applied head and one off the applied lineage (both
// refused, nothing written), an own-unverified mark -- with and without the
// tail indexer.
func TestCanonFoldApplyMatchesCommit(t *testing.T) {
	f := newCanonFoldFixture()
	type step struct {
		commit  *block.Block
		refused bool
	}
	cases := []struct {
		name    string
		canon   []*block.Block
		applied *block.Block
		marked  *block.Block
		steps   []step
	}{
		{"advance", []*block.Block{f.a[0], f.a[1]}, f.a[4], f.a[3],
			[]step{{f.a[3], false}, {f.a[4], false}, {f.a[2], false}, {f.a[5], true}, {f.s[3], true}, {f.a[4], false}}},
		{"rewrite", []*block.Block{f.a[0], f.a[1], f.s[2], f.s[3], f.s[4], f.s[5]}, f.a[4], nil,
			[]step{{f.a[4], false}, {f.a[3], false}, {f.s[5], true}}},
		{"rewriteBelow", []*block.Block{f.a[0], f.a[1], f.s[2], f.s[3]}, f.a[3], nil,
			[]step{{f.a[2], false}, {f.a[3], false}}},
	}
	for _, c := range cases {
		for _, indexer := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/indexer=%v", c.name, indexer), func(t *testing.T) {
				defer SetCanonFoldForTest(false)()
				ownDB, foldDB := newRealignTestDB(t), newRealignTestDB(t)
				f.seed(t, ownDB, c.canon, c.applied, c.marked)
				f.seed(t, foldDB, c.canon, c.applied, c.marked)
				own := &BlockChain{ChainDB: ownDB, ctx: context.Background(), qmdbEnabled: true}
				fold := &BlockChain{ChainDB: foldDB, ctx: context.Background(), qmdbEnabled: true}
				if indexer {
					own.SetTxIndexer(&testTxIndexer{})
					fold.SetTxIndexer(&testTxIndexer{})
				}
				own.currentBlock.Store(c.canon[len(c.canon)-1])
				job := &lagWriteJob{hash: c.applied.Hash(), number: c.applied.Number64().Uint64(), blk: c.applied}
				for i, s := range c.steps {
					hook := func(i int) func(kv.RwTx) error {
						return func(tx kv.RwTx) error {
							return tx.Put(modules.HotStuffState, []byte("hook"), []byte{byte(i)})
						}
					}
					err := own.CommitToCanonicalWith(s.commit.Hash(), hook(i))
					r := &canonFoldReq{hash: s.commit.Hash(), inTx: hook(i)}
					require.NoError(t, foldDB.Update(context.Background(), func(tx kv.RwTx) error {
						return fold.canonFoldApply(tx, r, job)
					}))
					if s.refused {
						require.Error(t, err, "step %d: own transaction", i)
						require.Error(t, r.err, "step %d: fold", i)
						require.False(t, r.applied)
					} else {
						require.NoError(t, err, "step %d: own transaction", i)
						require.NoError(t, r.err, "step %d: fold", i)
					}
					requireSameTables(t, dumpTables(t, ownDB), dumpTables(t, foldDB), "after step "+itoa(i))
				}
			})
		}
	}
}

// canonFoldChainRun imports blks through the ordered writer while a
// consensus-like loop commits each block's parent right after the block is
// queued (often before the parent is executed), with a state hook, and
// retries a deferred commit once its block is written, persisting the state
// on its own when the hook did not commit -- what the HotStuff service does.
// A stored sibling of block 3 is committed too (refused in every mode). It
// returns the final tables.
func canonFoldChainRun(t *testing.T, keys []*ecdsa.PrivateKey, blks []*block.Block, sib *block.Block) map[string]string {
	t.Helper()
	lc := newLagChainEngine(t, keys, importBenchEngine{})
	lagWriteBeforeCommitForTest = func(uint64) error {
		time.Sleep(15 * time.Millisecond)
		return nil
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()
	pending := map[types.Hash]uint64{}
	var hsMu sync.Mutex
	commit := func(b *block.Block, key string) {
		ran := false
		err := lc.bc.CommitToCanonicalWith(b.Hash(), func(tx kv.RwTx) error {
			hsMu.Lock()
			ran = true
			hsMu.Unlock()
			return tx.Put(modules.HotStuffState, []byte(key), []byte{1})
		})
		hsMu.Lock()
		committed := ran && err == nil
		hsMu.Unlock()
		if !committed {
			require.NoError(t, lc.db.Update(context.Background(), func(tx kv.RwTx) error {
				return tx.Put(modules.HotStuffState, []byte(key), []byte{1})
			}))
		}
		if err != nil {
			pending[b.Hash()] = b.Number64().Uint64()
		}
	}
	retry := func(upTo uint64) {
		for h, n := range pending {
			if n > upTo {
				continue
			}
			if err := lc.bc.CommitToCanonical(h); err == nil {
				delete(pending, h)
			}
		}
	}
	for i, b := range blks {
		lc.bc.RememberAvailable(b)
		lc.bc.EnqueueLagImport(b)
		if i == 3 && sib != nil {
			require.NoError(t, lc.db.Update(context.Background(), func(tx kv.RwTx) error {
				return rawdb.WriteBlock(tx, sib)
			}))
			commit(sib, "hs-sib")
		}
		if i > 0 {
			commit(blks[i-1], fmt.Sprintf("hs-%03d", i-1))
		}
		if i >= 2 {
			waitLagWritten(t, lc.bc, uint64(i-1))
			retry(uint64(i - 1))
		}
	}
	waitLagWritten(t, lc.bc, uint64(len(blks)))
	commit(blks[len(blks)-1], fmt.Sprintf("hs-%03d", len(blks)-1))
	lc.bc.lock.Lock()
	require.NoError(t, lc.bc.joinLagWrite())
	lc.bc.lock.Unlock()
	if sib != nil {
		// Off the applied lineage: refused in every mode, never retried.
		delete(pending, sib.Hash())
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(pending) > 0 && time.Now().Before(deadline) {
		retry(uint64(len(blks)))
		time.Sleep(time.Millisecond)
	}
	require.Empty(t, pending, "commits never completed")
	require.Eventually(t, func() bool {
		head := lc.bc.CurrentBlock()
		return head != nil && head.Hash() == blks[len(blks)-1].Hash()
	}, 5*time.Second, time.Millisecond, "published head")
	if canonFoldOn {
		cf := &lc.bc.canonFold
		require.NotZero(t, cf.foldedWaited.Load()+cf.foldedDeferred.Load(), "nothing folded")
	}
	return dumpTables(t, lc.db)
}

// TestCanonFoldChainSameTables: the same chain and the same commit
// schedule, with N42_CANON_FOLD off and on, end with the same rows in every
// table; with it on, commits were folded into the ordered writes (both
// those waiting on a write in flight and those refused as not applied yet).
func TestCanonFoldChainSameTables(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	keys := frametest.Keys(t, 3, 73)
	const n = 10
	restore := SetCanonFoldForTest(false)
	ref := newLagChainEngine(t, keys, importBenchEngine{})
	blks, _ := preparedWriteBlocks(t, ref, n)
	sib := ref.build(blks[1].Header().(*block.Header), 0x5b, ref.transfer(0, 99, ref.addrs[1], 3))
	restore()

	for _, early := range []bool{false, true} {
		t.Run(fmt.Sprintf("txnEarly=%v", early), func(t *testing.T) {
			defer SetLagWriteTxnEarlyForTest(early)()
			want := func() map[string]string {
				defer SetCanonFoldForTest(false)()
				return canonFoldChainRun(t, keys, blks, sib)
			}()
			defer SetCanonFoldForTest(true)()
			got := canonFoldChainRun(t, keys, blks, sib)
			requireSameTables(t, want, got, "canon fold")
		})
	}
}

// TestCanonFoldCounts: with the switch on, the commit schedule folds
// commits into the ordered writes and refuses not-yet-applied ones without
// the writer.
func TestCanonFoldCounts(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	defer SetCanonFoldForTest(true)()
	keys := frametest.Keys(t, 3, 74)
	lc := newLagChainEngine(t, keys, importBenchEngine{})
	blks, _ := func() ([]*block.Block, []map[string]string) {
		defer SetCanonFoldForTest(false)()
		ref := newLagChainEngine(t, keys, importBenchEngine{})
		return preparedWriteBlocks(t, ref, 6)
	}()
	release := make(chan struct{})
	reached := make(chan uint64, 16)
	lagWriteBeforeCommitForTest = func(n uint64) error {
		reached <- n
		<-release
		return nil
	}
	defer func() { lagWriteBeforeCommitForTest = nil }()

	// Block 1's write is held in flight: a commit of block 1 now is handed
	// to it and returns once it is durable; a commit of block 2 (not
	// executed) is refused without the writer and remembered.
	lc.bc.RememberAvailable(blks[0])
	lc.bc.EnqueueLagImport(blks[0])
	require.Equal(t, uint64(1), <-reached)
	var hookRan bool
	errc := make(chan error, 1)
	go func() {
		errc <- lc.bc.CommitToCanonicalWith(blks[0].Hash(), func(tx kv.RwTx) error {
			hookRan = true
			return tx.Put(modules.HotStuffState, []byte("hs"), []byte{1})
		})
	}()
	require.Eventually(t, func() bool {
		lc.bc.canonFold.mu.Lock()
		defer lc.bc.canonFold.mu.Unlock()
		return len(lc.bc.canonFold.waiting) == 1
	}, 5*time.Second, time.Millisecond)
	lc.bc.RememberAvailable(blks[1])
	lc.bc.EnqueueLagImport(blks[1])
	err2 := lc.bc.CommitToCanonicalWith(blks[1].Hash(), func(kv.RwTx) error { return nil })
	require.Error(t, err2, "block 2 is not executed: refused")
	require.Equal(t, uint64(1), lc.bc.canonFold.refusedNoWriter.Load())
	close(release)
	require.NoError(t, <-errc)
	require.True(t, hookRan)
	require.Equal(t, blks[0].Hash(), lc.bc.CurrentBlock().Hash(), "head published after the folded write")
	waitLagWritten(t, lc.bc, 2)
	require.Eventually(t, func() bool { return lc.bc.canonFold.foldedDeferred.Load() == 1 }, 5*time.Second, time.Millisecond)
	require.Equal(t, uint64(1), lc.bc.canonFold.foldedWaited.Load())
	require.Eventually(t, func() bool { return lc.bc.CurrentBlock().Hash() == blks[1].Hash() }, 5*time.Second, time.Millisecond)
	// The import notification's retry finds block 2 canonical without the
	// writer (no lookup rows left to write: no tail indexer here, so it
	// takes its own transaction, as before).
	require.NoError(t, lc.bc.CommitToCanonical(blks[1].Hash()))
	require.NoError(t, lc.db.View(context.Background(), func(tx kv.Tx) error {
		h, err := rawdb.ReadCanonicalHash(tx, 2)
		require.NoError(t, err)
		require.Equal(t, blks[1].Hash(), h)
		require.Equal(t, blks[1].Hash(), rawdb.ReadHeadBlockHash(tx))
		require.Equal(t, blks[1].Hash(), rawdb.ReadHotStuffCommittedHead(tx))
		v, err := tx.GetOne(modules.HotStuffState, []byte("hs"))
		require.NoError(t, err)
		require.Equal(t, []byte{1}, v)
		return nil
	}))
}

// TestCanonFoldCrashBeforeCommit: block 4's write has folded the canonical
// commit of block 4 (remembered as not applied when it arrived) when the
// node "crashes" before the transaction commits. The store has neither
// block 4 nor its canonical row, the committed head is block 3; after the
// restart block 4 imports again and its commit makes it canonical.
func TestCanonFoldCrashBeforeCommit(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	defer SetCanonFoldForTest(true)()
	keys := frametest.Keys(t, 3, 75)
	blks, _ := func() ([]*block.Block, []map[string]string) {
		defer SetCanonFoldForTest(false)()
		ref := newLagChainEngine(t, keys, importBenchEngine{})
		return preparedWriteBlocks(t, ref, 5)
	}()
	lc := newLagChainEngine(t, keys, importBenchEngine{})
	const lost = 4
	errCrash := errors.New("crash after the fold")
	reached := make(chan int, 1)
	release := make(chan struct{})
	var once sync.Once
	canonFoldAfterDrainForTest = func(n uint64, folded int) error {
		if n != lost {
			return nil
		}
		once.Do(func() {
			reached <- folded
			<-release
		})
		return errCrash // every later attempt "crashes" too
	}
	defer func() { canonFoldAfterDrainForTest = nil }()
	for i := 0; i < lost-1; i++ {
		lc.bc.RememberAvailable(blks[i])
		lc.bc.EnqueueLagImport(blks[i])
		waitLagWritten(t, lc.bc, uint64(i+1))
		require.NoError(t, lc.bc.CommitToCanonicalWith(blks[i].Hash(), func(kv.RwTx) error { return nil }))
	}
	// Block 4 is queued and its commit arrives at once: refused as not
	// executed (remembered) or handed to its write in flight. Either way
	// the write folds it.
	lc.bc.RememberAvailable(blks[lost-1])
	lc.bc.EnqueueLagImport(blks[lost-1])
	errc := make(chan error, 1)
	go func() {
		errc <- lc.bc.CommitToCanonicalWith(blks[lost-1].Hash(), func(kv.RwTx) error { return nil })
	}()
	var folded int
	select {
	case folded = <-reached:
	case <-time.After(30 * time.Second):
		t.Fatal("block 4's write never reached its commit")
	}
	require.Equal(t, 1, folded, "block 4's commit folded into its write")
	job := lc.bc.lagWritePending(blks[lost-1].Hash())
	require.NotNil(t, job)
	close(release)
	<-job.done
	require.Error(t, <-errc, "a commit of a block whose write crashed")
	require.NoError(t, lc.bc.Close())

	re := reopenLagChain(t, lc)
	require.NoError(t, lc.db.View(context.Background(), func(tx kv.Tx) error {
		require.Nil(t, rawdb.ReadHeader(tx, blks[lost-1].Hash(), lost), "block 4 is stored")
		h, err := rawdb.ReadCanonicalHash(tx, lost)
		require.NoError(t, err)
		require.Equal(t, types.Hash{}, h, "block 4's canonical row survived the crash")
		require.Equal(t, blks[lost-2].Hash(), rawdb.ReadHotStuffCommittedHead(tx), "committed head")
		require.Equal(t, blks[lost-2].Hash(), rawdb.ReadHeadBlockHash(tx), "head")
		return nil
	}))
	canonFoldAfterDrainForTest = nil
	re.bc.RememberAvailable(blks[lost-1])
	re.bc.EnqueueLagImport(blks[lost-1])
	waitLagWritten(t, re.bc, lost)
	require.NoError(t, re.bc.CommitToCanonicalWith(blks[lost-1].Hash(), func(kv.RwTx) error { return nil }))
	require.NoError(t, re.db.View(context.Background(), func(tx kv.Tx) error {
		h, err := rawdb.ReadCanonicalHash(tx, lost)
		require.NoError(t, err)
		require.Equal(t, blks[lost-1].Hash(), h)
		return nil
	}))
}
