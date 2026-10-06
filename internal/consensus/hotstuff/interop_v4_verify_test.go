package hotstuff

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
)

// buildSignedQC signs msg with keys[0:threshold] and marks exactly those
// validators as signers, producing a QC that verifies against vs.
func buildSignedQC(keys []blscommon.SecretKey, view ViewNumber, blockHash types.Hash, msg []byte, threshold, total int) QuorumCertificate {
	sigs := make([]blscommon.Signature, threshold)
	signers := make([]bool, total)
	for i := 0; i < threshold; i++ {
		sigs[i] = keys[i].Sign(msg)
		signers[i] = true
	}
	return QuorumCertificate{
		View:               view,
		BlockHash:          blockHash,
		AggregateSignature: bls.AggregateSignatures(sigs).Marshal(),
		Signers:            signers,
	}
}

func buildSignedTC(keys []blscommon.SecretKey, view ViewNumber, msg []byte, threshold, total int) TimeoutCertificate {
	sigs := make([]blscommon.Signature, threshold)
	signers := make([]bool, total)
	for i := 0; i < threshold; i++ {
		sigs[i] = keys[i].Sign(msg)
		signers[i] = true
	}
	return TimeoutCertificate{
		View:               view,
		AggregateSignature: bls.AggregateSignatures(sigs).Marshal(),
		Signers:            signers,
	}
}

// TestEngine_VerifyQCAndCommitQC covers the plain (non-H2V4) verifyQC /
// verifyCommitQC / verifyQCAnyDomain paths, including the "wrong domain"
// fallback in verifyQCAnyDomain.
func TestEngine_VerifyQCAndCommitQC(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	blockHash := types.Hash{0x42}
	view := ViewNumber(5)

	voteMsg := engine.voteSigningMessage(view, blockHash)
	qc := buildSignedQC(setup.keys, view, blockHash, voteMsg, 3, 4)
	if err := engine.verifyQC(&qc); err != nil {
		t.Fatalf("verifyQC: unexpected error: %v", err)
	}

	commitMsg := engine.commitSigningMessage(view, blockHash)
	commitQC := buildSignedQC(setup.keys, view, blockHash, commitMsg, 3, 4)
	if err := engine.verifyCommitQC(&commitQC); err != nil {
		t.Fatalf("verifyCommitQC: unexpected error: %v", err)
	}

	// verifyQCAnyDomain accepts either a vote-domain or commit-domain QC.
	if err := engine.verifyQCAnyDomain(&qc); err != nil {
		t.Fatalf("verifyQCAnyDomain(vote-domain QC): %v", err)
	}
	if err := engine.verifyQCAnyDomain(&commitQC); err != nil {
		t.Fatalf("verifyQCAnyDomain(commit-domain QC): %v", err)
	}

	// A QC signed under a completely different message (wrong block hash)
	// fails against both domains.
	bad := qc
	bad.BlockHash = types.Hash{0x99}
	if err := engine.verifyQCAnyDomain(&bad); err == nil {
		t.Fatalf("verifyQCAnyDomain: expected failure for mismatched block hash")
	}

	// VerifyCommitQCWithResolvedSet resolves the current validator set from
	// the QC's signer bitmap length and verifies against it.
	if err := engine.VerifyCommitQCWithResolvedSet(&commitQC); err != nil {
		t.Fatalf("VerifyCommitQCWithResolvedSet: %v", err)
	}
}

// TestEngine_VerifyTC covers verifyTC and verifyTCWithSet (explicit set
// variant), including rejection of an under-threshold TC.
func TestEngine_VerifyTC(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	view := ViewNumber(9)
	msg := engine.timeoutSigningMessage(view)
	tc := buildSignedTC(setup.keys, view, msg, 3, 4)
	if err := engine.verifyTC(&tc); err != nil {
		t.Fatalf("verifyTC: unexpected error: %v", err)
	}
	if err := engine.verifyTCWithSet(&tc, setup.vs); err != nil {
		t.Fatalf("verifyTCWithSet: unexpected error: %v", err)
	}

	short := buildSignedTC(setup.keys, view, msg, 1, 4)
	if err := engine.verifyTC(&short); err == nil {
		t.Fatalf("verifyTC: expected failure for below-threshold signer count")
	}
}

// TestEngine_H2V4Enabled covers the enabled/disabled branches and that the
// signing-message helpers switch domains once H2V4 is enabled.
func TestEngine_H2V4Enabled(t *testing.T) {
	setup := newTestSetup(t, 4)
	engine, _ := newTestEngine(t, setup, 0)

	if engine.H2V4Enabled() {
		t.Fatalf("expected H2V4 disabled by default")
	}
	plainVote := engine.voteSigningMessage(1, types.Hash{0x01})

	identity := H2V4ChainIdentity{ChainID: 7, GenesisHash: types.Hash{0x55}}
	engine.EnableH2V4(identity)
	if !engine.H2V4Enabled() {
		t.Fatalf("expected H2V4 enabled after EnableH2V4")
	}
	h2Vote := engine.voteSigningMessage(1, types.Hash{0x01})
	if string(plainVote) == string(h2Vote) {
		t.Fatalf("expected signing message to change under H2V4 domain")
	}

	// proposalSigningMessage / commitSigningMessage / newViewSigningMessage
	// also switch domains; exercise them for coverage of each branch.
	_ = engine.proposalSigningMessage(1, types.Hash{0x02})
	_ = engine.commitSigningMessage(1, types.Hash{0x02})
	_ = engine.newViewSigningMessage(1)
}

// TestVerifyH2V4Decide_ErrorPaths covers the nil-envelope, wrong-type, and
// wrong-payload guards ahead of signature verification.
func TestVerifyH2V4Decide_ErrorPaths(t *testing.T) {
	setup := newTestSetup(t, 4)

	if _, err := VerifyH2V4Decide(nil, setup.vs); err == nil {
		t.Fatalf("expected error for nil envelope")
	}
	if _, err := VerifyH2V4Decide(&H2V4Envelope{}, setup.vs); err == nil {
		t.Fatalf("expected error for nil Message")
	}
	wrongType := &H2V4Envelope{Message: &ConsensusMsg{Type: MsgVote}}
	if _, err := VerifyH2V4Decide(wrongType, setup.vs); err == nil {
		t.Fatalf("expected error for non-Decide message type")
	}
	wrongPayload := &H2V4Envelope{Message: &ConsensusMsg{Type: MsgDecide, Payload: nil}}
	if _, err := VerifyH2V4Decide(wrongPayload, setup.vs); err == nil {
		t.Fatalf("expected error for nil Decide payload")
	}
}
