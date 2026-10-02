package serve

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
)

// fullBE extends chainBE with the optional capabilities (AccountProof,
// AccountMultiproof, Code, AnchorHeights) so client_test.go can drive every
// HTTPSource method against a real Handler.
type fullBE struct {
	chainBE
	codeByHash map[types.Hash][]byte
}

func (b *fullBE) Code(h types.Hash) ([]byte, error) {
	if c, ok := b.codeByHash[h]; ok {
		return c, nil
	}
	return nil, ErrNotSupported
}

func (b *fullBE) AccountMultiproof(addrs []types.Address) ([]byte, error) {
	resp := AccountMultiproofResponse{Block: 1, Addrs: addrs, ProofNodes: [][]byte{{0x01, 0x02}}}
	return json.Marshal(resp)
}

func (b *fullBE) AccountProof(addr types.Address, slots []types.Hash) ([]byte, error) {
	resp := AccountProofResponse{Block: 1, Proof: &account.AccProofResult{Address: addr}}
	return json.Marshal(resp)
}

func (b *fullBE) AnchorHeights(from, to uint64) ([]uint64, error) {
	var out []uint64
	for n := from; n <= to; n += b.anchorEvery {
		out = append(out, n)
	}
	return out, nil
}

func TestHTTPSourceClientMethods(t *testing.T) {
	const N = 20
	const K = uint64(5)
	hdrs := emptyStateChain(N)
	be := &fullBE{
		chainBE:    chainBE{headers: hdrs, anchorEvery: K},
		codeByHash: map[types.Hash][]byte{},
	}
	hash := types.BytesToHash([]byte("deadbeefdeadbeefdeadbeefdeadbeef"))
	be.codeByHash[hash] = []byte{0x60, 0x00, 0x60, 0x00}

	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	src := NewHTTPSource(srv.URL)

	t.Run("Head", func(t *testing.T) {
		n, err := src.Head()
		if err != nil || n != N {
			t.Fatalf("Head() = %d, %v; want %d, nil", n, err, N)
		}
	})

	t.Run("HeadersFrom", func(t *testing.T) {
		hs, err := src.HeadersFrom(0, 5)
		if err != nil {
			t.Fatalf("HeadersFrom: %v", err)
		}
		if len(hs) != 5 {
			t.Fatalf("HeadersFrom returned %d headers, want 5", len(hs))
		}
		for i, h := range hs {
			if h.Hash() != hdrs[i].Hash() {
				t.Errorf("header %d hash mismatch", i)
			}
		}
	})

	t.Run("AnchorBytes and Anchor", func(t *testing.T) {
		raw, err := src.AnchorBytes(K)
		if err != nil || len(raw) == 0 {
			t.Fatalf("AnchorBytes: %v, len=%d", err, len(raw))
		}
		bp, err := src.Anchor(K)
		if err != nil {
			t.Fatalf("Anchor: %v", err)
		}
		if bp.Number != K {
			t.Errorf("Anchor.Number = %d, want %d", bp.Number, K)
		}
	})

	t.Run("GetWitness and Witness alias", func(t *testing.T) {
		if _, err := src.GetWitness(1); err != nil {
			t.Fatalf("GetWitness: %v", err)
		}
		if _, err := src.Witness(1); err != nil {
			t.Fatalf("Witness: %v", err)
		}
	})

	t.Run("AnchorHeights", func(t *testing.T) {
		hs, err := src.AnchorHeights(0, 15)
		if err != nil {
			t.Fatalf("AnchorHeights: %v", err)
		}
		want := []uint64{0, 5, 10, 15}
		if len(hs) != len(want) {
			t.Fatalf("AnchorHeights = %v, want %v", hs, want)
		}
		for i := range want {
			if hs[i] != want[i] {
				t.Errorf("AnchorHeights[%d] = %d, want %d", i, hs[i], want[i])
			}
		}
	})

	t.Run("AccountProof", func(t *testing.T) {
		addr := types.Address{0x01}
		res, err := src.AccountProof(addr, []types.Hash{{0x02}})
		if err != nil {
			t.Fatalf("AccountProof: %v", err)
		}
		if res.Proof == nil || res.Proof.Address != addr {
			t.Errorf("AccountProof response mismatch: %+v", res)
		}
	})

	t.Run("AccountMultiproof", func(t *testing.T) {
		addrs := []types.Address{{0x01}, {0x02}}
		res, err := src.AccountMultiproof(addrs)
		if err != nil {
			t.Fatalf("AccountMultiproof: %v", err)
		}
		if len(res.Addrs) != 2 {
			t.Errorf("AccountMultiproof.Addrs = %v, want 2 entries", res.Addrs)
		}
	})

	t.Run("AccountMultiproof empty", func(t *testing.T) {
		res, err := src.AccountMultiproof(nil)
		if err != nil {
			t.Fatalf("AccountMultiproof(nil): %v", err)
		}
		if res == nil || len(res.Addrs) != 0 {
			t.Errorf("AccountMultiproof(nil) = %+v, want empty", res)
		}
	})

	t.Run("Code found and not found", func(t *testing.T) {
		c, err := src.Code(hash)
		if err != nil {
			t.Fatalf("Code: %v", err)
		}
		if len(c) != 4 {
			t.Errorf("Code len = %d, want 4", len(c))
		}
		missing := types.Hash{0xff}
		c2, err := src.Code(missing)
		if err != nil {
			t.Fatalf("Code(missing): %v", err)
		}
		if c2 != nil {
			t.Errorf("Code(missing) = %v, want nil", c2)
		}
	})

	t.Run("FullHeader", func(t *testing.T) {
		h, err := src.FullHeader(1)
		if err != nil {
			t.Fatalf("FullHeader: %v", err)
		}
		if h.Number.Uint64() != 1 {
			t.Errorf("FullHeader.Number = %v, want 1", h.Number)
		}
	})

	t.Run("Body", func(t *testing.T) {
		b, err := src.Body(1)
		if err != nil {
			t.Fatalf("Body: %v", err)
		}
		if b == nil {
			t.Error("Body returned nil without error")
		}
	})
}

// TestHTTPSourceGetRetriesOn503 exercises the backoff/retry loop in get() for a
// transient 503, then a final success.
func TestHTTPSourceGetRetriesOn503(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/head", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":42}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	src := NewHTTPSource(srv.URL)
	n, err := src.Head()
	if err != nil {
		t.Fatalf("Head after retry: %v", err)
	}
	if n != 42 {
		t.Errorf("Head() = %d, want 42", n)
	}
	if calls < 2 {
		t.Errorf("expected at least one retry, got %d calls", calls)
	}
}

// TestHTTPSourceGetNotImplemented checks the ErrUnsupported wrapping for a 501
// response (the optional-capability signal).
func TestHTTPSourceGetNotImplemented(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/anchor-heights", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotImplemented)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	src := NewHTTPSource(srv.URL)
	_, err := src.AnchorHeights(0, 10)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, stateless.ErrUnsupported) {
		t.Errorf("expected wrapped ErrUnsupported, got %v", err)
	}
}

// TestHTTPSourceGetHardError checks a non-retryable status surfaces the body text.
func TestHTTPSourceGetHardError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/head", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	src := NewHTTPSource(srv.URL)
	if _, err := src.Head(); err == nil {
		t.Fatal("expected error")
	}
}
