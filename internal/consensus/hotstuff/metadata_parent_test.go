package hotstuff

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestMetadataLeaderRequiresRemoteAppliedParentWithoutQMDBMarker(t *testing.T) {
	service, fetcher, hash := newExecutionTestService(t, false)
	service.certificateSink = certificateSinkFunc(func(context.Context, *QuorumCertificate) error { return nil })
	if service.ensureParentApplied(hash) {
		t.Fatal("metadata store without QMDB marker bypassed remote execution gate")
	}
	if fetcher.lastHash != hash || fetcher.lastNumber != 42 {
		t.Fatal("remote parent evidence was not checked")
	}
	fetcher.applied = true
	if !service.ensureParentApplied(hash) {
		t.Fatal("bound remote execution evidence did not unblock production")
	}
	service.certificateSink = nil
	fetcher.applied = false
	if !service.ensureParentApplied(hash) {
		t.Fatal("default native missing-marker diagnostic behavior changed")
	}
}

func TestPerformanceProposalRejectsObsoleteViewForSameLeader(t *testing.T) {
	engine, output := singleValidatorEngine(t)
	view := engine.CurrentView()
	hash, parent := types.Hash{0xab}, types.Hash{0x11}
	engine.rememberImported(hash, parent)
	if err := engine.performanceBlockReady(view+1, hash, types.Hash{}, parent); err == nil {
		t.Fatal("same leader accepted candidate WAL bound to another view")
	}
	if broadcastsProposal(drainOutputs(output)) {
		t.Fatal("obsolete-view candidate emitted a proposal")
	}
	if err := engine.performanceBlockReady(view, hash, types.Hash{}, types.Hash{0x22}); err == nil {
		t.Fatal("candidate with wrong imported parent accepted")
	}
	if err := engine.performanceBlockReady(view, hash, types.Hash{}, parent); err != nil {
		t.Fatal(err)
	}
	if !broadcastsProposal(drainOutputs(output)) {
		t.Fatal("current-view candidate did not emit real proposal")
	}
	// This single-validator engine commits and advances immediately, while
	// remaining leader. A candidate WAL from the old view still cannot leak
	// into that next view.
	if err := engine.performanceBlockReady(view, hash, types.Hash{}, parent); err == nil {
		t.Fatal("candidate from completed view was proposed in the next view")
	}
}
