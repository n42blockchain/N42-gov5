package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/internal/synclegacy"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type conflictFixture struct {
	bc              *BlockChain
	p, a, b, a1, b1 *block.Block
}

func conflictBlock(number uint64, parent types.Hash, root byte) *block.Block {
	return block.NewBlock(&block.Header{
		Number:     uint256.NewInt(number),
		ParentHash: parent,
		Difficulty: uint256.NewInt(1),
		Root:       types.Hash{root},
	}, nil).(*block.Block)
}

// P(8) canonical; A(9) and B(9) siblings; A1(10) on A, B1(10) on B.
func newConflictFixture(t *testing.T) *conflictFixture {
	t.Helper()
	db := newRealignTestDB(t)
	f := &conflictFixture{}
	f.p = conflictBlock(8, types.Hash{}, 0x08)
	f.a = conflictBlock(9, f.p.Hash(), 0xa9)
	f.b = conflictBlock(9, f.p.Hash(), 0xb9)
	f.a1 = conflictBlock(10, f.a.Hash(), 0xaa)
	f.b1 = conflictBlock(10, f.b.Hash(), 0xba)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for _, b := range []*block.Block{f.p, f.a, f.b, f.a1, f.b1} {
			if err := rawdb.WriteBlock(tx, b); err != nil {
				return err
			}
		}
		if err := rawdb.WriteCanonicalHash(tx, f.p.Hash(), 8); err != nil {
			return err
		}
		rawdb.WriteHeadBlockHash(tx, f.p.Hash())
		return rawdb.WriteHeadHeaderHash(tx, f.p.Hash())
	}); err != nil {
		t.Fatal(err)
	}
	f.bc = &BlockChain{ChainDB: db, ctx: context.Background()}
	f.bc.currentBlock.Store(f.p)
	return f
}

type canonView struct {
	rows   map[uint64]types.Hash
	marker types.Hash
	head   types.Hash
}

func (f *conflictFixture) view(t *testing.T) canonView {
	t.Helper()
	v := canonView{rows: map[uint64]types.Hash{}}
	if err := f.bc.ChainDB.View(context.Background(), func(tx kv.Tx) error {
		for n := uint64(8); n <= 11; n++ {
			h, err := rawdb.ReadCanonicalHash(tx, n)
			if err != nil {
				return err
			}
			v.rows[n] = h
		}
		v.marker = rawdb.ReadHotStuffCommittedHead(tx)
		v.head = rawdb.ReadHeadBlockHash(tx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return v
}

func requireSameCanon(t *testing.T, before, after canonView) {
	t.Helper()
	if before.marker != after.marker || before.head != after.head {
		t.Fatalf("committed marker/head moved: %x/%x -> %x/%x", before.marker, before.head, after.marker, after.head)
	}
	for n, h := range before.rows {
		if after.rows[n] != h {
			t.Fatalf("canonical row %d rewritten: %x -> %x", n, h, after.rows[n])
		}
	}
}

// Two verified commits at one height: the second is refused with the
// conflict evidence, and no canonical row, head or committed marker moves.
func TestConflictingCommitFailsClosed(t *testing.T) {
	f := newConflictFixture(t)
	if err := f.bc.CommitToCanonical(f.a.Hash()); err != nil {
		t.Fatal(err)
	}
	before := f.view(t)
	if before.marker != f.a.Hash() {
		t.Fatalf("marker = %x, want A", before.marker)
	}
	metricBefore := metricCommitConflict.Get()

	err := f.bc.CommitToCanonical(f.b.Hash())
	var ce *consensus.ConflictingCommitError
	if !errors.As(err, &ce) || !errors.Is(err, consensus.ErrConflictingCommit) {
		t.Fatalf("sibling commit err = %v, want a ConflictingCommitError", err)
	}
	if ce.Height != 9 || ce.Committed != f.a.Hash() || ce.Incoming != f.b.Hash() {
		t.Fatalf("evidence = %+v", ce)
	}
	requireSameCanon(t, before, f.view(t))
	if f.bc.CurrentBlock().Hash() != f.a.Hash() {
		t.Fatal("current block moved")
	}

	// A descendant of the losing sibling conflicts at the committed height.
	err = f.bc.CommitToCanonical(f.b1.Hash())
	if !errors.As(err, &ce) || ce.Height != 9 || ce.Incoming != f.b.Hash() || ce.TargetNumber != 10 {
		t.Fatalf("descendant commit err = %v, want a conflict at height 9", err)
	}
	requireSameCanon(t, before, f.view(t))
	if got := metricCommitConflict.Get() - metricBefore; got != 2 {
		t.Fatalf("conflict metric rose by %d, want 2", got)
	}

	// Late duplicates and ancestors on the committed chain stay harmless.
	if err := f.bc.CommitToCanonical(f.a.Hash()); err != nil {
		t.Fatalf("duplicate commit: %v", err)
	}
	if err := f.bc.CommitToCanonical(f.p.Hash()); err != nil {
		t.Fatalf("ancestor commit: %v", err)
	}
	requireSameCanon(t, before, f.view(t))

	// Extending the committed chain still works.
	if err := f.bc.CommitToCanonical(f.a1.Hash()); err != nil {
		t.Fatalf("extending commit: %v", err)
	}
	if v := f.view(t); v.marker != f.a1.Hash() || v.rows[10] != f.a1.Hash() || v.rows[9] != f.a.Hash() {
		t.Fatalf("extending commit did not advance: %+v", v)
	}
}

// The escape hatch restores the old rewrite (and its truncation).
func TestConflictingCommitLegacyRewrites(t *testing.T) {
	defer synclegacy.CommitRewrite.SetForTest(true)()
	f := newConflictFixture(t)
	if err := f.bc.CommitToCanonical(f.a1.Hash()); err != nil {
		t.Fatal(err)
	}
	if err := f.bc.CommitToCanonical(f.b.Hash()); err != nil {
		t.Fatalf("legacy sibling commit: %v", err)
	}
	v := f.view(t)
	if v.marker != f.b.Hash() || v.rows[9] != f.b.Hash() || v.rows[10] != (types.Hash{}) {
		t.Fatalf("legacy path did not rewrite and truncate: %+v", v)
	}
}
