package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

// singleValidatorEngine returns an engine that is the sole validator, so it is
// the leader of every view and a quorum is one.
func singleValidatorEngine(t *testing.T) (*ConsensusEngine, chan EngineOutput) {
	t.Helper()
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	vs := NewValidatorSet([]ValidatorInfo{{Address: types.Address{1}, PublicKey: sk.PublicKey()}}, 0)
	out := make(chan EngineOutput, 256)
	return NewConsensusEngine(0, sk, vs, 1000, 10000, out), out
}

func broadcastsProposal(outputs []EngineOutput) bool {
	for _, o := range outputs {
		if o.Type != OutputBroadcast || o.Message == nil {
			continue
		}
		if o.Message.Type == MsgProposal {
			return true
		}
	}
	return false
}

// A QC arriving during the build moves LockedQC past the parent the block was
// built on. Proposing it anyway pairs a newer JustifyQC with an older parent,
// which every voter's extendsJustify refuses -- the view times out, the next
// leader repeats it, and the chain stops. The leader must drop the block
// instead. A peer hit this live on another client; blockProductionSyncGate
// lets a leader two blocks behind produce here, and a full build takes 1.6-2 s,
// so the window is wide.
func TestSealedBlockDroppedWhenParentNoLongerExtendsLockedQC(t *testing.T) {
	engine, out := singleValidatorEngine(t)

	blockHash := types.Hash{0xAB}
	builtOnParent := types.Hash{0x11}
	certifiedByQCNow := types.Hash{0x22} // a DIFFERENT block: the QC moved

	engine.rememberImported(blockHash, builtOnParent)
	engine.roundState.UpdateLockedQC(&QuorumCertificate{View: 5, BlockHash: certifiedByQCNow})

	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash}); err != nil {
		t.Fatalf("onBlockReady returned an error; it should drop quietly and let the view time out: %v", err)
	}
	if broadcastsProposal(drainOutputs(out)) {
		t.Fatal("proposed a block whose parent is not the block its JustifyQC certifies; " +
			"every voter would refuse it and the view would stall")
	}
}

// The guard must not fire on the happy path, or the leader stops proposing.
func TestSealedBlockProposedWhenParentMatchesLockedQC(t *testing.T) {
	engine, out := singleValidatorEngine(t)

	blockHash := types.Hash{0xAB}
	parent := types.Hash{0x11}

	engine.rememberImported(blockHash, parent)
	engine.roundState.UpdateLockedQC(&QuorumCertificate{View: 5, BlockHash: parent})

	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash}); err != nil {
		t.Fatalf("onBlockReady: %v", err)
	}
	if !broadcastsProposal(drainOutputs(out)) {
		t.Fatal("did not propose although the parent is exactly the block LockedQC certifies")
	}
}

// Fail-open, matching extendsJustify: with no recorded parent the rule has
// nothing to compare and must not block an otherwise honest proposal.
func TestSealedBlockProposedWhenParentUnknown(t *testing.T) {
	engine, out := singleValidatorEngine(t)

	blockHash := types.Hash{0xAB}
	engine.roundState.UpdateLockedQC(&QuorumCertificate{View: 5, BlockHash: types.Hash{0x22}})

	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash}); err != nil {
		t.Fatalf("onBlockReady: %v", err)
	}
	if !broadcastsProposal(drainOutputs(out)) {
		t.Fatal("dropped a block with no recorded parent; the rule must fail open, not closed")
	}
}

// The second shape the same comparison catches, and the one seen live on
// another client: LockedQC never moves, but the builder hands back a block
// built on a different parent than the one production was triggered for. The
// driver must not trust the payload's lineage — here the requested parent is
// the LockedQC block (service.go passes lq.BlockHash), so a block on any other
// parent fails the same check.
func TestSealedBlockDroppedWhenBuilderReturnedAnotherParent(t *testing.T) {
	engine, out := singleValidatorEngine(t)

	requestedParent := types.Hash{0x11} // what the leader asked to build on
	blockHash := types.Hash{0xAB}
	builderUsedParent := types.Hash{0x99} // what the builder actually used

	// LockedQC is stable throughout: this is not the QC-moved race.
	engine.roundState.UpdateLockedQC(&QuorumCertificate{View: 5, BlockHash: requestedParent})
	engine.rememberImported(blockHash, builderUsedParent)

	if err := engine.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash}); err != nil {
		t.Fatalf("onBlockReady: %v", err)
	}
	if broadcastsProposal(drainOutputs(out)) {
		t.Fatal("proposed a block the builder placed on a parent that was never requested; " +
			"voters would refuse it and the view would stall")
	}
}
