package serve

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHandler_OnlyGET verifies non-GET/HEAD methods are rejected with 405.
func TestHandler_OnlyGET(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/head", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

// TestHandler_NotFoundDefault covers writeErr's default branch: a plain
// backend error (gap/absent) surfaces as 404 without leaking err.Error().
func TestHandler_NotFoundDefault(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/header?n=999")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestHandler_AccountProofNotSupported covers writeErr's ErrNotSupported branch
// via the real /account-proof route (chainBE.AccountProof returns
// ErrNotSupported).
func TestHandler_AccountProofNotSupported(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/account-proof?addr=0x0000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
}

// TestHandler_BadAddrAndSlot covers the /account-proof bad-input branches.
func TestHandler_BadAddrAndSlot(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/account-proof?addr=not-hex")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad addr: status = %d, want 400", resp.StatusCode)
	}

	resp2, err := http.Get(srv.URL + "/account-proof?addr=0x0000000000000000000000000000000000000001&slots=not-hex")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Errorf("bad slot: status = %d, want 400", resp2.StatusCode)
	}
}

// TestHandler_BadN covers the qn() parse-error branch shared by several routes.
func TestHandler_BadN(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	for _, path := range []string{"/header?n=x", "/anchor?n=x", "/witness?n=x", "/block?n=x", "/full-header?n=x"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", path, resp.StatusCode)
		}
	}
}

// TestHandler_BadCodeHash covers /code's malformed-hash branch.
func TestHandler_BadCodeHash(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/code?h=not-hex")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHandler_BadAccountMultiproofAddr covers /account-multiproof's
// malformed-address branch.
func TestHandler_BadAccountMultiproofAddr(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/account-multiproof?addrs=not-hex")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHandler_Health covers the /health route's success path.
func TestHandler_Health(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestHandler_AnchorHeightsBadParams covers the /anchor-heights parse-error
// branch.
func TestHandler_AnchorHeightsBadParams(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/anchor-heights?from=x&to=y")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestHandler_HeadersBadParams covers /headers' parse-error branch.
func TestHandler_HeadersBadParams(t *testing.T) {
	be := &chainBE{headers: emptyStateChain(2), anchorEvery: 1000}
	svc := NewService(be, DefaultCaps(), nil)
	srv := httptest.NewServer(Handler(svc, nil, nil))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/headers?from=x&count=y")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}
