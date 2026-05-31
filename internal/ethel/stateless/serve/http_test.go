package serve

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// chainBE is a Backend over a synthetic empty-state header chain: every header's
// stateRoot is the empty-MPT root, so an empty-changeset anchor proof verifies
// (emptyRoot -> emptyRoot) — exercises the transport + minimal-client wiring (①③)
// without an EVM. Headers serialize via block.Header.Marshal so Hash() survives
// the wire.
type chainBE struct {
	headers     []*block.Header
	anchorEvery uint64
}

func (b *chainBE) Head() (uint64, types.Hash, uint64, error) {
	last := b.headers[len(b.headers)-1]
	n := last.Number.Uint64()
	return n, last.Hash(), n - n%b.anchorEvery, nil
}
func (b *chainBE) HeaderRLP(n uint64) ([]byte, error) {
	if n >= uint64(len(b.headers)) {
		return nil, errors.New("gap")
	}
	return b.headers[n].Marshal()
}
func (b *chainBE) BodyRLP(n uint64) ([]byte, error) { return []byte{}, nil }
func (b *chainBE) Witness(n uint64) ([]byte, error) { return []byte{}, nil }
func (b *chainBE) Anchor(n uint64) ([]byte, error) {
	if b.anchorEvery == 0 || n%b.anchorEvery != 0 {
		return nil, errors.New("not an anchor height")
	}
	return stateless.EncodeBlockProof(&stateless.BlockProof{Number: n}), nil
}
func (b *chainBE) Code(types.Hash) ([]byte, error) { return nil, errors.New("no code") }

func mkH(num uint64, parent, root types.Hash) *block.Header {
	return &block.Header{
		Number:      uint256.NewInt(num),
		ParentHash:  parent,
		Root:        root,
		ReceiptHash: root,
		Difficulty:  uint256.NewInt(0), // ToProtoMessage/Marshal requires non-nil
		BaseFee:     uint256.NewInt(0),
	}
}

func emptyStateChain(n int) []*block.Header {
	root := types.BytesToHash(crypto.Keccak256([]byte{0x80})) // empty MPT root
	hdrs := []*block.Header{mkH(0, types.Hash{}, root)}
	for i := 1; i <= n; i++ {
		hdrs = append(hdrs, mkH(uint64(i), hdrs[i-1].Hash(), root))
	}
	return hdrs
}

// TestHTTPEndToEnd: a minimal client syncs over the HTTP transport — fetching
// headers (①) and MPT anchors (③) from the serve Handler and verifying them —
// proving the P-B service ↔ P-C client wire works end to end.
func TestHTTPEndToEnd(t *testing.T) {
	const N = 30
	const K = uint64(5)
	hdrs := emptyStateChain(N)
	be := &chainBE{headers: hdrs, anchorEvery: K}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil))
	defer srv.Close()

	src := NewHTTPSource(srv.URL)

	// Transport faithfulness: a header's Hash survives Marshal → wire → Unmarshal.
	h5, err := src.Header(5)
	if err != nil {
		t.Fatalf("fetch header 5: %v", err)
	}
	if h5.Hash() != hdrs[5].Hash() {
		t.Fatalf("header hash round-trip mismatch: %x != %x", h5.Hash(), hdrs[5].Hash())
	}

	// End-to-end: drive the minimal client off the HTTP source.
	mc, err := stateless.NewMinimalClient(src, hdrs[0], 1000, K)
	if err != nil {
		t.Fatal(err)
	}
	head, err := mc.Sync()
	if err != nil {
		t.Fatalf("sync over HTTP: %v", err)
	}
	if head != N {
		t.Fatalf("synced head %d != tip %d", head, N)
	}
}

// TestHTTPRateLimit: the per-IP request-rate middleware returns 429 once a
// client exceeds its burst.
func TestHTTPRateLimit(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	rl := jsonrpc.NewRateLimiter(&jsonrpc.RateLimitConfig{RequestsPerSecond: 1, BurstSize: 2})
	defer rl.Stop()
	srv := httptest.NewServer(Handler(svc, rl))
	defer srv.Close()

	c := &http.Client{}
	got429 := false
	for i := 0; i < 10; i++ {
		resp, err := c.Get(srv.URL + "/head")
		if err != nil {
			t.Fatal(err)
		}
		code := resp.StatusCode
		resp.Body.Close()
		if code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("expected HTTP 429 after exceeding burst")
	}
}
