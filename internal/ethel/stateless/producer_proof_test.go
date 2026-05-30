package stateless

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// TestProducerProofFromForwardComputation proves Phase A's core claim: the MPT
// stateless multiproof can be captured as a BYPRODUCT of the existing per-block
// forward changeset root computation (TrieRootComputer incremental +
// EnableProofCapture) — no separate extraction, no change to witness gen — and
// the captured proof is consistent with the computed root. Consistency is shown
// end to end: the captured (post-state) multiproof plus the changeset's OLD
// values recomputes the pre-state root through the P8 consumer.
func TestProducerProofFromForwardComputation(t *testing.T) {
	// S0 = block N-1 state.
	accts := map[types.Address]*account.StateAccount{}
	for i := 1; i <= 30; i++ {
		a := &account.StateAccount{}
		a.Reset()
		a.Nonce = uint64(i)
		a.Balance.SetUint64(uint64(i) * 1000)
		a.CodeHash = types.BytesToHash(emptyCodeHashBytes)
		a.Initialised = true
		accts[addr20(uint64(i))] = a
	}

	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	r0, err := trc.ComputeRoot(accts, nil)
	if err != nil {
		t.Fatalf("bootstrap ComputeRoot: %v", err)
	}

	// Block N delta: modify a few existing accounts (the kind a block produces).
	touched := []types.Address{addr20(5), addr20(6), addr20(20)}
	dA := map[types.Address]*account.StateAccount{}
	a5 := *accts[touched[0]]
	a5.Balance.SetUint64(555555)
	dA[touched[0]] = &a5
	a6 := *accts[touched[1]]
	a6.Nonce = 99
	dA[touched[1]] = &a6
	a20 := *accts[touched[2]]
	a20.Balance.SetUint64(1)
	dA[touched[2]] = &a20

	// Forward incremental root computation WITH proof capture — the Phase A hook.
	trc.SetIncremental(true)
	trc.EnableProofCapture(true)
	r1, err := trc.ComputeRoot(dA, nil)
	if err != nil {
		t.Fatalf("incremental ComputeRoot: %v", err)
	}
	proof := trc.CapturedProof()
	if len(proof) == 0 {
		t.Fatal("no proof captured from forward computation")
	}
	if r1 == r0 {
		t.Fatal("delta had no effect on the root")
	}

	// (a) The captured multiproof anchors to the computed root and reaches the
	//     touched account leaves.
	pt, err := newPartialTrie(r1[:], proof)
	if err != nil {
		t.Fatalf("captured proof does not anchor to computed root: %v", err)
	}
	for _, a := range touched {
		if _, found, err := pt.get(keybytesToHex(keccak(a[:]))); err != nil || !found {
			t.Fatalf("touched account %s not reachable in captured proof (found=%v err=%v)", a.Hex(), found, err)
		}
	}

	// (b) End-to-end consistency: captured (post) proof + changeset OLD values
	//     recomputes the pre-state root r0 through the P8 stateless consumer.
	bp := &BlockProof{Number: 1, AccountProof: proof}
	for _, a := range touched {
		old := accts[a] // S0 value
		ch := AccountChange{AddrHash: keccak2hash(a), Nonce: old.Nonce, CodeHash: emptyCodeHashBytes}
		ch.Balance.Set(&old.Balance)
		bp.Changes = append(bp.Changes, ch)
	}
	if err := VerifyStateRoot(r1[:], r0[:], bp); err != nil {
		t.Fatalf("captured proof + old changeset must recompute pre-root r0: %v", err)
	}
}

// TestMerkleStageIncrementalWithProof exercises the changeset-sourced wrapper:
// it returns a multiproof that anchors to the root it computes.
func TestMerkleStageIncrementalWithProof(t *testing.T) {
	accts := map[types.Address]*account.StateAccount{}
	for i := 1; i <= 20; i++ {
		a := &account.StateAccount{}
		a.Reset()
		a.Nonce = uint64(i)
		a.Balance.SetUint64(uint64(i) * 1000)
		a.CodeHash = types.BytesToHash(emptyCodeHashBytes)
		a.Initialised = true
		accts[addr20(uint64(i))] = a
	}
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	if _, err := trc.ComputeRoot(accts, nil); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// Apply a block-N delta AND write its AccountChangeSet so the changeset-sourced
	// RetainList finds the touched keys.
	const blockN = uint64(1)
	dA := map[types.Address]*account.StateAccount{}
	a3 := *accts[addr20(3)]
	a3.Balance.SetUint64(424242)
	dA[addr20(3)] = &a3
	if err := writeAccountChangeset(tx, blockN, []types.Address{addr20(3)}, accts); err != nil {
		t.Fatalf("write changeset: %v", err)
	}
	trc.SetIncremental(true)
	if _, err := trc.ComputeRoot(dA, nil); err != nil { // advance state to post
		t.Fatalf("incremental: %v", err)
	}

	root, proof, err := commitment.MerkleStageIncrementalWithProof(tx, blockN, blockN)
	if err != nil {
		t.Fatalf("MerkleStageIncrementalWithProof: %v", err)
	}
	if len(proof) == 0 {
		t.Fatal("no proof returned")
	}
	if _, err := newPartialTrie(root[:], proof); err != nil {
		t.Fatalf("returned proof does not anchor to returned root: %v", err)
	}
}

func keccak2hash(a types.Address) types.Hash { return types.BytesToHash(keccak(a[:])) }

// writeAccountChangeset writes an AccountChangeSet entry per address for blockN
// (value = the address's OLD/S0 encoding), so BuildRetainListFromChangesets
// recovers the touched account keys. Storage untouched here.
func writeAccountChangeset(tx interface {
	Put(table string, k, v []byte) error
}, blockN uint64, addrs []types.Address, s0 map[types.Address]*account.StateAccount) error {
	var bk [8]byte
	bk[0] = byte(blockN >> 56)
	bk[1] = byte(blockN >> 48)
	bk[2] = byte(blockN >> 40)
	bk[3] = byte(blockN >> 32)
	bk[4] = byte(blockN >> 24)
	bk[5] = byte(blockN >> 16)
	bk[6] = byte(blockN >> 8)
	bk[7] = byte(blockN)
	for _, a := range addrs {
		enc := s0[a].MarshalV2()
		v := append(append([]byte(nil), a[:]...), enc...)
		if err := tx.Put("AccountChangeSet", bk[:], v); err != nil {
			return err
		}
	}
	return nil
}
