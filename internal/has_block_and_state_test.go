package internal

import (
	"context"
	"testing"

	lru "github.com/hashicorp/golang-lru/v2"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// mkBlock builds a one-transaction block at the given height.
func mkBlock(t *testing.T, number uint64, root byte) *block.Block {
	t.Helper()
	from := types.HexToAddress("0x100")
	to := types.HexToAddress("0x200")
	txn := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil)
	return block.NewBlock(&block.Header{
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{root},
	}, []*transaction.Transaction{txn}).(*block.Block)
}

// TestHasBlockAndStateMatchesReadBlockPredicate pins the equivalence the cheap
// implementation rests on: HasHeader && rawdb.HasBlock is true exactly when
// rawdb.ReadBlock returns non-nil. If a future storage change breaks that --
// a header written without a body, a body format ReadBlock rejects -- this
// test fails instead of HasBlockAndState silently answering the wrong thing.
func TestHasBlockAndStateMatchesReadBlockPredicate(t *testing.T) {
	db := newRealignTestDB(t)
	ctx := context.Background()

	full := mkBlock(t, 8, 0x08)     // header + body, canonical
	uncanon := mkBlock(t, 9, 0x09)  // header + body, NOT canonical
	hdrOnly := mkBlock(t, 10, 0x0a) // header only
	absent := mkBlock(t, 11, 0x0b)  // nothing stored

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		if err := rawdb.WriteBlock(tx, full); err != nil {
			return err
		}
		if err := rawdb.WriteBlock(tx, uncanon); err != nil {
			return err
		}
		rawdb.WriteHeader(tx, hdrOnly.Header().(*block.Header))
		return rawdb.WriteCanonicalHash(tx, full.Hash(), 8)
	}); err != nil {
		t.Fatal(err)
	}

	cache, _ := lru.New[types.Hash, *block.Block](16)
	bc := &BlockChain{ChainDB: db, ctx: ctx, blockCache: cache}

	cases := []struct {
		name string
		blk  *block.Block
		num  uint64
		want bool
	}{
		{"header+body, canonical", full, 8, true},
		{"header+body, not canonical", uncanon, 9, false},
		{"header only", hdrOnly, 10, false},
		{"absent", absent, 11, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := bc.HasBlockAndState(c.blk.Hash(), c.num)
			if got != c.want {
				t.Fatalf("HasBlockAndState = %v, want %v", got, c.want)
			}

			// The old implementation, computed directly, must agree.
			var legacy bool
			if err := db.View(ctx, func(tx kv.Tx) error {
				if stored := rawdb.ReadBlock(tx, c.blk.Hash(), c.num); stored != nil {
					is, err := rawdb.IsCanonicalHash(tx, stored.Hash())
					if err != nil {
						return err
					}
					legacy = is
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if got != legacy {
				t.Fatalf("HasBlockAndState = %v but GetBlock-based predicate = %v", got, legacy)
			}
		})
	}

	// The point of the change: answering the predicate must not pull block
	// bodies into the cache. ValidateBody calls this twice per imported block,
	// and at bench load that path held 2.07 GB of live heap.
	if n := cache.Len(); n != 0 {
		t.Fatalf("HasBlockAndState populated blockCache with %d entries; it must not decode bodies", n)
	}

	// A cached block still answers without touching the database.
	cache.Add(absent.Hash(), absent)
	if !bc.hasStoredBlock(absent.Hash(), 11) {
		t.Fatal("hasStoredBlock ignored a cached block")
	}

	// A zero hash is never a stored block.
	if bc.hasStoredBlock(types.Hash{}, 0) {
		t.Fatal("hasStoredBlock accepted the zero hash")
	}
}
