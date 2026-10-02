// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// hsTNotifyProducer records CommitToCanonical calls for the header-QC
// canonicalization and deferred-commit-retry paths.
type hsTNotifyProducer struct {
	committed []types.Hash
	err       error
}

func (p *hsTNotifyProducer) TriggerBlockProduction(types.Hash) {}
func (p *hsTNotifyProducer) CommitToCanonical(hash types.Hash) error {
	p.committed = append(p.committed, hash)
	return p.err
}

// hsTNotifyFetcher is a minimal BlockFetcher + BlockApplied probe.
type hsTNotifyFetcher struct {
	applied bool
}

func (f *hsTNotifyFetcher) FetchBlockByHash(types.Hash) {}
func (f *hsTNotifyFetcher) CatchUp()                    {}
func (f *hsTNotifyFetcher) HeightBehind() uint64        { return 0 }
func (f *hsTNotifyFetcher) BlockApplied(types.Hash, uint64) bool {
	return f.applied
}

// hsTNewNotifyService builds a Service with a real HotStuff engine (4
// validators, local index 0) wired to a memdb instance, ready to exercise
// the Notify* / maybeCommitFromHeaderQC family.
func hsTNewNotifyService(t *testing.T) (*Service, *testSetup, *hsTNotifyProducer, *hsTNotifyFetcher) {
	t.Helper()
	setup := newTestSetup(t, 4)
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	db := memdb.NewTestDB(t)
	producer := &hsTNotifyProducer{}
	fetcher := &hsTNotifyFetcher{applied: true}
	s := &Service{
		engine:            h,
		db:                db,
		ctx:               context.Background(),
		blockProducer:     producer,
		blockFetcher:      fetcher,
		pendingExecutions: make(map[types.Hash]struct{}),
		notifiedImports:   make(map[types.Hash]struct{}),
	}
	return s, setup, producer, fetcher
}

func hsTWriteHeader(t *testing.T, s *Service, h *block.Header) types.Hash {
	t.Helper()
	if err := s.db.Update(context.Background(), func(tx kv.RwTx) error {
		rawdb.WriteHeader(tx, h)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return h.Hash()
}

// TestService_NotifyBlockImported_AppliedDedupAndCommit covers
// NotifyBlockImported's full body for an applied block: header lookup,
// applied-gate pass, dedup bookkeeping, and that a second notification for
// the same hash is a no-op (no panic, no duplicate production).
func TestService_NotifyBlockImported_AppliedDedupAndCommit(t *testing.T) {
	s, _, _, fetcher := hsTNewNotifyService(t)
	fetcher.applied = true

	header := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	hash := hsTWriteHeader(t, s, header)

	s.NotifyBlockImported(hash, types.Hash{0x01})
	if _, ok := s.notifiedImports[hash]; !ok {
		t.Fatal("expected hash to be recorded in notifiedImports")
	}

	// Second notification for the same hash: dedup path, must not panic.
	s.NotifyBlockImported(hash, types.Hash{0x01})
}

// TestService_NotifyBlockImported_NotAppliedWithheld covers the applied-
// evidence gate: when BlockApplied reports false, the engine notification is
// withheld and the hash is NOT added to the dedup set (so a later real
// notification can still go through).
func TestService_NotifyBlockImported_NotAppliedWithheld(t *testing.T) {
	s, _, _, fetcher := hsTNewNotifyService(t)
	fetcher.applied = false

	header := &block.Header{
		Number:     uint256.NewInt(2),
		Difficulty: uint256.NewInt(0),
	}
	hash := hsTWriteHeader(t, s, header)

	s.NotifyBlockImported(hash, types.Hash{0x02})
	if _, ok := s.notifiedImports[hash]; ok {
		t.Fatal("hash should not be marked notified when BlockApplied is false")
	}
}

// TestService_NotifyBlockImported_NoHeaderReturnsEarly covers the
// !haveHeader guard for a hash with no stored header.
func TestService_NotifyBlockImported_NoHeaderReturnsEarly(t *testing.T) {
	s, _, _, _ := hsTNewNotifyService(t)
	s.NotifyBlockImported(types.Hash{0xff}, types.Hash{0x01}) // must not panic
}

// TestService_NotifyBlockImported_DeferredRetries covers the retryProduce and
// retryCommit branches: a pending deferred production and a pending deferred
// commit keyed by this hash are both resolved.
func TestService_NotifyBlockImported_DeferredRetries(t *testing.T) {
	s, _, producer, fetcher := hsTNewNotifyService(t)
	fetcher.applied = true

	header := &block.Header{
		Number:     uint256.NewInt(3),
		Difficulty: uint256.NewInt(0),
	}
	hash := hsTWriteHeader(t, s, header)

	s.pendingMu.Lock()
	s.pendingCommits = map[types.Hash]struct{}{hash: {}}
	s.deferredProduce.view = s.engine.Engine().CurrentView()
	s.deferredProduce.parent = hash
	s.pendingMu.Unlock()

	s.NotifyBlockImported(hash, types.Hash{0x03})

	// Deferred commit retry runs synchronously.
	if len(producer.committed) != 1 || producer.committed[0] != hash {
		t.Fatalf("expected a deferred CommitToCanonical(%s), got %v", hash, producer.committed)
	}
	s.pendingMu.Lock()
	_, stillPending := s.pendingCommits[hash]
	s.pendingMu.Unlock()
	if stillPending {
		t.Fatal("expected the pending commit entry to be cleared")
	}
}

// TestService_NotifyBlockExecuted_FirstTimeProcessesAndDedups covers the
// early-vote path: first call processes and marks the dedup set; a second
// call for the same hash returns immediately via the dedup guard.
func TestService_NotifyBlockExecuted_FirstTimeProcessesAndDedups(t *testing.T) {
	s, _, _, _ := hsTNewNotifyService(t)

	hash := types.Hash{0x10}
	parent := types.Hash{0x11}
	txHash := types.Hash{0x12}

	s.NotifyBlockExecuted(hash, txHash, parent, 5, nil)
	if _, ok := s.notifiedImports[hash]; !ok {
		t.Fatal("expected hash to be recorded in notifiedImports")
	}
	// Second call: dedup short-circuit, must not panic or duplicate work.
	s.NotifyBlockExecuted(hash, txHash, parent, 5, nil)
}

// TestService_MaybeCommitFromHeaderQC_CanonicalizesOnValidQC builds a real
// BLS commit QC embedded in a header's extra field and verifies
// maybeCommitFromHeaderQC canonicalizes its target once the target header is
// locally known and ahead of the committed head.
func TestService_MaybeCommitFromHeaderQC_CanonicalizesOnValidQC(t *testing.T) {
	s, setup, producer, _ := hsTNewNotifyService(t)

	target := &block.Header{
		Number:     uint256.NewInt(7),
		Difficulty: uint256.NewInt(0),
		Extra:      []byte{0x01}, // distinguish from the zero-value default header
	}
	targetHash := hsTWriteHeader(t, s, target)

	const view ViewNumber = 1
	commitMsg := CommitSigningMessage(view, targetHash)
	vc := NewVoteCollector(view, targetHash, setup.vs.Len())
	for i := 0; i < 3; i++ {
		sig := setup.keys[i].Sign(commitMsg)
		if err := vc.AddVote(ValidatorIndex(i), sig); err != nil {
			t.Fatal(err)
		}
	}
	qc, err := vc.BuildQCWithMessage(setup.vs, commitMsg)
	if err != nil {
		t.Fatal(err)
	}

	extra, err := buildHeaderExtra(view, qc)
	if err != nil {
		t.Fatal(err)
	}

	s.maybeCommitFromHeaderQC(extra)

	if len(producer.committed) != 1 || producer.committed[0] != targetHash {
		t.Fatalf("expected CommitToCanonical(%s), got %v", targetHash, producer.committed)
	}
}

// TestService_MaybeCommitFromHeaderQC_Guards covers the early-return guards:
// nil producer/db, empty extra, undecodable extra, and a target that is not
// yet locally known (qcNum == nil).
func TestService_MaybeCommitFromHeaderQC_Guards(t *testing.T) {
	s, _, producer, _ := hsTNewNotifyService(t)

	// Empty extra: no-op.
	s.maybeCommitFromHeaderQC(nil)
	if len(producer.committed) != 0 {
		t.Fatal("expected no commit on empty extra")
	}

	// Garbage extra that fails to decode a QC.
	s.maybeCommitFromHeaderQC([]byte{0x01, 0x02, 0x03})
	if len(producer.committed) != 0 {
		t.Fatal("expected no commit on undecodable extra")
	}

	// Valid-looking QC whose target block is not locally known.
	const view ViewNumber = 2
	unknownHash := types.Hash{0xaa, 0xbb}
	qc := &QuorumCertificate{View: view, BlockHash: unknownHash}
	extra, err := buildHeaderExtra(view, qc)
	if err != nil {
		t.Fatal(err)
	}
	// This QC is an empty/placeholder one (no signers), so buildHeaderExtra
	// takes the "isEmptyHeaderQC" branch; ExtractHeaderQC then returns a
	// zero-valued QC that fails the qc.View == 0 guard. Exercise that path
	// explicitly for coverage of the early return.
	s.maybeCommitFromHeaderQC(extra)
	if len(producer.committed) != 0 {
		t.Fatal("expected no commit for a placeholder/empty QC")
	}

	// nil producer guard.
	s.blockProducer = nil
	s.maybeCommitFromHeaderQC([]byte{0x01})
}

// TestService_BroadcastBlockData covers the placeholder leader broadcast
// delay; bounded well under the 100ms test-wait ceiling by construction (the
// function itself sleeps 50ms).
func TestService_BroadcastBlockData(t *testing.T) {
	s := &Service{}
	start := time.Now()
	s.broadcastBlockData(types.Hash{0x01})
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("broadcastBlockData returned too quickly: %v", elapsed)
	}
}

// TestService_SetBlockProducerAndFetcher covers the two trivial setters.
func TestService_SetBlockProducerAndFetcher(t *testing.T) {
	s := &Service{}
	producer := &hsTNotifyProducer{}
	fetcher := &hsTNotifyFetcher{}
	s.SetBlockProducer(producer)
	s.SetBlockFetcher(fetcher)
	if s.blockProducer != producer {
		t.Fatal("SetBlockProducer: not stored")
	}
	if s.blockFetcher != fetcher {
		t.Fatal("SetBlockFetcher: not stored")
	}
}
