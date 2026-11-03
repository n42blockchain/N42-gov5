package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// A valid aggregate authenticates its own view/hash, not the unsigned envelope.
func TestPrepareQCRequiresEnvelopeBinding(t *testing.T) {
	for _, twoPhase := range []bool{false, true} {
		for _, mismatch := range []string{"hash", "view", "genesis"} {
			t.Run(mismatch+map[bool]string{false: "/import-gated", true: "/two-phase"}[twoPhase], func(t *testing.T) {
				setup := newTestSetup(t, 4)
				e, outputs := newTestEngine(t, setup, 0)
				e.twoPhaseVote = twoPhase
				hash := types.Hash{0x42}
				qc := buildPaperTestQC(t, setup, 1, hash)
				switch mismatch {
				case "hash":
					qc = buildPaperTestQC(t, setup, 1, types.Hash{0x99})
				case "view":
					qc = buildPaperTestQC(t, setup, 2, hash)
				case "genesis":
					g := GenesisQC()
					qc = &g
				}
				err := e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{
					Type: MsgPrepareQC, Payload: &PrepareQCMsg{View: 1, BlockHash: hash, QC: *qc},
				}})
				if err == nil {
					t.Fatal("accepted a certificate for a different view/block")
				}
				if e.roundState.HasCommitVotedInView(1) || e.LockedQC().View != 0 || e.pendingCommitQC != nil {
					t.Fatal("invalid envelope changed the lock, vote journal, or pending commit")
				}
				if got := drainOutputs(outputs); len(got) != 0 {
					t.Fatalf("invalid envelope emitted outputs: %+v", got)
				}
			})
		}
	}
}

func TestPrepareQCMatchingEnvelopeStillVotes(t *testing.T) {
	setup := newTestSetup(t, 4)
	e, outputs := newTestEngine(t, setup, 0)
	hash := types.Hash{0x42}
	qc := buildPaperTestQC(t, setup, 1, hash)
	if err := e.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{
		Type: MsgPrepareQC, Payload: &PrepareQCMsg{View: 1, BlockHash: hash, QC: *qc},
	}}); err != nil {
		t.Fatal(err)
	}
	if !e.roundState.HasCommitVotedInView(1) || len(drainOutputs(outputs)) != 1 {
		t.Fatal("matching certificate did not produce a commit vote")
	}
}
