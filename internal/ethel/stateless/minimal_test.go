package stateless

import (
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// TestMinimalVerifierEndToEnd drives the full minimal-node flow: anchor →
// extend headers → verify a window in parallel → attest verified blocks →
// aggregator counts; a verify-only (no key) node cannot attest.
func TestMinimalVerifierEndToEnd(t *testing.T) {
	const nBlocks = 24
	base := mkContractWorld(9, 10)

	initRoot, _, _ := buildAccountTrie(base)
	anchor := mkHeader(0, types.Hash{0xAB})
	copy(anchor.Root[:], initRoot)

	var headers []*block.Header
	var proofs []*BlockProof
	prevHash := anchor.Hash()
	for b := 1; b <= nBlocks; b++ {
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
		bp.Number = uint64(b)
		proofs = append(proofs, bp)
		h := mkHeader(uint64(b), prevHash)
		copy(h.Root[:], post)
		headers = append(headers, h)
		prevHash = h.Hash()
	}

	key, _ := crypto.GenerateKey()
	mv, err := NewMinimalVerifier(anchor, key)
	if err != nil {
		t.Fatal(err)
	}
	if acc, err := mv.ExtendHeaders(headers); err != nil {
		t.Fatalf("ExtendHeaders: %v (acc %d)", err, acc)
	}
	if n, _ := mv.Head(); n != nBlocks {
		t.Fatalf("head %d != %d", n, nBlocks)
	}

	results := mv.VerifyWindow(proofs, 4)
	if n := CountVerified(results); n != nBlocks {
		t.Fatalf("verified %d/%d", n, nBlocks)
	}

	pool := NewAttestationPool(map[types.Address]bool{crypto.PubkeyToAddress(key.PublicKey): true})
	for _, bp := range proofs {
		att, err := mv.AttestVerified(bp)
		if err != nil {
			t.Fatalf("attest block %d: %v", bp.Number, err)
		}
		if _, added, err := pool.Add(att); err != nil || !added {
			t.Fatalf("pool add block %d: added=%v err=%v", bp.Number, added, err)
		}
	}
	sr, _ := mv.hc.TrustedStateRoot(5)
	rr, _ := mv.hc.TrustedReceiptRoot(5)
	if !pool.Finalized(5, sr, rr, 1) {
		t.Fatal("block 5 not finalized at threshold 1")
	}

	vo, _ := NewMinimalVerifier(anchor, nil)
	vo.ExtendHeaders(headers)
	if _, err := vo.AttestVerified(proofs[0]); err == nil {
		t.Fatal("verify-only node attested")
	}
}
