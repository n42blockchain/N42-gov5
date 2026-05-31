package ethel

import (
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/params"
)

// memSrc is an in-memory stateless.Source over a pre-built header chain.
type memSrc struct {
	headers     map[uint64]*block.Header
	anchorEvery uint64
	tip         uint64
}

func (s *memSrc) Head() (uint64, error) { return s.tip, nil }
func (s *memSrc) Header(n uint64) (*block.Header, error) {
	h := s.headers[n]
	if h == nil {
		return nil, fmt.Errorf("no header %d", n)
	}
	return h, nil
}
func (s *memSrc) Anchor(n uint64) (*stateless.BlockProof, error) {
	if s.anchorEvery == 0 || n%s.anchorEvery != 0 {
		return nil, fmt.Errorf("not an anchor height %d", n)
	}
	return &stateless.BlockProof{Number: n}, nil // empty changeset (empty-state chain)
}

// TestMinimalClientLayer2Wired drives a minimal client with the layer-② hook
// installed (WireMinimalExec): every block is header-extended (①), witness-
// replayed for receiptRoot (②), and anchor-verified every K (③). Uses an
// empty-state, post-merge/pre-Shanghai chain so the empty block replays to the
// empty receipts root with no state change.
func TestMinimalClientLayer2Wired(t *testing.T) {
	const base = uint64(16000000)
	const N = uint64(20)
	const K = uint64(5)
	cfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(cfg)
	empty := emptyTrieRoot()

	headers := map[uint64]*block.Header{}
	anchorHdr := mkHeader(base, tsAnchor, types.Hash{}, empty, empty)
	headers[base] = anchorHdr
	parent := anchorHdr
	for i := uint64(1); i <= N; i++ {
		h := mkHeader(base+i, tsAnchor+i, parent.Hash(), empty, empty)
		headers[base+i] = h
		parent = h
	}
	src := &memSrc{headers: headers, anchorEvery: K, tip: base + N}

	mc, err := stateless.NewMinimalClient(src, anchorHdr, 1<<30, K) // no prune in this test
	if err != nil {
		t.Fatal(err)
	}
	WireMinimalExec(mc, cfg, engine,
		func(uint64) (*GethBodyResult, error) { return &GethBodyResult{}, nil }, // empty body
		func(uint64) ([]byte, error) { return nil, nil },                        // empty witness
		nil, nil,                                                                // no senders/code
	)

	head, err := mc.Sync()
	if err != nil {
		t.Fatalf("three-layer sync: %v", err)
	}
	if head != base+N {
		t.Fatalf("head %d != tip %d", head, base+N)
	}
}

// TestMinimalClientLayer2RejectsBadReceipt: if a block's header claims a wrong
// receiptRoot, the wired layer-② replay rejects it (the empty block replays to
// the empty receipts root, not the claimed one).
func TestMinimalClientLayer2RejectsBadReceipt(t *testing.T) {
	const base = uint64(16000000)
	const K = uint64(1000)
	cfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(cfg)
	empty := emptyTrieRoot()

	anchorHdr := mkHeader(base, tsAnchor, types.Hash{}, empty, empty)
	// Block base+1 claims a bogus receiptRoot (but valid empty state root).
	bad := mkHeader(base+1, tsAnchor+1, anchorHdr.Hash(), empty, types.BytesToHash([]byte{0xba, 0xd0}))
	src := &memSrc{headers: map[uint64]*block.Header{base: anchorHdr, base + 1: bad}, anchorEvery: K, tip: base + 1}

	mc, _ := stateless.NewMinimalClient(src, anchorHdr, 1<<30, K)
	WireMinimalExec(mc, cfg, engine,
		func(uint64) (*GethBodyResult, error) { return &GethBodyResult{}, nil },
		func(uint64) ([]byte, error) { return nil, nil },
		nil, nil,
	)
	if _, err := mc.Sync(); err == nil {
		t.Fatal("expected layer② rejection of wrong receiptRoot")
	}
}
