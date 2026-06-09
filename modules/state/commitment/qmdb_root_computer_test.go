package commitment

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/state"
)

// Compile-time: QMDBRootComputer must satisfy the RootComputer interface so
// replay-v2 can drive it like JMT/BMT/MPT.
var _ state.RootComputer = (*QMDBRootComputer)(nil)

func qmAddr(b byte) types.Address {
	var a types.Address
	a[19] = b
	return a
}
func qmAcct(nonce, bal uint64) *account.StateAccount {
	a := &account.StateAccount{Initialised: true, Nonce: nonce}
	a.Balance.SetUint64(bal)
	return a
}

// block is one block's dirty set.
type block struct {
	accts map[types.Address]*account.StateAccount
	stor  map[types.Address]map[types.Hash]*uint256.Int
}

func runBlocks(blocks []block) []types.Hash {
	rc := NewQMDBRootComputer()
	roots := make([]types.Hash, len(blocks))
	for i, b := range blocks {
		r, err := rc.ComputeRoot(b.accts, b.stor)
		if err != nil {
			panic(err)
		}
		roots[i] = r
	}
	return roots
}

// TestQMDBComputerDeterministic: the same block sequence yields the same per-block
// roots every run (cross-node agreement comes from identical replay).
func TestQMDBComputerDeterministic(t *testing.T) {
	slot := func(b byte) types.Hash { var h types.Hash; h[31] = b; return h }
	blocks := []block{
		{accts: map[types.Address]*account.StateAccount{qmAddr(1): qmAcct(1, 100), qmAddr(2): qmAcct(1, 200)}},
		{
			accts: map[types.Address]*account.StateAccount{qmAddr(1): qmAcct(2, 150)}, // update
			stor:  map[types.Address]map[types.Hash]*uint256.Int{qmAddr(1): {slot(1): uint256.NewInt(0xaa)}},
		},
		{accts: map[types.Address]*account.StateAccount{qmAddr(2): nil}}, // delete acct 2
		{stor: map[types.Address]map[types.Hash]*uint256.Int{qmAddr(1): {slot(1): uint256.NewInt(0)}}}, // delete slot
	}
	r1 := runBlocks(blocks)
	r2 := runBlocks(blocks)
	for i := range r1 {
		if r1[i] != r2[i] {
			t.Fatalf("block %d root non-deterministic:\n  %x\n  %x", i, r1[i], r2[i])
		}
	}
	// roots must actually change across blocks (state is moving)
	if r1[0] == r1[1] || r1[1] == r1[2] {
		t.Fatal("expected roots to change as state changes")
	}
}

// TestQMDBComputerProvesLiveState: after a sequence, every live account/storage
// key has a verifiable membership proof against the final root.
func TestQMDBComputerProvesLiveState(t *testing.T) {
	rc := NewQMDBRootComputer()
	accts := map[types.Address]*account.StateAccount{}
	for i := byte(1); i <= 40; i++ {
		accts[qmAddr(i)] = qmAcct(uint64(i), uint64(i)*1000)
	}
	root, err := rc.ComputeRoot(accts, nil)
	if err != nil {
		t.Fatal(err)
	}
	tree := rc.Tree()
	for i := byte(1); i <= 40; i++ {
		kh := qmdb.Hash(AccountKeyHash(qmAddr(i)))
		p, ok := tree.GetProof(kh)
		if !ok {
			t.Fatalf("acct %d: no proof", i)
		}
		if !qmdb.VerifyProof(qmdb.Hash(root), p) {
			t.Fatalf("acct %d: proof did not verify against root", i)
		}
	}
}
