package commitment

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

// The proposer's isolated tree builds a block; the live tree replays the same
// dirty set and flushes it. AdoptOwnAppends must leave the isolated tree
// equal to a fresh load of the store, block after block, with no peel and no
// reload in between -- and must refuse when the two trees disagree.
func TestQMDBMinerTreeAdoptsOwnAppends(t *testing.T) {
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

	// The isolated tree starts as a full load of the store at block 1.
	miner := NewQMDBRootComputer()
	miner.EnableUndoRecording()
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		miner.SetCold(tx)
		defer miner.SetCold(nil)
		return miner.LoadFrom(tx)
	}); err != nil {
		t.Fatal(err)
	}
	if miner.Root() != live.Root() {
		t.Fatalf("isolated tree root %x != live %x after load", miner.Root().Bytes()[:8], live.Root().Bytes()[:8])
	}

	// Blocks 2..4: the isolated tree builds, the live tree replays and
	// flushes, the isolated tree adopts. Half of each block overwrites the
	// previous one so flushed slots die (deadFlushed bookkeeping in play).
	for i, blk := range []map[types.Address]*account.StateAccount{
		mkBlock(600, 1200, 2), mkBlock(1200, 1200, 3), mkBlock(1800, 1200, 4),
	} {
		var built types.Hash
		if err := db.View(t.Context(), func(tx kv.Tx) error {
			miner.SetCold(tx)
			defer miner.SetCold(nil)
			var err error
			built, err = miner.ComputeRoot(blk, nil)
			return err
		}); err != nil {
			t.Fatalf("block %d build: %v", i+2, err)
		}
		if miner.LastUndo() == nil {
			t.Fatalf("block %d: the build must leave an undo record", i+2)
		}
		_, written := rlApplyBlock(t, db, live, blk)
		if written != built {
			t.Fatalf("block %d: live root %x != built %x", i+2, written[:8], built[:8])
		}
		if !miner.AdoptOwnAppends(live.NextSlot(), live.FlushedThrough()) {
			t.Fatalf("block %d: adoption refused (miner cursor %d, live %d)", i+2, miner.NextSlot(), live.NextSlot())
		}
		if miner.LastUndo() != nil {
			t.Fatalf("block %d: adoption must drop the undo record", i+2)
		}
		// The adopted tree must be what ReloadForBuild would have produced,
		// and a reload on top of it must be a no-op that agrees with a fresh
		// load of the store.
		fresh := NewQMDBRootComputer()
		if err := db.View(t.Context(), func(tx kv.Tx) error {
			fresh.SetCold(tx)
			defer fresh.SetCold(nil)
			if err := fresh.LoadFrom(tx); err != nil {
				return err
			}
			miner.SetCold(tx)
			defer miner.SetCold(nil)
			return miner.ReloadForBuild(tx)
		}); err != nil {
			t.Fatalf("block %d: reload after adoption: %v", i+2, err)
		}
		if miner.Root() != fresh.Root() || miner.NextSlot() != fresh.NextSlot() || miner.Tree().LiveCount() != fresh.Tree().LiveCount() {
			t.Fatalf("block %d: adopted tree (root %x, slot %d, live %d) != fresh load (root %x, slot %d, live %d)",
				i+2, miner.Root().Bytes()[:8], miner.NextSlot(), miner.Tree().LiveCount(),
				fresh.Root().Bytes()[:8], fresh.NextSlot(), fresh.Tree().LiveCount())
		}
		for j := 0; j < 1200; j += 97 {
			kh := qmdb.Hash(AccountKeyHash(rlAddr(base(i) + j)))
			var vm, vf []byte
			var fm, ff bool
			_ = db.View(t.Context(), func(tx kv.Tx) error {
				vm, fm, _ = miner.Lookup(kh, tx)
				vf, ff, _ = fresh.Lookup(kh, tx)
				return nil
			})
			if fm != ff || string(vm) != string(vf) {
				t.Fatalf("block %d: lookup %d differs after adoption (found %v/%v)", i+2, j, fm, ff)
			}
		}
	}

	// Disagreement: the isolated tree built a block the live tree did not
	// write. Adoption must refuse and leave the undo for the peel.
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		miner.SetCold(tx)
		defer miner.SetCold(nil)
		_, err := miner.ComputeRoot(mkBlock(2400, 300, 5), nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if miner.AdoptOwnAppends(live.NextSlot(), live.FlushedThrough()) {
		t.Fatal("adoption must refuse when the live tree's cursor is behind the build")
	}
	if miner.LastUndo() == nil {
		t.Fatal("a refused adoption must keep the undo record")
	}
}

func base(i int) int { return 600 * (i + 1) }
