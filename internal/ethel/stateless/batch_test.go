package stateless

import (
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// TestVerifyBatchParallel builds a contiguous run of blocks, each with its own
// BlockProof and header-chain-anchored pre/post roots, and verifies them
// concurrently — asserting all pass and a tampered one is caught.
func TestVerifyBatchParallel(t *testing.T) {
	const nBlocks = 60
	base := mkContractWorld(3, 12)

	var headers []*block.Header
	var proofs []*BlockProof

	initRoot, _, _ := buildAccountTrie(base)
	anchor := mkHeader(0, types.Hash{0xAB})
	copy(anchor.Root[:], initRoot)
	headers = append(headers, anchor)

	prevHash := anchor.Hash()
	for b := 1; b <= nBlocks; b++ {
		blkNum := uint64(b)
		_, post, bp := buildBlockProof(t, base, func(w map[string]*acctState) []AccountChange {
			var ch []AccountChange
			for _, ah := range sortedAddrs(w) {
				a := w[ah]
				a.balance += uint64(b)
				var ahA types.Hash
				copy(ahA[:], ah)
				c := AccountChange{AddrHash: ahA, Nonce: a.nonce, CodeHash: a.codeHash}
				c.Balance.SetUint64(a.balance)
				ch = append(ch, c)
			}
			return ch
		})
		bp.Number = blkNum
		proofs = append(proofs, bp)

		h := mkHeader(blkNum, prevHash)
		copy(h.Root[:], post)
		headers = append(headers, h)
		prevHash = h.Hash()
	}

	hc, err := NewHeaderChain(headers[0])
	if err != nil {
		t.Fatal(err)
	}
	if acc, err := hc.ExtendBatch(headers[1:]); err != nil {
		t.Fatalf("ExtendBatch: %v (accepted %d)", err, acc)
	}

	results := VerifyBatch(hc, proofs, 8)
	if n := CountVerified(results); n != nBlocks {
		for _, r := range results {
			if r.Err != nil {
				t.Logf("block %d: %v", r.Number, r.Err)
			}
		}
		t.Fatalf("verified %d/%d", n, nBlocks)
	}

	// tamper one proof's changeset → that block must fail, others still pass
	if len(proofs[10].Changes) > 0 {
		proofs[10].Changes[0].Balance.AddUint64(&proofs[10].Changes[0].Balance, 1)
	}
	results = VerifyBatch(hc, proofs, 8)
	if results[10].Err == nil {
		t.Fatal("tampered block verified")
	}
	if n := CountVerified(results); n != nBlocks-1 {
		t.Fatalf("after tamper verified %d, want %d", n, nBlocks-1)
	}
}
