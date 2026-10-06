package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestTwoPhaseCommitChecksImportedParent(t *testing.T) {
	for _, beforeImport := range []bool{false, true} {
		for _, matchingParent := range []bool{false, true} {
			name := map[bool]string{false: "qc-after-import", true: "qc-before-import"}[beforeImport] +
				map[bool]string{false: "/wrong-parent", true: "/matching-parent"}[matchingParent]
			t.Run(name, func(t *testing.T) {
				setup := newTestSetup(t, 4)
				e, outputs := newTestEngine(t, setup, 0)
				e.twoPhaseVote = true
				e.roundState.AdvanceView(2)
				hash, justify := types.Hash{0xAA}, types.Hash{0x11}
				leader := LeaderForView(2, setup.vs)
				proposal := &Proposal{View: 2, BlockHash: hash, Proposer: leader,
					JustifyQC: *buildPaperTestQC(t, setup, 1, justify),
					Signature: setup.keys[leader].Sign(SigningMessage(2, hash)).Marshal()}
				if err := e.processProposal(proposal, msgTiming{}); err != nil {
					t.Fatal(err)
				}
				if e.roundState.HasVotedInView(2) {
					t.Fatal("prepare vote escaped before the parent was known")
				}
				drainOutputs(outputs)
				pqc := &PrepareQCMsg{View: 2, BlockHash: hash, QC: *buildPaperTestQC(t, setup, 2, hash)}
				if beforeImport {
					if err := e.processPrepareQC(pqc, msgTiming{}); err != nil {
						t.Fatal(err)
					}
					if e.roundState.HasCommitVotedInView(2) {
						t.Fatal("commit vote escaped before import")
					}
				}
				parent := types.Hash{0x99}
				if matchingParent {
					parent = justify
				}
				if err := e.onBlockImported(hash, types.Hash{}, parent); err != nil {
					t.Fatal(err)
				}
				if !beforeImport {
					if err := e.processPrepareQC(pqc, msgTiming{}); err != nil {
						t.Fatal(err)
					}
				}
				if e.roundState.HasCommitVotedInView(2) != matchingParent {
					t.Fatalf("commit vote = %v, matching parent = %v", e.roundState.HasCommitVotedInView(2), matchingParent)
				}
				if !matchingParent && len(drainOutputs(outputs)) != 0 {
					t.Fatal("wrong-parent block emitted a commit vote")
				}
			})
		}
	}
}
