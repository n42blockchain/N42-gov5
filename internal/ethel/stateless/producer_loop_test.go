package stateless

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state/commitment"

	"github.com/holiman/uint256"
)

func mkHdr(num uint64, parent, root types.Hash) *block.Header {
	return &block.Header{
		Number:      uint256.NewInt(num),
		ParentHash:  parent,
		Root:        root,
		ReceiptHash: root,
	}
}

// TestContinuousProduceVerifyLoop is option B's producer→verify closed loop.
// A forward chain advances the trie block by block (the same shape as replaying
// acctcs/storcs forward through MerkleStageIncremental); at each ANCHOR block
// (every K) the producer captures the block's pre-state MPT multiproof from the
// N-1 trie. Each block is emitted as a StatelessBundle (light = no proof; anchor
// = with proof), serialized and decoded, then a minimal client verifies the
// header chain (① every block) and the MPT anchor (③ every K) — the cadence
// model sized at ~2 MB / 100 blocks. (Layer ② per-block witness execution is
// covered by the empty-block VerifyBlockFull test; synthetic state-changing
// blocks have no real witness to replay.)
func TestContinuousProduceVerifyLoop(t *testing.T) {
	const N = 12
	const K = uint64(4)

	accts := map[types.Address]*account.StateAccount{}
	for i := 1; i <= 40; i++ {
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
		t.Fatalf("bootstrap: %v", err)
	}
	headers := []*block.Header{mkHdr(0, types.Hash{}, r0)}

	type prod struct {
		anchor  bool
		proof   [][]byte
		changes []AccountChange
	}
	var prods []prod

	trc.SetIncremental(true)
	for a := uint64(1); a <= N; a++ {
		// Block a: modify a deterministic set of existing accounts.
		dA := map[types.Address]*account.StateAccount{}
		for _, mul := range []uint64{7, 13, 5} {
			ad := addr20((a*mul)%40 + 1)
			na := *accts[ad]
			na.Balance.SetUint64(1_000_000 + a)
			na.Nonce = a
			dA[ad] = &na
		}
		touched := make([]types.Address, 0, len(dA))
		for ad := range dA {
			touched = append(touched, ad)
		}
		if err := writeAccountChangeset(tx, a, touched, accts); err != nil {
			t.Fatalf("changeset %d: %v", a, err)
		}

		anchor := a%K == 0
		var preProof [][]byte
		if anchor {
			// PRE-state proof: capture from the N-1 trie BEFORE applying block a.
			preRoot, pp, perr := commitment.ExtractBlockMultiproof(tx, a, a)
			if perr != nil {
				t.Fatalf("extract %d: %v", a, perr)
			}
			if preRoot != headers[a-1].Root {
				t.Fatalf("anchor %d preRoot %x != header[%d].Root %x", a, preRoot[:4], a-1, headers[a-1].Root[:4])
			}
			preProof = pp
		}

		rn, err := trc.ComputeRoot(dA, nil) // advance trie to block a
		if err != nil {
			t.Fatalf("apply %d: %v", a, err)
		}
		for k, v := range dA {
			accts[k] = v
		}
		headers = append(headers, mkHdr(a, headers[a-1].Hash(), rn))

		var changes []AccountChange
		if anchor {
			for ad, na := range dA {
				ch := AccountChange{AddrHash: keccak2hash(ad), Nonce: na.Nonce, CodeHash: emptyCodeHashBytes}
				ch.Balance.Set(&na.Balance)
				changes = append(changes, ch)
			}
		}
		prods = append(prods, prod{anchor: anchor, proof: preProof, changes: changes})
	}

	// Minimal client: trust header[0], extend the chain (① every block).
	mv, err := NewMinimalVerifier(headers[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mv.ExtendHeaders(headers[1:]); err != nil {
		t.Fatalf("extend headers: %v", err)
	}

	// Emit each block as a bundle, serialize→decode, verify the anchors (③).
	anchorsOK := 0
	for i, p := range prods {
		a := uint64(i + 1)
		var bp *BlockProof
		if p.anchor {
			bp = &BlockProof{Number: a, AccountProof: p.proof, Changes: p.changes}
		}
		bundle := &StatelessBundle{Number: a, Proof: bp}
		dec, derr := DecodeBundle(bundle.Encode())
		if derr != nil {
			t.Fatalf("bundle %d round-trip: %v", a, derr)
		}
		if dec.Proof == nil {
			continue // light bundle: stateRoot trusted via the header chain (①)
		}
		if err := mv.VerifyBlock(dec.Proof); err != nil {
			t.Fatalf("anchor %d layer③ verify: %v", a, err)
		}
		anchorsOK++
	}

	wantAnchors := N / int(K)
	if anchorsOK != wantAnchors {
		t.Fatalf("anchors verified %d, want %d", anchorsOK, wantAnchors)
	}
	headNum, _ := mv.Head()
	if headNum != N {
		t.Fatalf("header chain head %d, want %d", headNum, N)
	}
}
