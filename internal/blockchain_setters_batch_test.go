// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers a batch of BlockChain attach/accessor pairs that were entirely
// untested: SetPrefetchPredictor (and its SlotRecorder wiring), SetFreezer,
// SetExExManager/ExExManager, SetJMTStoreRefresh, SetBMTCommitment,
// SetVerkleCommitment/VerkleCommitment/VerkleEnabled, SetMPTRootComputer,
// SetLtHashCommitment/LtHashCommitment, RootComputer, SetZKProving/
// IsZKProvingEnabled, Freezer/AncientReader, SetCommitteePool/CommitteePool,
// AddPeer, LatestBlockCh, and WaitBlockApplied's fast-true path.

package internal

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
)

// fakeFreezer is a minimal freezer.FreezerAPI implementation so SetFreezer
// can be exercised without a real on-disk freezer.
type fakeFreezer struct{ frozen uint64 }

func (f *fakeFreezer) Frozen() uint64                           { return f.frozen }
func (f *fakeFreezer) HasAncient(uint64) bool                   { return false }
func (f *fakeFreezer) Ancient(string, uint64) ([]byte, error)   { return nil, nil }
func (f *fakeFreezer) Freeze(uint64, *freezer.FreezeData) error { return nil }
func (f *fakeFreezer) TruncateHead(uint64) error                { return nil }
func (f *fakeFreezer) StartFreeze(context.Context, func() uint64, freezer.FreezeFunc, func(uint64, uint64) error) {
}
func (f *fakeFreezer) Sync() error  { return nil }
func (f *fakeFreezer) Close() error { return nil }

func TestSetPrefetchPredictorWiresSlotRecorder(t *testing.T) {
	sp := NewStateProcessor(nil, nil, nil)
	bc := &BlockChain{process: sp}
	if bc.prefetchPredictor != nil {
		t.Fatalf("new BlockChain has a non-nil prefetchPredictor")
	}
	pred := NewPrefetchPredictor(8)
	bc.SetPrefetchPredictor(pred)
	if bc.prefetchPredictor != pred {
		t.Fatalf("SetPrefetchPredictor() did not store the predictor")
	}
	if sp.slotRecorder != pred {
		t.Fatalf("SetPrefetchPredictor() did not wire the StateProcessor's slot recorder")
	}

	// nil is accepted and must not wire a recorder.
	bc.SetPrefetchPredictor(nil)
	if bc.prefetchPredictor != nil {
		t.Fatalf("SetPrefetchPredictor(nil) left a non-nil predictor")
	}
}

func TestSetFreezerAttachesAPI(t *testing.T) {
	bc := &BlockChain{}
	f := &fakeFreezer{frozen: 7}
	bc.SetFreezer(f)
	if bc.Freezer() != f {
		t.Fatalf("Freezer() = %v, want the attached fake", bc.Freezer())
	}
	// Not a *freezer.Freezer concrete type, so no AncientReader is created.
	if bc.AncientReader() != nil {
		t.Fatalf("AncientReader() = %v, want nil for a non-concrete freezer", bc.AncientReader())
	}
}

func TestSetExExManagerAndAccessor(t *testing.T) {
	bc := &BlockChain{}
	if bc.ExExManager() != nil {
		t.Fatalf("new BlockChain has a non-nil ExExManager")
	}
	// A nil *exex.Manager round-trips through the setter/getter unchanged.
	bc.SetExExManager(nil)
	if bc.ExExManager() != nil {
		t.Fatalf("ExExManager() after SetExExManager(nil) = non-nil")
	}
}

func TestCommitmentAttachSettersAndAccessors(t *testing.T) {
	bc := &BlockChain{}

	bc.SetJMTStoreRefresh(func() {})
	if bc.jmtStoreRefresh == nil {
		t.Fatalf("SetJMTStoreRefresh() did not store the callback")
	}

	if bc.IsJMTEnabled() {
		t.Fatalf("IsJMTEnabled() = true before any JMT commitment attached")
	}
	if bc.JMTCommitment() != nil {
		t.Fatalf("JMTCommitment() = non-nil before any commitment attached")
	}

	bc.SetBMTCommitment(nil)
	if !bc.bmtEnabled {
		t.Fatalf("SetBMTCommitment() did not enable BMT")
	}

	if bc.VerkleEnabled() {
		t.Fatalf("VerkleEnabled() = true before SetVerkleCommitment")
	}
	bc.SetVerkleCommitment(nil)
	if !bc.VerkleEnabled() {
		t.Fatalf("VerkleEnabled() = false after SetVerkleCommitment")
	}
	if bc.VerkleCommitment() != nil {
		t.Fatalf("VerkleCommitment() = non-nil for a nil commitment")
	}

	bc.SetMPTRootComputer(nil)
	if !bc.mptEnabled {
		t.Fatalf("SetMPTRootComputer() did not enable MPT")
	}

	if bc.LtHashCommitment() != nil {
		t.Fatalf("LtHashCommitment() = non-nil before SetLtHashCommitment")
	}
	bc.SetLtHashCommitment(nil)
	if !bc.ltHashEnabled {
		t.Fatalf("SetLtHashCommitment() did not enable LtHash")
	}

	if bc.RootComputer() != nil {
		t.Fatalf("RootComputer() = non-nil before SetRootComputer")
	}
	bc.SetRootComputer(nil)
	if bc.RootComputer() != nil {
		t.Fatalf("RootComputer() after SetRootComputer(nil) = non-nil")
	}
	_ = state.RootComputer(nil) // confirms the import is the same interface type
}

func TestSetZKProvingAndIsEnabled(t *testing.T) {
	bc := &BlockChain{}
	if bc.IsZKProvingEnabled() {
		t.Fatalf("IsZKProvingEnabled() = true before SetZKProving")
	}
	bc.SetZKProving(true)
	if !bc.IsZKProvingEnabled() {
		t.Fatalf("IsZKProvingEnabled() = false after SetZKProving(true)")
	}

	bc2 := &BlockChain{}
	bc2.SetZKProving(false, true) // requireProof implies zkProving
	if !bc2.IsZKProvingEnabled() {
		t.Fatalf("SetZKProving(false, true) did not force zkProving on")
	}
	if !bc2.zkRequireProof {
		t.Fatalf("SetZKProving(false, true) did not set zkRequireProof")
	}
}

func TestSetCommitteePoolAndAccessor(t *testing.T) {
	bc := &BlockChain{}
	if bc.CommitteePool() != nil {
		t.Fatalf("CommitteePool() = non-nil before SetCommitteePool")
	}
	bc.SetCommitteePool(nil)
	if bc.CommitteePool() != nil {
		t.Fatalf("CommitteePool() after SetCommitteePool(nil) = non-nil")
	}
}

func TestAddPeerRejectsMismatchedGenesisAndDuplicatePeer(t *testing.T) {
	genesis := testConcreteBlock(&block.Header{Number: uint256.NewInt(0), Difficulty: uint256.NewInt(1)}, &block.Body{})
	bc := &BlockChain{
		genesisBlock: genesis,
		peers:        make(map[peer.ID]bool),
	}
	bc.currentBlock.Store(genesis)

	if err := bc.AddPeer("not-the-genesis-hash", 1, peer.ID("p1")); err == nil {
		t.Fatalf("AddPeer() with mismatched genesis hash = nil error, want error")
	}

	if err := bc.AddPeer(genesis.Hash().String(), 1, peer.ID("p1")); err != nil {
		t.Fatalf("AddPeer() = %v, want nil", err)
	}
	if err := bc.AddPeer(genesis.Hash().String(), 1, peer.ID("p1")); err == nil {
		t.Fatalf("AddPeer() duplicate peer = nil error, want error")
	}
}

func TestLatestBlockChDeliversAndClosesOnQuit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan block.IBlock, 1)
	bc := &BlockChain{ctx: ctx, latestBlockCh: ch}

	blk := testConcreteBlock(&block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(1)}, &block.Body{})
	ch <- blk
	got, err := bc.LatestBlockCh()
	if err != nil || got.Hash() != blk.Hash() {
		t.Fatalf("LatestBlockCh() = (%v, %v), want (blk, nil)", got, err)
	}

	cancel()
	if _, err := bc.LatestBlockCh(); err == nil {
		t.Fatalf("LatestBlockCh() after ctx cancel = nil error, want error")
	}
}

func TestWaitBlockAppliedReturnsImmediatelyWhenQMDBDisabled(t *testing.T) {
	bc := &BlockChain{ctx: context.Background()}
	start := time.Now()
	if !bc.WaitBlockApplied(types.HexToHash("0x1"), 50*time.Millisecond) {
		t.Fatalf("WaitBlockApplied() with QMDB disabled = false, want true (nothing to contradict)")
	}
	if time.Since(start) > 20*time.Millisecond {
		t.Fatalf("WaitBlockApplied() took %s, want an immediate true (no polling needed)", time.Since(start))
	}
}
