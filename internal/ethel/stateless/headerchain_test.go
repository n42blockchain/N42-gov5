package stateless

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// mkHeader builds a header at number n whose ParentHash points at parent (the
// real Hash() of the previous header), with deterministic stateRoot/receiptRoot
// derived from n so we can assert the chain records the right targets.
func mkHeader(n uint64, parent types.Hash) *block.Header {
	h := &block.Header{}
	h.Number = uint256.NewInt(n)
	h.ParentHash = parent
	h.Difficulty = uint256.NewInt(1)
	var sr, rr types.Hash
	sr[0], sr[1] = byte(n), byte(n>>8)
	rr[0], rr[1] = byte(n>>16), byte(n>>24)
	h.Root = sr
	h.ReceiptHash = rr
	return h
}

// buildChain returns a contiguous run of headers [start, start+count) with a
// valid parentHash hash-chain (each child's ParentHash == real Hash() of prev).
func buildChain(start uint64, count int) []*block.Header {
	out := make([]*block.Header, 0, count)
	var parent types.Hash
	if count > 0 {
		// anchor's parent is arbitrary; first returned header is the anchor.
		parent[0] = 0xaa
	}
	for i := 0; i < count; i++ {
		h := mkHeader(start+uint64(i), parent)
		parent = h.Hash() // next child points at this header's real hash
		out = append(out, h)
	}
	return out
}

func TestHeaderChainExtend(t *testing.T) {
	chain := buildChain(25096000, 200)
	hc, err := NewHeaderChain(chain[0])
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := hc.ExtendBatch(chain[1:])
	if err != nil {
		t.Fatalf("ExtendBatch: %v (accepted %d)", err, accepted)
	}
	if accepted != 199 {
		t.Fatalf("accepted %d, want 199", accepted)
	}
	headNum, _ := hc.Head()
	if headNum != 25096199 {
		t.Fatalf("head %d, want 25096199", headNum)
	}
	// every block's trusted stateRoot/receiptRoot must match what we put in.
	for i, h := range chain {
		n := chain[0].Number.Uint64() + uint64(i)
		sr, ok := hc.TrustedStateRoot(n)
		if !ok || sr != h.Root {
			t.Fatalf("block %d: trusted stateRoot %x != %x (ok=%v)", n, sr[:4], h.Root[:4], ok)
		}
		rr, ok := hc.TrustedReceiptRoot(n)
		if !ok || rr != h.ReceiptHash {
			t.Fatalf("block %d: trusted receiptRoot mismatch", n)
		}
	}
}

// A header whose parentHash doesn't match the head must be rejected.
func TestHeaderChainBrokenLink(t *testing.T) {
	chain := buildChain(100, 5)
	hc, _ := NewHeaderChain(chain[0])
	bad := mkHeader(101, types.Hash{0xde, 0xad}) // wrong parent
	if err := hc.Extend(bad); err == nil {
		t.Fatal("expected broken-chain error, got nil")
	}
	// a tampered stateRoot changes the hash, so the NEXT real child would break;
	// here directly: feed the real child[2] (num 102) onto head=100 → non-contiguous.
	if err := hc.Extend(chain[2]); err == nil {
		t.Fatal("expected non-contiguous error, got nil")
	}
}

// Non-contiguous number is rejected even if hash-linked.
func TestHeaderChainGap(t *testing.T) {
	chain := buildChain(0, 10)
	hc, _ := NewHeaderChain(chain[0])
	if _, err := hc.ExtendBatch(chain[1:4]); err != nil {
		t.Fatal(err)
	}
	// skip chain[4], try chain[5] → gap
	if err := hc.Extend(chain[5]); err == nil {
		t.Fatal("expected gap error, got nil")
	}
}

// Prune keeps a rolling window and the anchor.
func TestHeaderChainPrune(t *testing.T) {
	chain := buildChain(1000, 100)
	hc, _ := NewHeaderChain(chain[0])
	hc.ExtendBatch(chain[1:])
	hc.Prune(1050)
	// anchor (1000) kept despite < 1050
	if _, ok := hc.TrustedStateRoot(1000); !ok {
		t.Fatal("anchor pruned")
	}
	// 1049 dropped
	if _, ok := hc.TrustedStateRoot(1049); ok {
		t.Fatal("1049 should be pruned")
	}
	// 1050 kept
	if _, ok := hc.TrustedStateRoot(1050); !ok {
		t.Fatal("1050 should be kept")
	}
}
