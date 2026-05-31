package stateless

import (
	"context"
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// memSource is an in-memory Source: a pre-built forward chain (headers + anchor
// proofs) standing in for a producer RPC.
type memSource struct {
	headers map[uint64]*block.Header
	anchors map[uint64]*BlockProof
	tip     uint64
}

func (s *memSource) Head() (uint64, error) { return s.tip, nil }
func (s *memSource) Header(n uint64) (*block.Header, error) {
	h := s.headers[n]
	if h == nil {
		return nil, fmt.Errorf("no header %d", n)
	}
	return h, nil
}
func (s *memSource) Anchor(n uint64) (*BlockProof, error) {
	bp := s.anchors[n]
	if bp == nil {
		return nil, fmt.Errorf("no anchor %d", n)
	}
	return bp, nil
}

// buildChain forward-builds N account-modification blocks (anchors every K),
// returning a memSource (headers + pre-state anchor proofs) and the genesis
// header. Mirrors the producer: pre-proof captured at the N-1 trie before
// applying block N.
func buildAnchorChain(t *testing.T, N int, K uint64) (*memSource, *block.Header) {
	t.Helper()
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
	t.Cleanup(func() { tx.Rollback() })

	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false)
	r0, err := trc.ComputeRoot(accts, nil)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	headers := map[uint64]*block.Header{0: mkHdr(0, types.Hash{}, r0)}
	anchors := map[uint64]*BlockProof{}

	trc.SetIncremental(true)
	for a := uint64(1); a <= uint64(N); a++ {
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
		var bp *BlockProof
		if a%K == 0 {
			preRoot, pp, perr := commitment.ExtractBlockMultiproof(tx, a, a)
			if perr != nil {
				t.Fatalf("extract %d: %v", a, perr)
			}
			if preRoot != headers[a-1].Root {
				t.Fatalf("anchor %d preRoot mismatch", a)
			}
			bp = &BlockProof{Number: a, AccountProof: pp}
			for ad, na := range dA {
				ch := AccountChange{AddrHash: keccak2hash(ad), Nonce: na.Nonce, CodeHash: emptyCodeHashBytes}
				ch.Balance.Set(&na.Balance)
				bp.Changes = append(bp.Changes, ch)
			}
		}
		rn, err := trc.ComputeRoot(dA, nil)
		if err != nil {
			t.Fatalf("apply %d: %v", a, err)
		}
		for k, v := range dA {
			accts[k] = v
		}
		headers[a] = mkHdr(a, headers[a-1].Hash(), rn)
		if bp != nil {
			anchors[a] = bp
		}
	}
	return &memSource{headers: headers, anchors: anchors, tip: uint64(N)}, headers[0]
}

// TestMinimalClientRollingRetention drives the minimal client over a chain
// longer than its retention window and asserts: it reaches the tip verifying ①
// (header chain) + ③ (MPT anchors); the retained window stays bounded; and the
// trust root re-anchors forward to a verified anchor (so trust no longer depends
// on the genesis checkpoint or pruned data).
func TestMinimalClientRollingRetention(t *testing.T) {
	const N = 60
	const K = uint64(5)
	const retention = uint64(20)

	src, anchor := buildAnchorChain(t, N, K)
	c, err := NewMinimalClient(src, anchor, retention, K)
	if err != nil {
		t.Fatal(err)
	}

	head, err := c.Sync()
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if head != N {
		t.Fatalf("head %d != tip %d", head, N)
	}

	// Rolling window bounded.
	if got := c.Retained(); uint64(got) > retention+K+2 {
		t.Fatalf("retained window too large: %d (retention %d)", got, retention)
	}

	// Trust root advanced (re-anchored) past the genesis checkpoint, and lands on
	// a verified anchor block.
	if c.ReanchorCount() == 0 {
		t.Fatal("expected at least one re-anchor over a chain longer than retention")
	}
	tr, _ := c.TrustRoot()
	if tr == 0 {
		t.Fatal("trust root should have advanced past genesis (0)")
	}
	if tr%K != 0 {
		t.Fatalf("trust root %d is not a verified anchor block (K=%d)", tr, K)
	}
	if head-tr > retention {
		t.Fatalf("trust root %d lags head %d by more than retention %d", tr, head, retention)
	}
}

// TestMinimalClientRejectsBadAnchor: a tampered anchor proof is rejected at ③.
func TestMinimalClientRejectsBadAnchor(t *testing.T) {
	const N = 12
	const K = uint64(4)
	src, anchor := buildAnchorChain(t, N, K)
	// Corrupt the anchor at block 8: claim a phantom account change so the
	// recomputed root diverges.
	bad := *src.anchors[8]
	bad.Changes = append([]AccountChange{}, bad.Changes...)
	var e AccountChange
	e.AddrHash = types.BytesToHash([]byte{0xfe, 0xed})
	e.Nonce = 7
	bad.Changes = append(bad.Changes, e)
	src.anchors[8] = &bad

	c, err := NewMinimalClient(src, anchor, 1000, K)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Sync(); err == nil {
		t.Fatal("expected layer③ rejection of tampered anchor")
	}
}
