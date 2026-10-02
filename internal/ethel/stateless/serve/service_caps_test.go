package serve

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// errBE wraps fullBE but can be told to fail AccountMultiproof/AccountProof, and
// supports Code lookups for GetCode/GetCodeZ branch coverage.
type errBE struct {
	fullBE
	multiproofErr error
	acctProofErr  error
}

func (b *errBE) AccountMultiproof(addrs []types.Address) ([]byte, error) {
	if b.multiproofErr != nil {
		return nil, b.multiproofErr
	}
	return b.fullBE.AccountMultiproof(addrs)
}

func (b *errBE) AccountProof(addr types.Address, slots []types.Hash) ([]byte, error) {
	if b.acctProofErr != nil {
		return nil, b.acctProofErr
	}
	return b.fullBE.AccountProof(addr, slots)
}

func newErrBE() *errBE {
	hdrs := emptyStateChain(5)
	return &errBE{fullBE: fullBE{chainBE: chainBE{headers: hdrs, anchorEvery: 2}, codeByHash: map[types.Hash][]byte{}}}
}

// TestGetAccountMultiproof_CapAndError covers the cap-exceeded branch and the
// backend-error passthrough branch.
func TestGetAccountMultiproof_CapAndError(t *testing.T) {
	be := newErrBE()
	caps := DefaultCaps()
	caps.MaxCodeHashes = 1
	svc := NewService(be, caps, nil)

	_, err := svc.GetAccountMultiproof("1.2.3.4", []types.Address{{1}, {2}})
	if !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("expected ErrCapExceeded, got %v", err)
	}

	be.multiproofErr = errors.New("backend boom")
	svc2 := NewService(be, DefaultCaps(), nil)
	if _, err := svc2.GetAccountMultiproof("1.2.3.4", []types.Address{{1}}); err == nil {
		t.Fatal("expected backend error to propagate")
	}
}

// TestGetAccountProof_CapAndError mirrors the multiproof test for AccountProof.
func TestGetAccountProof_CapAndError(t *testing.T) {
	be := newErrBE()
	caps := DefaultCaps()
	caps.MaxCodeHashes = 1
	svc := NewService(be, caps, nil)

	_, err := svc.GetAccountProof("1.2.3.4", types.Address{1}, []types.Hash{{1}, {2}})
	if !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("expected ErrCapExceeded, got %v", err)
	}

	be.acctProofErr = errors.New("backend boom")
	svc2 := NewService(be, DefaultCaps(), nil)
	if _, err := svc2.GetAccountProof("1.2.3.4", types.Address{1}, nil); err == nil {
		t.Fatal("expected backend error to propagate")
	}
}

// TestGetFullHeader_Error covers the backend-error branch (gap/absent block).
func TestGetFullHeader_Error(t *testing.T) {
	be := newErrBE()
	svc := NewService(be, DefaultCaps(), nil)
	if _, err := svc.GetFullHeader("1.2.3.4", 999); err == nil {
		t.Fatal("expected error for out-of-range block")
	}
}

// TestGetCode covers both the found and not-found/error branches of GetCode
// (distinct from GetCodeZ, which compresses).
func TestGetCode(t *testing.T) {
	be := newErrBE()
	hash := types.Hash{0xaa}
	be.codeByHash[hash] = []byte{0x60, 0x00}
	svc := NewService(be, DefaultCaps(), nil)

	out, err := svc.GetCode("1.2.3.4", []types.Hash{hash, {0xbb}})
	if err != nil {
		t.Fatalf("GetCode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("GetCode result = %v, want 1 entry", out)
	}
	if c, ok := out[hash]; !ok || len(c) != 2 {
		t.Errorf("GetCode[hash] = %v", c)
	}
}

// TestGetCode_CapExceeded covers the MaxCodeHashes cap branch.
func TestGetCode_CapExceeded(t *testing.T) {
	be := newErrBE()
	caps := DefaultCaps()
	caps.MaxCodeHashes = 1
	svc := NewService(be, caps, nil)
	if _, err := svc.GetCode("1.2.3.4", []types.Hash{{1}, {2}}); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("expected ErrCapExceeded, got %v", err)
	}
}

// TestGetCode_RateLimited covers the bandwidth-limiter rejection branch.
func TestGetCode_RateLimited(t *testing.T) {
	be := newErrBE()
	hash := types.Hash{0xaa}
	be.codeByHash[hash] = []byte{0x60, 0x00, 0x00, 0x00}
	bw := NewByteLimiter(1, 1, 16) // tiny budget so Allow() rejects immediately
	svc := NewService(be, DefaultCaps(), bw)
	if _, err := svc.GetCode("1.2.3.4", []types.Hash{hash}); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}
}

// TestGetAnchorHeights_Caps covers the to<from and span-exceeded branches, plus
// ErrNotSupported when the backend lacks the optional interface.
func TestGetAnchorHeights_Caps(t *testing.T) {
	be := newErrBE()
	svc := NewService(be, DefaultCaps(), nil)
	if _, err := svc.GetAnchorHeights("ip", 10, 5); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("to<from: expected ErrCapExceeded, got %v", err)
	}

	caps := DefaultCaps()
	caps.MaxAnchorSpan = 1
	svc2 := NewService(be, caps, nil)
	if _, err := svc2.GetAnchorHeights("ip", 0, 10); !errors.Is(err, ErrCapExceeded) {
		t.Fatalf("span exceeded: expected ErrCapExceeded, got %v", err)
	}
}

// noAnchorListerBE implements Backend but not anchorListerBackend.
type noAnchorListerBE struct{ chainBE }

func TestGetAnchorHeights_NotSupported(t *testing.T) {
	be := &noAnchorListerBE{chainBE{headers: emptyStateChain(3), anchorEvery: 1}}
	svc := NewService(be, DefaultCaps(), nil)
	if _, err := svc.GetAnchorHeights("ip", 0, 1); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("expected ErrNotSupported, got %v", err)
	}
}
