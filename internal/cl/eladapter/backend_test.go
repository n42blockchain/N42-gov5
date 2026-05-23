// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Phase 7.1.1.a wiring tests for the eladapter Adapter ↔ Backend seam.
// Verifies that the three Engine API methods (NewPayload,
// ForkChoiceUpdate, CurrentHeader) delegate to the Backend correctly
// and surface its errors / return values unchanged.

//go:build n42el

package eladapter

import (
	"context"
	"errors"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	depcommon "github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/engineapi/engine_types"
	"github.com/n42blockchain/N42/internal/cl/depshim/hexutil"
	deptypes "github.com/n42blockchain/N42/internal/cl/depshim/types"
	"github.com/n42blockchain/N42/internal/cl/phase1/execution_client"
)

// recordingBackend captures every Backend call so tests can assert the
// adapter forwarded the right arguments. Each method also returns an
// operator-controlled value/error so tests can pin the wiring behaviour.
type recordingBackend struct {
	// inputs captured per call
	gotExecBlk    *cltypes.Eth1Block
	gotExecParent *depcommon.Hash
	gotExecVH     []depcommon.Hash
	gotExecReq    []hexutil.Bytes
	execStatus    execution_client.PayloadStatus
	execErr       error

	gotFCUHead, gotFCUSafe, gotFCUFinal depcommon.Hash
	gotFCUAttrs                         *engine_types.PayloadAttributes
	gotFCUVersion                       clparams.StateVersion
	fcuPayloadID                        []byte
	fcuErr                              error

	currentHeaderResult *deptypes.Header
	currentHeaderErr    error

	// Phase 7.1.2 — insertion path
	gotInsertSingle *deptypes.Block
	gotInsertBatch  []*deptypes.Block
	gotInsertWait   bool
	insertErr       error

	// existing methods get safe defaults
	currentNum  uint64
	hasBlock    bool
	isCanonical bool
	ready       bool
	returnErr   error
}

func (b *recordingBackend) CurrentHeadNumber(_ context.Context) (uint64, error) {
	return b.currentNum, b.returnErr
}
func (b *recordingBackend) HasBlock(_ context.Context, _ depcommon.Hash) (bool, error) {
	return b.hasBlock, b.returnErr
}
func (b *recordingBackend) IsCanonicalHash(_ context.Context, _ depcommon.Hash) (bool, error) {
	return b.isCanonical, b.returnErr
}
func (b *recordingBackend) Ready(_ context.Context) (bool, error) {
	return b.ready, b.returnErr
}

func (b *recordingBackend) ExecutePayload(
	_ context.Context,
	blk *cltypes.Eth1Block,
	parentBeaconBlockRoot *depcommon.Hash,
	versionedHashes []depcommon.Hash,
	executionRequests []hexutil.Bytes,
) (execution_client.PayloadStatus, error) {
	b.gotExecBlk = blk
	b.gotExecParent = parentBeaconBlockRoot
	b.gotExecVH = versionedHashes
	b.gotExecReq = executionRequests
	return b.execStatus, b.execErr
}

func (b *recordingBackend) UpdateForkchoice(
	_ context.Context,
	head, safe, finalized depcommon.Hash,
	attrs *engine_types.PayloadAttributes,
	version clparams.StateVersion,
) ([]byte, error) {
	b.gotFCUHead = head
	b.gotFCUSafe = safe
	b.gotFCUFinal = finalized
	b.gotFCUAttrs = attrs
	b.gotFCUVersion = version
	return b.fcuPayloadID, b.fcuErr
}

func (b *recordingBackend) ReadCurrentHeader(_ context.Context) (*deptypes.Header, error) {
	return b.currentHeaderResult, b.currentHeaderErr
}

func (b *recordingBackend) InsertBlock(_ context.Context, blk *deptypes.Block) error {
	b.gotInsertSingle = blk
	return b.insertErr
}

func (b *recordingBackend) InsertBlocks(_ context.Context, blks []*deptypes.Block, wait bool) error {
	b.gotInsertBatch = blks
	b.gotInsertWait = wait
	return b.insertErr
}

func TestAdapter_NewPayload_DelegatesToBackend(t *testing.T) {
	want := execution_client.PayloadStatus(execution_client.PayloadStatusValidated)
	wantErr := errors.New("from backend")
	b := &recordingBackend{execStatus: want, execErr: wantErr}
	a := New(b)

	parent := depcommon.Hash{0x42}
	vh := []depcommon.Hash{{0x01}, {0x02}}
	reqs := []hexutil.Bytes{hexutil.Bytes("req-a")}
	blk := &cltypes.Eth1Block{BlockNumber: 12345}

	gotStatus, gotErr := a.NewPayload(context.Background(), blk, &parent, vh, reqs)

	if gotStatus != want {
		t.Errorf("status = %v, want %v", gotStatus, want)
	}
	if gotErr != wantErr {
		t.Errorf("err = %v, want %v", gotErr, wantErr)
	}
	if b.gotExecBlk != blk {
		t.Errorf("Backend did not see the same Eth1Block pointer")
	}
	if b.gotExecParent != &parent {
		t.Errorf("Backend did not see the same parentBeaconBlockRoot pointer")
	}
	if len(b.gotExecVH) != 2 || b.gotExecVH[0] != vh[0] || b.gotExecVH[1] != vh[1] {
		t.Errorf("versionedHashes mismatch: got %v", b.gotExecVH)
	}
	if len(b.gotExecReq) != 1 || string(b.gotExecReq[0]) != "req-a" {
		t.Errorf("executionRequests mismatch: got %v", b.gotExecReq)
	}
}

func TestAdapter_ForkChoiceUpdate_DelegatesToBackend(t *testing.T) {
	wantID := []byte{0xde, 0xad, 0xbe, 0xef}
	wantErr := errors.New("fcu down")
	b := &recordingBackend{fcuPayloadID: wantID, fcuErr: wantErr}
	a := New(b)

	head := depcommon.Hash{0x10}
	safe := depcommon.Hash{0x20}
	final := depcommon.Hash{0x30}
	attrs := &engine_types.PayloadAttributes{Timestamp: 1234}

	gotID, gotErr := a.ForkChoiceUpdate(context.Background(), head, safe, final, attrs, clparams.DenebVersion)

	if string(gotID) != string(wantID) {
		t.Errorf("payloadID = %x, want %x", gotID, wantID)
	}
	if gotErr != wantErr {
		t.Errorf("err = %v, want %v", gotErr, wantErr)
	}
	if b.gotFCUHead != head || b.gotFCUSafe != safe || b.gotFCUFinal != final {
		t.Errorf("FCU hashes mismatch: head=%x safe=%x final=%x", b.gotFCUHead, b.gotFCUSafe, b.gotFCUFinal)
	}
	if b.gotFCUAttrs != attrs {
		t.Errorf("Backend did not see the same attrs pointer")
	}
	if b.gotFCUVersion != clparams.DenebVersion {
		t.Errorf("version = %v, want DenebVersion", b.gotFCUVersion)
	}
}

func TestAdapter_CurrentHeader_DelegatesToBackend(t *testing.T) {
	wantHeader := &deptypes.Header{
		Number: *big.NewInt(25_101_866),
	}
	b := &recordingBackend{currentHeaderResult: wantHeader}
	a := New(b)

	got, err := a.CurrentHeader(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != wantHeader {
		t.Errorf("header pointer mismatch: got %p, want %p", got, wantHeader)
	}
	if got.Number.Uint64() != 25_101_866 {
		t.Errorf("Number = %d, want 25101866", got.Number.Uint64())
	}
}

func TestAdapter_CurrentHeader_BackendErrorPassedThrough(t *testing.T) {
	wantErr := errors.New("rawdb closed")
	b := &recordingBackend{currentHeaderErr: wantErr}
	a := New(b)

	got, err := a.CurrentHeader(context.Background())
	if err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if got != nil {
		t.Errorf("expected nil header on err; got %v", got)
	}
}

func TestAdapter_InsertBlock_DelegatesToBackend(t *testing.T) {
	wantErr := errors.New("insert failed")
	b := &recordingBackend{insertErr: wantErr}
	a := New(b)

	blk := &deptypes.Block{Transactions: [][]byte{{0xaa}}}
	err := a.InsertBlock(context.Background(), blk)
	if err != wantErr {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
	if b.gotInsertSingle != blk {
		t.Errorf("Backend did not see same Block pointer")
	}
}

func TestAdapter_InsertBlocks_DelegatesToBackend(t *testing.T) {
	b := &recordingBackend{}
	a := New(b)

	blks := []*deptypes.Block{
		{Transactions: [][]byte{{0x01}}},
		{Transactions: [][]byte{{0x02}}},
	}
	if err := a.InsertBlocks(context.Background(), blks, true); err != nil {
		t.Errorf("err = %v", err)
	}
	if len(b.gotInsertBatch) != 2 {
		t.Errorf("got %d blocks, want 2", len(b.gotInsertBatch))
	}
	if !b.gotInsertWait {
		t.Errorf("wait flag not forwarded")
	}
}

func TestAdapter_SupportInsertion_StaysFalseUntilValidated(t *testing.T) {
	a := New(&recordingBackend{})
	if a.SupportInsertion() {
		t.Errorf("SupportInsertion must stay false until 7.1.2 is mainnet-validated; see plan doc")
	}
}

func TestAdapter_RetainsAlreadyWiredMethods(t *testing.T) {
	// Sanity: the Phase 7.1.1 extension must not regress the
	// pre-existing IsCanonicalHash / HasBlock / Ready wiring.
	b := &recordingBackend{
		isCanonical: true,
		hasBlock:    true,
		ready:       true,
	}
	a := New(b)
	ctx := context.Background()
	if ok, _ := a.IsCanonicalHash(ctx, depcommon.Hash{}); !ok {
		t.Errorf("IsCanonicalHash regression")
	}
	if ok, _ := a.HasBlock(ctx, depcommon.Hash{}); !ok {
		t.Errorf("HasBlock regression")
	}
	if ok, _ := a.Ready(ctx); !ok {
		t.Errorf("Ready regression")
	}
}
