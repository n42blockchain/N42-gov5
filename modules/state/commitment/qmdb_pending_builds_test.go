package commitment

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// Two-deep speculation on the proposer's tree: build v, chain v+1 on it
// before v is written, then have the live tree write v (adopt beneath
// v+1) and v+1 (adopt the rest); the tree must equal a fresh load after
// each step. A lost view peels both builds back to the base.
func TestQMDBMinerTreeChainsPendingBuilds(t *testing.T) {
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })

	mkBlock := func(base, n int, nonce uint64) map[types.Address]*account.StateAccount {
		accts := make(map[types.Address]*account.StateAccount, n)
		for i := 0; i < n; i++ {
			accts[rlAddr(base+i)] = qmAcct(nonce, uint64(1000+base+i))
		}
		return accts
	}
	db := memdb.NewTestDB(t)
	live := NewQMDBRootComputer()
	live.EnableUndoRecording()
	rlApplyBlock(t, db, live, mkBlock(0, 1200, 1))
	miner := NewQMDBRootComputer()
	miner.EnableUndoRecording()
	view := func(fn func(tx kv.Tx) error) {
		if err := db.View(t.Context(), fn); err != nil {
			t.Fatal(err)
		}
	}
	view(func(tx kv.Tx) error { miner.SetCold(tx); defer miner.SetCold(nil); return miner.LoadFrom(tx) })
	baseRoot := miner.Root()

	build := func(blk map[types.Address]*account.StateAccount) types.Hash {
		var r types.Hash
		view(func(tx kv.Tx) error {
			miner.SetCold(tx)
			defer miner.SetCold(nil)
			var err error
			r, err = miner.ComputeRoot(blk, nil)
			return err
		})
		return r
	}
	b2, b3 := mkBlock(600, 1200, 2), mkBlock(1200, 1200, 3)
	root2 := build(b2)
	if !miner.ChainPendingBuild() {
		t.Fatal("chain: nothing pending")
	}
	root3 := build(b3)
	if miner.PendingBuilds() != 1 || !miner.HasUnwrittenBuild() {
		t.Fatalf("pending %d", miner.PendingBuilds())
	}
	// The live tree writes v; the miner adopts v beneath v+1 and keeps v+1.
	_, w2 := rlApplyBlock(t, db, live, b2)
	if w2 != root2 {
		t.Fatalf("live root %x != built %x", w2[:8], root2[:8])
	}
	if !miner.AdoptOwnAppends(live.NextSlot(), live.FlushedThrough()) || miner.PendingBuilds() != 0 || miner.LastUndo() == nil {
		t.Fatalf("adopt v: pending %d lastUndo=%v", miner.PendingBuilds(), miner.LastUndo() != nil)
	}
	if miner.Root() != root3 {
		t.Fatalf("after adopting v the tree must still be v+1's state")
	}
	// The live tree writes v+1; the miner adopts the rest and equals a fresh load.
	_, w3 := rlApplyBlock(t, db, live, b3)
	if w3 != root3 {
		t.Fatalf("live root %x != built %x", w3[:8], root3[:8])
	}
	if !miner.AdoptOwnAppends(live.NextSlot(), live.FlushedThrough()) || miner.HasUnwrittenBuild() {
		t.Fatal("adopt v+1")
	}
	fresh := NewQMDBRootComputer()
	view(func(tx kv.Tx) error {
		fresh.SetCold(tx)
		defer fresh.SetCold(nil)
		if err := fresh.LoadFrom(tx); err != nil {
			return err
		}
		miner.SetCold(tx)
		defer miner.SetCold(nil)
		return miner.ReloadForBuild(tx)
	})
	if miner.Root() != fresh.Root() || miner.NextSlot() != fresh.NextSlot() {
		t.Fatalf("adopted tree %x/%d != fresh %x/%d", miner.Root().Bytes()[:8], miner.NextSlot(), fresh.Root().Bytes()[:8], fresh.NextSlot())
	}
	// A lost view: two unwritten builds peel back to the base.
	b4, b5 := mkBlock(1800, 900, 4), mkBlock(2400, 900, 5)
	build(b4)
	miner.ChainPendingBuild()
	build(b5)
	before := fresh.Root()
	view(func(tx kv.Tx) error { miner.SetCold(tx); defer miner.SetCold(nil); return miner.PeelAll() })
	if miner.Root() != before || miner.HasUnwrittenBuild() {
		t.Fatalf("peel-all: root %x want %x (base was %x)", miner.Root().Bytes()[:8], before[:8], baseRoot[:8])
	}
}
