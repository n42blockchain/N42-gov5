package commitment

import (
	"testing"

	"github.com/n42blockchain/N42/lib/qmdb"
)

func qmdbTestKey(b byte) qmdb.Hash {
	var h qmdb.Hash
	h[0] = b
	h[31] = b
	return h
}

// newIndexPair builds a reload tree and a reference tree carrying the same
// keyHash -> slot mapping, which is the state verifyReloadIndex must accept.
func newIndexPair(t *testing.T, n int) (*QMDBRootComputer, *qmdb.Tree, qmdb.Index) {
	t.Helper()
	gotIdx, wantIdx := qmdb.NewMapIndex(), qmdb.NewMapIndex()
	for i := 0; i < n; i++ {
		k := qmdbTestKey(byte(i + 1))
		gotIdx.Put(k, uint64(i*10))
		wantIdx.Put(k, uint64(i*10))
	}
	r := &QMDBRootComputer{t: qmdb.New()}
	r.t.SetIndex(gotIdx)
	ref := qmdb.New()
	ref.SetIndex(wantIdx)
	return r, ref, gotIdx
}

func TestVerifyReloadIndexAcceptsIdenticalMappings(t *testing.T) {
	r, ref, _ := newIndexPair(t, 16)
	if !r.verifyReloadIndex(ref, "test") {
		t.Fatal("identical index mappings were reported as divergent")
	}
}

// TestVerifyReloadIndexCatchesStaleSlot is the P1 regression: a key mapped to the
// WRONG slot keeps the live-key count identical and can leave the world root
// byte-identical, so cardinality and root checks both pass. Only the mapping
// comparison sees it — and it has to, because Tree.Get resolves through the
// index without consulting the live bit.
func TestVerifyReloadIndexCatchesStaleSlot(t *testing.T) {
	r, ref, gotIdx := newIndexPair(t, 16)

	stale := qmdbTestKey(5)
	gotIdx.Put(stale, 99999) // points at a slot the rebuild says is not current

	if r.t.LiveCount() != ref.LiveCount() {
		t.Fatalf("precondition: cardinality must still match (%d vs %d)", r.t.LiveCount(), ref.LiveCount())
	}
	if r.verifyReloadIndex(ref, "test") {
		t.Fatal("a stale slot mapping passed verification; the check is still cardinality-only")
	}
}

// TestVerifyReloadIndexCatchesSubstitutedKey covers the equal-cardinality case
// where the reload dropped one key and grew another: the set argument behind the
// one-directional walk (equal counts + reference subset => equal sets) is what
// makes a single direction sufficient, so a substitution must be caught.
func TestVerifyReloadIndexCatchesSubstitutedKey(t *testing.T) {
	r, ref, gotIdx := newIndexPair(t, 16)

	gotIdx.Delete(qmdbTestKey(7))
	gotIdx.Put(qmdbTestKey(200), 70)

	if r.t.LiveCount() != ref.LiveCount() {
		t.Fatalf("precondition: cardinality must still match (%d vs %d)", r.t.LiveCount(), ref.LiveCount())
	}
	if r.verifyReloadIndex(ref, "test") {
		t.Fatal("a substituted key passed verification")
	}
}

// TestVerifyReloadIndexSkipsNonIterableIndex documents the MDBX-backed case: the
// comparison cannot run, and the function says so rather than claiming a pass it
// did not earn.
func TestVerifyReloadIndexSkipsNonIterableIndex(t *testing.T) {
	r, _, _ := newIndexPair(t, 4)
	ref := qmdb.New()
	ref.SetIndex(opaqueIndex{})
	if !r.verifyReloadIndex(ref, "test") {
		t.Fatal("non-iterable reference index should be skipped, not failed")
	}
}

// opaqueIndex implements qmdb.Index without IterableIndex, standing in for the
// MDBX-backed index.
type opaqueIndex struct{}

func (opaqueIndex) Get(qmdb.Hash) (uint64, bool) { return 0, false }
func (opaqueIndex) Put(qmdb.Hash, uint64)        {}
func (opaqueIndex) Delete(qmdb.Hash)             {}
func (opaqueIndex) Len() int                     { return 0 }
