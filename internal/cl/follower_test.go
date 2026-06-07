//go:build n42el

// Copyright 2026 The N42 Authors
// This file is part of the N42 library.

package cl

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	common "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/engineapi/engine_types"
	deptypes "github.com/n42blockchain/N42/internal/cl/depshim/types"
)

type fcuCall struct {
	finalized, safe, head common.Hash
	version               clparams.StateVersion
}

type fakeEngine struct {
	hdr    *deptypes.Header
	hdrErr error
	calls  []fcuCall
}

func (f *fakeEngine) CurrentHeader(ctx context.Context) (*deptypes.Header, error) {
	return f.hdr, f.hdrErr
}

func (f *fakeEngine) ForkChoiceUpdate(ctx context.Context, finalized, safe, head common.Hash, _ *engine_types.PayloadAttributes, version clparams.StateVersion) ([]byte, error) {
	f.calls = append(f.calls, fcuCall{finalized, safe, head, version})
	return nil, nil
}

func hdrWithNumber(n uint64) *deptypes.Header {
	h := &deptypes.Header{}
	h.Number = *new(big.Int).SetUint64(n)
	return h
}

// No finalized checkpoint yet → no fork-choice update.
func TestDriveForkChoice_NoFinalizedSkips(t *testing.T) {
	eng := &fakeEngine{hdr: hdrWithNumber(10)}
	_, updated, err := driveForkChoice(context.Background(), eng, finalizedInfo{}, fcuPointers{})
	if err != nil {
		t.Fatal(err)
	}
	if updated || len(eng.calls) != 0 {
		t.Fatalf("expected no FCU when finalized is zero; updated=%v calls=%d", updated, len(eng.calls))
	}
}

// EL behind finalized (catch-up) → head pinned to finalized so the EL syncs toward it.
func TestDriveForkChoice_CatchUpHeadIsFinalized(t *testing.T) {
	fin := finalizedInfo{hash: common.HexToHash("0xf1"), number: 200, version: clparams.DenebVersion}
	eng := &fakeEngine{hdr: hdrWithNumber(100)} // EL behind
	next, updated, err := driveForkChoice(context.Background(), eng, fin, fcuPointers{})
	if err != nil || !updated {
		t.Fatalf("expected an update; updated=%v err=%v", updated, err)
	}
	if len(eng.calls) != 1 {
		t.Fatalf("want 1 FCU, got %d", len(eng.calls))
	}
	c := eng.calls[0]
	if c.finalized != fin.hash || c.safe != fin.hash || c.head != fin.hash {
		t.Errorf("catch-up: want all = finalized %x; got fin=%x safe=%x head=%x", fin.hash, c.finalized, c.safe, c.head)
	}
	if c.version != clparams.DenebVersion {
		t.Errorf("version = %v, want Deneb", c.version)
	}
	if next.head != fin.hash {
		t.Errorf("next.head = %x, want finalized", next.head)
	}
}

// EL at/past finalized → head follows the EL's live tip (12 s liveness).
func TestDriveForkChoice_LiveHeadIsELTip(t *testing.T) {
	fin := finalizedInfo{hash: common.HexToHash("0xf1"), number: 50, version: clparams.ElectraVersion}
	elHdr := hdrWithNumber(100) // EL ahead of finalized
	wantHead := elHdr.Hash()
	eng := &fakeEngine{hdr: elHdr}
	next, updated, err := driveForkChoice(context.Background(), eng, fin, fcuPointers{})
	if err != nil || !updated {
		t.Fatalf("expected an update; updated=%v err=%v", updated, err)
	}
	c := eng.calls[0]
	if c.head != wantHead {
		t.Errorf("live: head = %x, want EL tip %x", c.head, wantHead)
	}
	if c.finalized != fin.hash {
		t.Errorf("finalized = %x, want %x", c.finalized, fin.hash)
	}
	if next.head != wantHead {
		t.Errorf("next.head = %x, want EL tip", next.head)
	}
}

// Re-driving with identical inputs issues no second FCU.
func TestDriveForkChoice_DedupsUnchanged(t *testing.T) {
	fin := finalizedInfo{hash: common.HexToHash("0xf1"), number: 50}
	eng := &fakeEngine{hdr: hdrWithNumber(100)}
	last, updated, err := driveForkChoice(context.Background(), eng, fin, fcuPointers{})
	if err != nil || !updated {
		t.Fatalf("first drive should update; updated=%v err=%v", updated, err)
	}
	_, updated2, err := driveForkChoice(context.Background(), eng, fin, last)
	if err != nil {
		t.Fatal(err)
	}
	if updated2 || len(eng.calls) != 1 {
		t.Fatalf("second identical drive must be a no-op; updated=%v calls=%d", updated2, len(eng.calls))
	}
}

// CurrentHeader error → fall back to head=finalized (still drives the EL).
func TestDriveForkChoice_HeaderErrorFallsBackToFinalized(t *testing.T) {
	fin := finalizedInfo{hash: common.HexToHash("0xf1"), number: 50}
	eng := &fakeEngine{hdrErr: errors.New("EL not ready")}
	_, updated, err := driveForkChoice(context.Background(), eng, fin, fcuPointers{})
	if err != nil || !updated {
		t.Fatalf("expected update with finalized fallback; updated=%v err=%v", updated, err)
	}
	if eng.calls[0].head != fin.hash {
		t.Errorf("head = %x, want finalized fallback %x", eng.calls[0].head, fin.hash)
	}
}
