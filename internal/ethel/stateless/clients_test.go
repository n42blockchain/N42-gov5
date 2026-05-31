package stateless

import (
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// fullMem adds bodies to a memSource (a FullSource).
type fullMem struct {
	*memSource
	bodies map[uint64][]byte
}

func (f *fullMem) Body(n uint64) ([]byte, error) {
	b, ok := f.bodies[n]
	if !ok {
		return nil, fmt.Errorf("no body %d", n)
	}
	return b, nil
}

// archMem adds witnesses (an ArchiveSource).
type archMem struct {
	*fullMem
	wits map[uint64][]byte
}

func (a *archMem) Witness(n uint64) ([]byte, error) { return a.wits[n], nil }

func newFullMem(src *memSource) *fullMem {
	bodies := map[uint64][]byte{}
	for n := range src.headers {
		bodies[n] = []byte(fmt.Sprintf("body-%d", n))
	}
	return &fullMem{memSource: src, bodies: bodies}
}

// TestFullClientArchivesChain: the full client follows the header chain (①) from
// genesis to tip and stores every header+body, with an optional body verifier.
func TestFullClientArchivesChain(t *testing.T) {
	const N = 20
	const K = uint64(5)
	src, genesis := buildAnchorChain(t, N, K)
	fs := newFullMem(src)

	stored := map[uint64][]byte{}
	storedHdr := map[uint64]types.Hash{}
	verifierCalls := 0
	verify := func(h *block.Header, body []byte) error {
		verifierCalls++
		if string(body) != fmt.Sprintf("body-%d", h.Number.Uint64()) {
			return fmt.Errorf("body mismatch")
		}
		return nil
	}
	store := func(n uint64, h *block.Header, body []byte) error {
		stored[n] = body
		storedHdr[n] = h.Hash()
		return nil
	}

	fc, err := NewFullClient(fs, genesis, store, verify)
	if err != nil {
		t.Fatal(err)
	}
	head, err := fc.Sync()
	if err != nil {
		t.Fatalf("full sync: %v", err)
	}
	if head != N {
		t.Fatalf("head %d != tip %d", head, N)
	}
	for n := uint64(0); n <= N; n++ {
		if _, ok := stored[n]; !ok {
			t.Fatalf("block %d not archived", n)
		}
		if storedHdr[n] != src.headers[n].Hash() {
			t.Fatalf("block %d stored wrong header hash", n)
		}
	}
	if verifierCalls != N+1 { // genesis + N
		t.Fatalf("verifier called %d times, want %d", verifierCalls, N+1)
	}
}

// TestFullClientRejectsBadBody: a body that fails the verifier stops the sync.
func TestFullClientRejectsBadBody(t *testing.T) {
	const N = 10
	src, genesis := buildAnchorChain(t, N, 5)
	fs := newFullMem(src)
	fs.bodies[6] = []byte("corrupt") // block 6 body won't match the verifier

	verify := func(h *block.Header, body []byte) error {
		if string(body) != fmt.Sprintf("body-%d", h.Number.Uint64()) {
			return fmt.Errorf("body mismatch at %d", h.Number.Uint64())
		}
		return nil
	}
	fc, _ := NewFullClient(fs, genesis, func(uint64, *block.Header, []byte) error { return nil }, verify)
	head, err := fc.Sync()
	if err == nil {
		t.Fatal("expected body verify failure")
	}
	if head != 5 {
		t.Fatalf("archive should stop at last good block 5, got %d", head)
	}
}

// TestArchiveClientForwardExecutes: the archive client forward-executes genesis→
// tip, checking each computed state root against the trusted header root (③), and
// reports AtTip when caught up.
func TestArchiveClientForwardExecutes(t *testing.T) {
	const N = 15
	src, genesis := buildAnchorChain(t, N, 5)
	as := &archMem{fullMem: newFullMem(src), wits: map[uint64][]byte{}}
	for n := range src.headers {
		as.wits[n] = []byte(fmt.Sprintf("wit-%d", n))
	}

	executed := map[uint64]bool{}
	// A faithful executor: it "computes" the correct post-state root (here, the
	// trusted header root) and records that it ran with the right artifacts.
	exec := func(n uint64, h *block.Header, body, wit []byte, ancestor func(uint64) types.Hash) (types.Hash, error) {
		if string(body) != fmt.Sprintf("body-%d", n) || string(wit) != fmt.Sprintf("wit-%d", n) {
			return types.Hash{}, fmt.Errorf("wrong artifacts at %d", n)
		}
		executed[n] = true
		return h.Root, nil
	}

	ac, err := NewArchiveClient(as, genesis, exec)
	if err != nil {
		t.Fatal(err)
	}
	head, err := ac.Sync()
	if err != nil {
		t.Fatalf("archive sync: %v", err)
	}
	if head != N {
		t.Fatalf("head %d != tip %d", head, N)
	}
	for n := uint64(1); n <= N; n++ {
		if !executed[n] {
			t.Fatalf("block %d not executed", n)
		}
	}
	if at, _ := ac.AtTip(); !at {
		t.Fatal("archive should report AtTip after catching up")
	}
}

// TestArchiveClientRejectsBadStateRoot: an executor returning the wrong state
// root is caught at ③ (computed root != trusted header root).
func TestArchiveClientRejectsBadStateRoot(t *testing.T) {
	const N = 8
	src, genesis := buildAnchorChain(t, N, 4)
	as := &archMem{fullMem: newFullMem(src), wits: map[uint64][]byte{}}
	exec := func(n uint64, h *block.Header, body, wit []byte, ancestor func(uint64) types.Hash) (types.Hash, error) {
		if n == 5 {
			return types.BytesToHash([]byte{0xba, 0xd0}), nil // wrong root at block 5
		}
		return h.Root, nil
	}
	ac, _ := NewArchiveClient(as, genesis, exec)
	head, err := ac.Sync()
	if err == nil {
		t.Fatal("expected ③ state-root rejection")
	}
	if head != 4 {
		t.Fatalf("archive should stop at last good block 4, got %d", head)
	}
}

// divergentSrc wraps a memSource, returning a tampered header at one block.
type divergentSrc struct {
	*memSource
	at  uint64
	bad *block.Header
}

func (d *divergentSrc) Header(n uint64) (*block.Header, error) {
	if n == d.at {
		return d.bad, nil
	}
	return d.memSource.Header(n)
}

// TestMultiSourceQuorum: with 3 producers and quorum 2, a single producer that
// serves a divergent header for a block is outvoted (the two honest agree), but
// raising quorum to 3 surfaces the disagreement as an error.
func TestMultiSourceQuorum(t *testing.T) {
	const N = 12
	const K = uint64(4)
	src, _ := buildAnchorChain(t, N, K)
	// Two honest sources (identical deterministic chain) + one liar at block 7.
	bad := mkHdr(7, types.Hash{0x99}, types.Hash{0x88}) // wrong parent+root → different hash
	liar := &divergentSrc{memSource: src, at: 7, bad: bad}

	ms2, err := NewMultiSource([]Source{src, src, liar}, 2)
	if err != nil {
		t.Fatal(err)
	}
	h7, err := ms2.Header(7)
	if err != nil {
		t.Fatalf("quorum 2 should outvote the liar: %v", err)
	}
	if h7.Hash() != src.headers[7].Hash() {
		t.Fatal("quorum 2 returned the wrong (tampered) header")
	}
	// Head agreement: all three agree on the tip (liar only diverges at 7).
	if tip, err := ms2.Head(); err != nil || tip != N {
		t.Fatalf("quorum head: tip=%d err=%v", tip, err)
	}
	// Anchor agreement at an anchor height.
	if _, err := ms2.Anchor(K); err != nil {
		t.Fatalf("quorum 2 anchor: %v", err)
	}

	// Quorum 3: the liar breaks unanimity at block 7.
	ms3, _ := NewMultiSource([]Source{src, src, liar}, 3)
	if _, err := ms3.Header(7); err == nil {
		t.Fatal("quorum 3 should report the disagreement at block 7")
	}
	// Non-divergent blocks still pass at quorum 3.
	if _, err := ms3.Header(6); err != nil {
		t.Fatalf("quorum 3 should pass on agreed block 6: %v", err)
	}
}
