package serve

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// richBE is a chainBE that also serves bodies + code (for /block, /code).
type richBE struct {
	chainBE
	bodies map[uint64][]byte
	codes  map[types.Hash][]byte
}

func (b *richBE) BodyRLP(n uint64) ([]byte, error) { return b.bodies[n], nil }
func (b *richBE) Code(h types.Hash) ([]byte, error) {
	c, ok := b.codes[h]
	if !ok {
		return nil, http.ErrNoLocation
	}
	return c, nil
}

func newRichBE(n int) *richBE {
	hdrs := emptyStateChain(n)
	be := &richBE{chainBE: chainBE{headers: hdrs, anchorEvery: 1000}, bodies: map[uint64][]byte{}, codes: map[types.Hash][]byte{}}
	for i := 0; i <= n; i++ {
		be.bodies[uint64(i)] = []byte{0xb0, byte(i)} // distinct body per block
	}
	return be
}

// TestBlockBodyRoundTrip: /block returns header||body and HTTPSource.Body parses
// the trailing body bytes back out.
func TestBlockBodyRoundTrip(t *testing.T) {
	be := newRichBE(5)
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	src := NewHTTPSource(srv.URL)
	body, err := src.Body(3)
	if err != nil {
		t.Fatalf("body 3: %v", err)
	}
	if len(body) != 2 || body[0] != 0xb0 || body[1] != 3 {
		t.Fatalf("body 3 round-trip mismatch: %x", body)
	}
}

// TestCodeRoute: /code returns requested bytecodes keyed by keccak; missing
// hashes are omitted.
func TestCodeRoute(t *testing.T) {
	be := newRichBE(2)
	code := []byte{0x60, 0x60, 0x60, 0x40}
	ch := types.BytesToHash(crypto.Keccak256(code))
	be.codes[ch] = code

	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	// /code ships ZSTD(code); HTTPSource.Code decompresses → raw round-trip.
	src := NewHTTPSource(srv.URL)
	got, err := src.Code(ch)
	if err != nil {
		t.Fatalf("fetch code: %v", err)
	}
	if string(got) != string(code) {
		t.Fatalf("code round-trip mismatch: %x != %x", got, code)
	}
	// A missing hash decompresses to nothing (no record).
	missing, err := src.Code(types.BytesToHash([]byte{0xde, 0xad}))
	if err != nil {
		t.Fatalf("missing code: %v", err)
	}
	if len(missing) != 0 {
		t.Fatalf("expected empty for missing hash, got %d bytes", len(missing))
	}
}

// TestHealth: /health returns 200 + JSON with the head.
func TestHealth(t *testing.T) {
	be := newRichBE(3)
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status %d", resp.StatusCode)
	}
}

// TestMaxConcurrent: limitConcurrent caps in-flight requests; the overflow gets
// 503 while the first is still being served.
func TestMaxConcurrent(t *testing.T) {
	enter := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	blocking := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(enter) }) // signal the first request is in-flight
		<-release
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(limitConcurrent(1, blocking))
	defer srv.Close()

	// First request occupies the only slot.
	firstDone := make(chan int, 1)
	go func() {
		resp, err := http.Get(srv.URL + "/")
		if err != nil {
			firstDone <- -1
			return
		}
		firstDone <- resp.StatusCode
		resp.Body.Close()
	}()
	<-enter // first is now blocked inside the handler, holding the slot

	// Second request must be rejected with 503.
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	got := resp.StatusCode
	resp.Body.Close()
	if got != http.StatusServiceUnavailable {
		t.Fatalf("second request status %d, want 503", got)
	}

	close(release)
	if code := <-firstDone; code != http.StatusOK {
		t.Fatalf("first request status %d, want 200", code)
	}
}
