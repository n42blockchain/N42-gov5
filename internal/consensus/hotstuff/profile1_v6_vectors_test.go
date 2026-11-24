// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Vector V6 of the N42 mixed production standard, profile 1: consensus
// messages (proposal, prepare vote, PrepareQC, commit vote, Decide/CommitQC,
// timeout, TC/NewView) with their H2-v4 preimages, signatures and wire bytes,
// and gov5's verdicts for the vote gates and certificate rules of slice 3
// (A13, A14, E6, E10, E11, E13, AM5, AM19, AM23), including a TC landing on
// an epoch boundary (SYNC §10, T = 1) and certificate sizes at n = 4, 100
// and 512. The validators and the chain identity are vector V1's.
//
// The JSON is pinned under internal/testdata/profile1/vectors/V6;
// N42_UPDATE_PROFILE1_FIXTURE=1 rewrites it and N42_PROFILE1_V6_OUT=<dir>
// writes a copy elsewhere.

package hotstuff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

const (
	v6Dir          = "../../testdata/profile1/vectors/V6"
	v6File         = "consensus_messages_v6.json"
	v6Readme       = "README.md"
	v1VectorFile   = "../../testdata/profile1/vectors/V1/header_profile_v1.json"
	v6EpochLength  = 10800 // AM22
	v6LeaderTenure = 1     // AM22
)

type v6Validator struct {
	Index        int    `json:"index"`
	Address      string `json:"address"`
	BLSPublicKey string `json:"blsPublicKey"`
	BLSSecretKey string `json:"blsSecretKey,omitempty"`
}

type v6Message struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	View         uint64 `json:"view"`
	Domain       string `json:"domain"`
	Signer       string `json:"signer"`
	PreimageHex  string `json:"signingPreimage"`
	SignatureHex string `json:"signature"`
	WireHex      string `json:"wire"`
	EnvelopeHex  string `json:"h2v4Envelope"`
	GossipHex    string `json:"gossipSnappy"`
	Note         string `json:"note,omitempty"`
}

type v6Verdict struct {
	Case    string `json:"case"`
	Rules   string `json:"rules"`
	Input   string `json:"input"`
	Steps   string `json:"steps"`
	Verdict string `json:"verdict"`
	Error   string `json:"error,omitempty"`
	Outcome string `json:"outcome"`
}

type v6Size struct {
	N             int      `json:"n"`
	F             int      `json:"f"`
	Quorum        int      `json:"quorum"`
	KeySeed       string   `json:"keySeed"`
	QCBytes       int      `json:"qcSszBytes"`
	TCBytes       int      `json:"tcSszBytes"`
	DecideWire    int      `json:"decideWireBytes"`
	NewViewWire   int      `json:"newViewWireBytes"`
	NewViewEnv    int      `json:"newViewEnvelopeBytes"`
	NewViewGossip int      `json:"newViewGossipBytes"`
	PublicKeys    []string `json:"blsPublicKeys,omitempty"`
	DecideHex     string   `json:"decideWire,omitempty"`
	NewViewHex    string   `json:"newViewWire,omitempty"`
	Verdict       string   `json:"verdict"`
}

type v6File_ struct {
	Standard     string        `json:"standard"`
	Generator    string        `json:"generator"`
	ChainID      uint64        `json:"chainId"`
	GenesisHash  string        `json:"genesisHash"`
	SignatureDST string        `json:"signatureDst"`
	LeaderTenure uint64        `json:"leaderTenure"`
	EpochLength  uint64        `json:"epochLength"`
	Validators   []v6Validator `json:"validators"`
	Messages     []v6Message   `json:"messages"`
	Verdicts     []v6Verdict   `json:"verdicts"`
	Sizes        []v6Size      `json:"sizes"`
}

type v6V1 struct {
	ChainID     uint64        `json:"chainId"`
	GenesisHash string        `json:"genesisHash"`
	Validators  []v6Validator `json:"validators"`
	Headers     []struct {
		Name string `json:"name"`
		Hash string `json:"hash"`
	} `json:"headers"`
}

// v6Net is a validator set with its secret keys.
type v6Net struct {
	keys []common.SecretKey
	vs   *ValidatorSet
}

func v6NetFromV1(t *testing.T, v1 *v6V1) *v6Net {
	t.Helper()
	n := &v6Net{}
	var infos []ValidatorInfo
	for _, v := range v1.Validators {
		sk, err := bls.SecretKeyFromBytes(mustHex(t, v.BLSSecretKey))
		if err != nil {
			t.Fatal(err)
		}
		n.keys = append(n.keys, sk)
		infos = append(infos, ValidatorInfo{Address: types.HexToAddress(v.Address), PublicKey: sk.PublicKey()})
	}
	n.vs = NewValidatorSet(infos, uint32((len(infos)-1)/3))
	return n
}

// v6DerivedNet derives n validators from seed (sha256(seed || i), first
// byte cleared, as V1 does).
func v6DerivedNet(t *testing.T, seed string, size int) *v6Net {
	t.Helper()
	n := &v6Net{}
	var infos []ValidatorInfo
	for i := 0; i < size; i++ {
		s := sha256.Sum256([]byte(fmt.Sprintf("%s-%d", seed, i)))
		s[0] = 0
		sk, err := bls.SecretKeyFromBytes(s[:])
		if err != nil {
			t.Fatal(err)
		}
		var addr types.Address
		copy(addr[:], s[12:])
		n.keys = append(n.keys, sk)
		infos = append(infos, ValidatorInfo{Address: addr, PublicKey: sk.PublicKey()})
	}
	n.vs = NewValidatorSet(infos, uint32((size-1)/3))
	return n
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	if len(s) >= 2 && s[:2] == "0x" {
		s = s[2:]
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func hx(b []byte) string { return "0x" + hex.EncodeToString(b) }

// qc aggregates validators 0..q-1 over msg.
func (n *v6Net) qc(t *testing.T, view ViewNumber, blockHash types.Hash, msg []byte) QuorumCertificate {
	t.Helper()
	q := n.vs.QuorumSize()
	signers := make([]int, q)
	for i := range signers {
		signers[i] = i
	}
	return p1QC(t, n.keys, n.vs, view, blockHash, msg, signers...)
}

// tc aggregates validators 0..q-1's timeouts of view, each carrying highQC.
func (n *v6Net) tc(t *testing.T, id H2V4ChainIdentity, view ViewNumber, highQC QuorumCertificate) TimeoutCertificate {
	t.Helper()
	msg := H2V4TimeoutSigningMessage(id, view)
	tcol := NewTimeoutCollector(view, n.vs.Len())
	for i := 0; i < n.vs.QuorumSize(); i++ {
		if err := tcol.AddTimeout(ValidatorIndex(i), n.keys[i].Sign(msg), highQC); err != nil {
			t.Fatal(err)
		}
	}
	tc, err := tcol.BuildTCWithMessages(n.vs, msg, func(*QuorumCertificate) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	return *tc
}

// engine is a profile-1 engine of validator idx with the AM22 schedule.
func (n *v6Net) engine(t *testing.T, id H2V4ChainIdentity, idx int) (*ConsensusEngine, chan EngineOutput) {
	t.Helper()
	ch := make(chan EngineOutput, 1024)
	em := NewEpochManagerWithLength(n.vs, v6EpochLength)
	em.SetFixedSet()
	e := NewConsensusEngineWithEpochManager(ValidatorIndex(idx), n.keys[idx], em, 24000, 96000, ch)
	e.leaderTenure = v6LeaderTenure
	e.SetRotateOnTimeout(true, 0)
	e.EnableH2V4(id)
	e.SetProfile1(n.vs)
	return e, ch
}

func v6AtView(t *testing.T, e *ConsensusEngine, ch chan EngineOutput, view ViewNumber) {
	t.Helper()
	e.mu.Lock()
	err := e.advanceToView(view)
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	drainOutputs(ch)
}

func v6Wire(t *testing.T, id H2V4ChainIdentity, msg *ConsensusMsg) (wire, env, gossip []byte) {
	t.Helper()
	var err error
	if wire, err = EncodeConsensusMsg(msg); err != nil {
		t.Fatal(err)
	}
	if env, err = EncodeH2V4Envelope(H2V4Envelope{Identity: id, Message: msg}); err != nil {
		t.Fatal(err)
	}
	if gossip, err = EncodeH2V4Gossip(H2V4Envelope{Identity: id, Message: msg}); err != nil {
		t.Fatal(err)
	}
	// The envelope round-trips to the same message (canonical decode).
	back, err := DecodeH2V4Envelope(env, id)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := EncodeConsensusMsg(back.Message)
	if !bytes.Equal(again, wire) {
		t.Fatal("H2-v4 envelope does not round-trip")
	}
	return wire, env, gossip
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func verdictOf(err error) string {
	if err == nil {
		return "accepted"
	}
	return "refused"
}

func TestProfile1ConsensusVectorV6(t *testing.T) {
	raw, err := os.ReadFile(v1VectorFile)
	if err != nil {
		t.Fatal(err)
	}
	var v1 v6V1
	if err := json.Unmarshal(raw, &v1); err != nil {
		t.Fatal(err)
	}
	if len(v1.Headers) < 2 {
		t.Fatal("V1 has fewer than two headers")
	}
	id := H2V4ChainIdentity{ChainID: v1.ChainID, GenesisHash: types.HexToHash(v1.GenesisHash)}
	net := v6NetFromV1(t, &v1)
	h1 := types.HexToHash(v1.Headers[0].Hash)
	h2 := types.HexToHash(v1.Headers[1].Hash)
	zero := types.Hash{}

	out := v6File_{
		Standard:     "N42 mixed production standard, profile 1 (v0.3 + AM1-AM23)",
		Generator:    "gov5 internal/consensus/hotstuff/profile1_v6_vectors_test.go (TestProfile1ConsensusVectorV6)",
		ChainID:      id.ChainID,
		GenesisHash:  id.GenesisHash.Hex(),
		SignatureDST: "BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_",
		LeaderTenure: v6LeaderTenure,
		EpochLength:  v6EpochLength,
		Validators:   v1.Validators,
	}

	// ---- Messages ---------------------------------------------------------
	qc1Vote := net.qc(t, 1, h1, H2V4VoteSigningMessage(id, 1, h1))
	qc2Vote := net.qc(t, 2, h2, H2V4VoteSigningMessage(id, 2, h2))
	qc2Commit := net.qc(t, 2, h2, H2V4CommitSigningMessage(id, 2, h2, zero))

	addMsg := func(name, typ string, view ViewNumber, domain, signer string, pre, sig []byte, msg *ConsensusMsg, note string) []byte {
		wire, env, gossip := v6Wire(t, id, msg)
		out.Messages = append(out.Messages, v6Message{
			Name: name, Type: typ, View: view, Domain: domain, Signer: signer,
			PreimageHex: hx(pre), SignatureHex: hx(sig), WireHex: hx(wire), EnvelopeHex: hx(env), GossipHex: hx(gossip), Note: note,
		})
		return wire
	}

	propPre := H2V4ProposalSigningMessage(id, 2, h2, zero)
	proposal := &Proposal{View: 2, BlockHash: h2, JustifyQC: qc1Vote, Proposer: 2, Signature: net.keys[2].Sign(propPre).Marshal()}
	addMsg("proposal_v2", "proposal", 2, "proposal (phase 1)", "validator 2 = leader(2)", propPre, proposal.Signature,
		&ConsensusMsg{Type: MsgProposal, Payload: proposal}, "block 2 of V1, justified by the PrepareQC of block 1 at view 1 (validators 0,1,2)")

	votePre := H2V4VoteSigningMessage(id, 2, h2)
	vote := &Vote{View: 2, BlockHash: h2, Voter: 0, Signature: net.keys[0].Sign(votePre).Marshal()}
	addMsg("prepare_vote_v2", "vote", 2, "vote (phase 2)", "validator 0", votePre, vote.Signature,
		&ConsensusMsg{Type: MsgVote, Payload: vote}, "Round 1; cast only after the A13/AM23 gate")

	pqcMsg := &PrepareQCMsg{View: 2, BlockHash: h2, QC: qc2Vote}
	addMsg("prepare_qc_v2", "prepareQC", 2, "vote (phase 2), aggregate", "validators 0,1,2", votePre, qc2Vote.AggregateSignature,
		&ConsensusMsg{Type: MsgPrepareQC, Payload: pqcMsg}, "")

	commitPre := H2V4CommitSigningMessage(id, 2, h2, zero)
	cv := &CommitVote{View: 2, BlockHash: h2, Voter: 0, Signature: net.keys[0].Sign(commitPre).Marshal()}
	addMsg("commit_vote_v2", "commitVote", 2, "commit (phase 3), changesHash zero", "validator 0", commitPre, cv.Signature,
		&ConsensusMsg{Type: MsgCommitVote, Payload: cv}, "Round 2; cast only after this node's own execution of the block")

	decide := &Decide{View: 2, BlockHash: h2, CommitQC: qc2Commit}
	addMsg("decide_v2", "decide", 2, "commit (phase 3), aggregate", "validators 0,1,2", commitPre, qc2Commit.AggregateSignature,
		&ConsensusMsg{Type: MsgDecide, Payload: decide}, "the CommitQC is the anchor form (commit domain only)")

	// A TC landing on the epoch boundary: TC view E, next view E+1 = the
	// boundary b ((b-1) mod E = 0). With T = 1, view_after_tc(E) = E+1.
	tcView := ViewNumber(v6EpochLength)
	nvView := ViewAfterTimeout(tcView, v6LeaderTenure, v6EpochLength, true)
	if nvView != tcView+1 || (uint64(nvView)-1)%v6EpochLength != 0 {
		t.Fatalf("view_after_tc(%d) = %d, want the boundary %d", tcView, nvView, tcView+1)
	}
	toPre := H2V4TimeoutSigningMessage(id, tcView)
	tmsg := &TimeoutMessage{View: tcView, HighQC: qc2Vote, Sender: 0, Signature: net.keys[0].Sign(toPre).Marshal()}
	addMsg("timeout_v10800", "timeout", uint64(tcView), "timeout (phase 4)", "validator 0", toPre, tmsg.Signature,
		&ConsensusMsg{Type: MsgTimeout, Payload: tmsg}, "highQC = PrepareQC of block 2")

	tc := net.tc(t, id, tcView, qc2Vote)
	nvLeader := LeaderForViewWithTenure(nvView, net.vs, v6LeaderTenure)
	nvPre := H2V4NewViewSigningMessage(id, nvView)
	nv := &NewViewMsg{View: nvView, TimeoutCert: tc, Leader: nvLeader, Signature: net.keys[nvLeader].Sign(nvPre).Marshal()}
	addMsg("newview_tc_epoch_boundary", "newView", uint64(nvView), "newview (phase 5); TC in timeout (phase 4)",
		fmt.Sprintf("validator %d = leader(%d); TC by validators 0,1,2", nvLeader, nvView), nvPre, nv.Signature,
		&ConsensusMsg{Type: MsgNewView, Payload: nv}, fmt.Sprintf("TC of view %d (aggregate %s over its timeout preimage %s), highQC = PrepareQC of block 2; next view %d is the epoch boundary",
			tcView, hx(tc.AggregateSignature), hx(toPre), nvView))

	// ---- Verdicts ---------------------------------------------------------
	add := func(c, rules, input, steps string, err error, outcome string) {
		out.Verdicts = append(out.Verdicts, v6Verdict{Case: c, Rules: rules, Input: input, Steps: steps, Verdict: verdictOf(err), Error: errText(err), Outcome: outcome})
	}
	foreign := v6DerivedNet(t, "n42-profile1-v6-foreign-validator", 4)

	// Round 1 gate.
	{
		e, ch := net.engine(t, id, 0)
		v6AtView(t, e, ch, 2)
		err := p1Deliver(e, MsgProposal, proposal)
		p1Event(t, e, EventBlockHeaderKnown, h2, h1)
		afterHeader := p1Votes(ch, MsgVote)
		p1Event(t, e, EventBlockChecked, h2, h1)
		afterCheck := p1Votes(ch, MsgVote)
		p1Event(t, e, EventBlockImported, h1, zero)
		outs := drainOutputs(ch)
		afterParent := countOutputs(outs, OutputSendToValidator, MsgVote)
		if err != nil || afterHeader != 0 || afterCheck != 0 || afterParent != 1 {
			t.Fatalf("round-1 gate: err=%v header=%d check=%d parent=%d", err, afterHeader, afterCheck, afterParent)
		}
		for _, o := range outs {
			if o.Message != nil && o.Message.Type == MsgVote {
				got, _ := EncodeConsensusMsg(o.Message)
				want, _ := EncodeConsensusMsg(&ConsensusMsg{Type: MsgVote, Payload: vote})
				if !bytes.Equal(got, want) {
					t.Fatal("the engine's prepare vote differs from prepare_vote_v2")
				}
			}
		}
		add("round1_gate", "A13, E6, E7, AM19, AM23", "proposal_v2 to validator 0 at view 2",
			"header known -> 0 votes; deferred check passed -> 0 votes (parent unimported); parent imported -> 1 vote", err,
			"prepare_vote_v2 is cast only after the static header check, the deferred proposal check and the parent's import")
	}
	{
		e, ch := net.engine(t, id, 0)
		v6AtView(t, e, ch, 2)
		err := p1Deliver(e, MsgProposal, proposal)
		p1Event(t, e, EventBlockImported, h1, zero)
		p1Event(t, e, EventBlockImported, h2, h1)
		n := p1Votes(ch, MsgVote)
		if err != nil || n != 0 {
			t.Fatalf("refused block: err=%v votes=%d", err, n)
		}
		err = errors.New("not voted: the block has no passing deferred proposal check")
		add("round1_refused_block", "A13, AM19, J2", "proposal_v2 whose block fails the deferred proposal check (or the static header check)",
			"parent and block imported, no passing check", err, "no prepare vote; the refusal is logged with its ErrProfile1* reason and counted in n42_blocks_refused_total{reason}")
	}
	{
		e, ch := net.engine(t, id, 0)
		v6AtView(t, e, ch, 2)
		bad := *proposal
		bad.Proposer = 3
		bad.Signature = net.keys[3].Sign(propPre).Marshal()
		add("proposal_wrong_proposer", "E6", "proposal_v2 signed by validator 3 (not leader(2))", "deliver", p1Deliver(e, MsgProposal, &bad), "no vote")
		bad2 := *proposal
		bad2.Signature = net.keys[2].Sign(votePre).Marshal()
		add("proposal_vote_domain_signature", "E6, E10", "proposal_v2 signed in the vote domain", "deliver", p1Deliver(e, MsgProposal, &bad2), "no vote")
		bad3 := *proposal
		bad3.JustifyQC = p1QC(t, foreign.keys, foreign.vs, 1, h1, H2V4VoteSigningMessage(id, 1, h1), 0, 1, 2)
		bad3.Signature = net.keys[2].Sign(propPre).Marshal()
		add("proposal_justify_foreign_set", "E6, E13, AM5", "proposal_v2 justified by a QC of a foreign 4-member set", "deliver", p1Deliver(e, MsgProposal, &bad3), "no vote")
		drainOutputs(ch)
	}
	// Locking (AM19 last bullet).
	{
		e, ch := net.engine(t, id, 0)
		v6AtView(t, e, ch, 3)
		e.mu.Lock()
		e.roundState.UpdateLockedQC(&qc2Vote)
		e.mu.Unlock()
		x3 := types.Hash{0x03}
		low := p1Proposal(&testSetup{keys: net.keys, vs: net.vs}, id, 3, x3, qc1Vote, 3)
		err := p1Deliver(e, MsgProposal, low)
		var sv *SafetyViolationError
		if !errors.As(err, &sv) {
			t.Fatalf("justify below the lock: %v", err)
		}
		add("lock_refuses_lower_justify", "E6, AM19", "view 3 proposal (leader 3) justified by the view-1 QC while locked on the view-2 PrepareQC",
			"lock = PrepareQC(view 2, block 2); deliver", err, "refused: justify.view < locked.view")
		drainOutputs(ch)
	}
	{
		e, ch := net.engine(t, id, 0)
		v6AtView(t, e, ch, 3)
		e.mu.Lock()
		e.roundState.UpdateLockedQC(&qc1Vote)
		e.mu.Unlock()
		x3 := types.Hash{0x03}
		high := p1Proposal(&testSetup{keys: net.keys, vs: net.vs}, id, 3, x3, qc2Vote, 3)
		err := p1Deliver(e, MsgProposal, high)
		p1Event(t, e, EventBlockImported, h2, h1)
		p1Event(t, e, EventBlockChecked, x3, h2)
		n := p1Votes(ch, MsgVote)
		if err != nil || n != 1 || e.LockedQC().View != 2 {
			t.Fatalf("higher justify: err=%v votes=%d lock=%d", err, n, e.LockedQC().View)
		}
		add("lock_moves_to_higher_justify", "E6, AM19", "view 3 proposal (leader 3) of a block on block 2, justified by the view-2 PrepareQC while locked at view 1",
			"deliver; block 2 imported; block checked", err, "accepted; the lock moves to view 2 and the prepare vote is cast")
	}
	// Round 2 gate.
	{
		e, ch := net.engine(t, id, 0)
		v6AtView(t, e, ch, 2)
		err := p1Deliver(e, MsgPrepareQC, pqcMsg)
		p1Event(t, e, EventBlockChecked, h2, h1)
		p1Event(t, e, EventBlockImported, h1, zero)
		before := p1Votes(ch, MsgCommitVote)
		p1Event(t, e, EventBlockImported, h2, h1)
		outs := drainOutputs(ch)
		after := countOutputs(outs, OutputSendToValidator, MsgCommitVote)
		if err != nil || before != 0 || after != 1 {
			t.Fatalf("round-2 gate: err=%v before=%d after=%d", err, before, after)
		}
		for _, o := range outs {
			if o.Message != nil && o.Message.Type == MsgCommitVote {
				got, _ := EncodeConsensusMsg(o.Message)
				want, _ := EncodeConsensusMsg(&ConsensusMsg{Type: MsgCommitVote, Payload: cv})
				if !bytes.Equal(got, want) {
					t.Fatal("the engine's commit vote differs from commit_vote_v2")
				}
			}
		}
		add("round2_gate", "A13, E7, AM19", "prepare_qc_v2 to validator 0 at view 2",
			"block checked + parent imported -> 0 commit votes; block imported (own execution) -> 1", err,
			"commit_vote_v2 is cast only after this node's own execution of block 2")
	}
	{
		e, ch := net.engine(t, id, 0)
		v6AtView(t, e, ch, 2)
		fq := p1QC(t, foreign.keys, foreign.vs, 2, h2, votePre, 0, 1, 2)
		add("prepare_qc_foreign_set", "E13, AM5", "prepare_qc_v2 aggregated by a foreign 4-member set", "deliver",
			p1Deliver(e, MsgPrepareQC, &PrepareQCMsg{View: 2, BlockHash: h2, QC: fq}), "no commit vote")
		lq := p1QC(t, net.keys, net.vs, 2, h2, SigningMessage(2, h2), 0, 1, 2)
		add("prepare_qc_legacy_domain", "A14", "prepare_qc_v2 aggregated over the legacy (pre-H2-v4) preimage", "deliver",
			p1Deliver(e, MsgPrepareQC, &PrepareQCMsg{View: 2, BlockHash: h2, QC: lq}), "no commit vote")
		add("prepare_qc_commit_domain", "E10, E11", "prepare_qc_v2 carrying the commit-domain aggregate of decide_v2", "deliver",
			p1Deliver(e, MsgPrepareQC, &PrepareQCMsg{View: 2, BlockHash: h2, QC: qc2Commit}), "no commit vote")
		drainOutputs(ch)
	}
	// Anchors: commit domain only.
	{
		e, ch := net.engine(t, id, 0)
		v6AtView(t, e, ch, 2)
		add("decide_prepare_domain", "A14, E13", "decide_v2 carrying the vote-domain aggregate of prepare_qc_v2", "deliver",
			p1Deliver(e, MsgDecide, &Decide{View: 2, BlockHash: h2, CommitQC: qc2Vote}), "not committed")
		fq := p1QC(t, foreign.keys, foreign.vs, 2, h2, commitPre, 0, 1, 2)
		add("decide_foreign_set", "E13, AM5", "decide_v2 aggregated by a foreign 4-member set", "deliver",
			p1Deliver(e, MsgDecide, &Decide{View: 2, BlockHash: h2, CommitQC: fq}), "not committed")
		wide := v6DerivedNet(t, "n42-profile1-v6-wide-validator", 5)
		wq := wide.qc(t, 2, h2, commitPre)
		add("decide_wrong_width", "E13, AM5", "decide_v2 with a 5-bit signer bitmap (not the genesis set's width)", "deliver",
			p1Deliver(e, MsgDecide, &Decide{View: 2, BlockHash: h2, CommitQC: wq}), "not committed (ErrProfile1CertSet)")
		if e.LastCommittedQC().View != 0 {
			t.Fatal("a refused Decide committed")
		}
		err := p1Deliver(e, MsgDecide, decide)
		if err != nil || e.LastCommittedQC().View != 2 {
			t.Fatalf("decide_v2: %v", err)
		}
		add("decide_valid", "E12, E13", "decide_v2", "deliver", err, "block 2 committed; view advances to 3")
		drainOutputs(ch)
	}
	// TC on the epoch boundary.
	{
		e, ch := net.engine(t, id, 0)
		err := p1Deliver(e, MsgNewView, nv)
		if err != nil || e.CurrentView() != nvView || e.LockedQC().View != 2 {
			t.Fatalf("newview: err=%v view=%d lock=%d", err, e.CurrentView(), e.LockedQC().View)
		}
		add("tc_epoch_boundary_follower", "E4, E11, A18, A22, SYNC §10", "newview_tc_epoch_boundary to validator 0 at view 1",
			"deliver", err, fmt.Sprintf("accepted; view %d (the epoch boundary, set unchanged: the genesis set); lock = the TC's highQC (view 2)", nvView))
		drainOutputs(ch)

		l, lch := net.engine(t, id, int(nvLeader))
		if err := p1Deliver(l, MsgNewView, nv); err != nil {
			t.Fatal(err)
		}
		drainOutputs(lch)
		x := types.Hash{0x0b}
		if err := l.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: x}); err != nil {
			t.Fatal(err)
		}
		var prop *Proposal
		for _, o := range drainOutputs(lch) {
			if o.Type == OutputBroadcast && o.Message != nil && o.Message.Type == MsgProposal {
				prop = o.Message.Payload.(*Proposal)
			}
		}
		if prop == nil || prop.View != nvView || prop.JustifyQC.View != tc.HighQC.View || prop.JustifyQC.BlockHash != tc.HighQC.BlockHash {
			t.Fatalf("leader after TC proposed %+v", prop)
		}
		addMsg("proposal_after_tc", "proposal", uint64(nvView), "proposal (phase 1)", fmt.Sprintf("validator %d = leader(%d)", nvLeader, nvView),
			H2V4ProposalSigningMessage(id, nvView, x, zero), prop.Signature, &ConsensusMsg{Type: MsgProposal, Payload: prop},
			"block hash 0x0b00.. (a stand-in); JustifyQC = the TC's highQC")
		add("tc_leader_proposes_on_high_qc", "AM19, E6, F5", "newview_tc_epoch_boundary to the leader of the boundary view, then a block is ready",
			"deliver; EventBlockReady", nil, "proposal_after_tc justified by the highest QC the TC carries")
	}
	{
		e, _ := net.engine(t, id, 0)
		ftc := foreign.tc(t, id, tcView, qc2Vote)
		fnv := *nv
		fnv.TimeoutCert = ftc
		add("tc_foreign_set", "E11, E13, AM5", "newview_tc_epoch_boundary with a TC aggregated by a foreign set", "deliver",
			p1Deliver(e, MsgNewView, &fnv), "view unchanged")
		lnv := *nv
		lnv.View = nvView + 1
		lnv.Leader = LeaderForViewWithTenure(lnv.View, net.vs, v6LeaderTenure)
		lnv.Signature = net.keys[lnv.Leader].Sign(H2V4NewViewSigningMessage(id, lnv.View)).Marshal()
		add("tc_wrong_next_view", "E4, SYNC §10", fmt.Sprintf("a NewView for view %d carrying the TC of view %d", lnv.View, tcView), "deliver",
			p1Deliver(e, MsgNewView, &lnv), "view unchanged")
		if e.CurrentView() != 1 {
			t.Fatalf("a refused NewView moved the view to %d", e.CurrentView())
		}
	}
	for _, v := range out.Verdicts {
		want := "refused"
		switch v.Case {
		case "round1_gate", "lock_moves_to_higher_justify", "round2_gate", "decide_valid",
			"tc_epoch_boundary_follower", "tc_leader_proposes_on_high_qc":
			want = "accepted"
		}
		if v.Verdict != want {
			t.Fatalf("%s: %s (%s), want %s", v.Case, v.Verdict, v.Error, want)
		}
	}

	// ---- Sizes ------------------------------------------------------------
	for _, size := range []int{4, 100, 512} {
		sn, seed := net, "V1 validators"
		if size != 4 {
			seed = "n42-profile1-v6-validator"
			sn = v6DerivedNet(t, seed, size)
		}
		cq := sn.qc(t, 2, h2, commitPre)
		vq := sn.qc(t, 2, h2, votePre)
		stc := sn.tc(t, id, tcView, vq)
		sl := LeaderForViewWithTenure(nvView, sn.vs, v6LeaderTenure)
		snv := &NewViewMsg{View: nvView, TimeoutCert: stc, Leader: sl, Signature: sn.keys[sl].Sign(nvPre).Marshal()}
		sd := &Decide{View: 2, BlockHash: h2, CommitQC: cq}
		qcb, _ := encodeQC(&cq)
		tcb, _ := encodeTC(&stc)
		dw, _, _ := v6Wire(t, id, &ConsensusMsg{Type: MsgDecide, Payload: sd})
		nw, ne, ng := v6Wire(t, id, &ConsensusMsg{Type: MsgNewView, Payload: snv})

		e, ch := sn.engine(t, id, 0)
		derr := p1Deliver(e, MsgDecide, sd)
		e2, _ := sn.engine(t, id, 0)
		nerr := p1Deliver(e2, MsgNewView, snv)
		drainOutputs(ch)
		if derr != nil || nerr != nil || e2.CurrentView() != nvView {
			t.Fatalf("n=%d: decide %v newview %v", size, derr, nerr)
		}
		s := v6Size{N: size, F: (size - 1) / 3, Quorum: sn.vs.QuorumSize(), KeySeed: seed, QCBytes: len(qcb), TCBytes: len(tcb),
			DecideWire: len(dw), NewViewWire: len(nw), NewViewEnv: len(ne), NewViewGossip: len(ng),
			Verdict: "decide and newview (TC on the epoch boundary) accepted by a profile-1 engine of that set"}
		if size == 100 {
			for i := 0; i < size; i++ {
				pk, _ := sn.vs.GetPublicKey(ValidatorIndex(i))
				s.PublicKeys = append(s.PublicKeys, hx(pk.Marshal()))
			}
			s.DecideHex, s.NewViewHex = hx(dw), hx(nw)
		}
		out.Sizes = append(out.Sizes, s)
	}

	enc, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{v6File: append(enc, '\n'), v6Readme: []byte(v6ReadmeText(out))}
	var dirs []string
	if os.Getenv("N42_UPDATE_PROFILE1_FIXTURE") == "1" {
		dirs = append(dirs, v6Dir)
	}
	if d := os.Getenv("N42_PROFILE1_V6_OUT"); d != "" {
		dirs = append(dirs, d)
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, b := range files {
			if err := os.WriteFile(filepath.Join(d, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(v6Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s/%s differs from its derivation; rerun with N42_UPDATE_PROFILE1_FIXTURE=1 if intended", v6Dir, name)
		}
	}
	t.Logf("V6: %d messages, %d verdicts, sizes n=4/100/512", len(out.Messages), len(out.Verdicts))
}

func v6ReadmeText(v v6File_) string {
	var b bytes.Buffer
	b.WriteString(`# Vector V6: consensus messages (N42 mixed production standard, profile 1)

Generated by gov5 ` + "`internal/consensus/hotstuff/profile1_v6_vectors_test.go`" + ` (TestProfile1ConsensusVectorV6);
gov5's code is the bytes reference. Do not edit by hand: rerun the test with
` + "`N42_UPDATE_PROFILE1_FIXTURE=1`" + ` (gov5 testdata copy) and ` + "`N42_PROFILE1_V6_OUT=<dir>`" + `.

## Identity and validators

Chain id ` + fmt.Sprint(v.ChainID) + `, genesis hash ` + "`" + v.GenesisHash + "`" + ` and the four validators (with their test BLS
secret keys) are vector V1's. Leader tenure ` + fmt.Sprint(v.LeaderTenure) + `, epoch length ` + fmt.Sprint(v.EpochLength) + ` (AM22). Signatures use
the POP DST ` + "`" + v.SignatureDST + "`" + `; aggregates are plain BLS aggregates over the
signers in the bitmap (validator index order = bitmap order = genesis list order).

## Files

- ` + "`consensus_messages_v6.json`" + `:
  - ` + "`messages`" + `: proposal, prepare vote, PrepareQC, commit vote, Decide (CommitQC), timeout and NewView
    (TC on the epoch boundary), plus the proposal a leader makes after adopting that TC. Each with its
    H2-v4 signing preimage (E10: "N42H2V4" || phase || chainId u64 LE || genesisHash || view u64 LE || payload),
    signature, SSZ wire bytes (` + "`EncodeConsensusMsg`" + `), the H2-v4 envelope
    ("N42H2V4" || chainId || genesisHash || changesHash(0) || len u32 LE || wire) and its snappy block (gossip form).
  - ` + "`verdicts`" + `: gov5's accept/refuse decision for each case, with the error text.
  - ` + "`sizes`" + `: QC/TC SSZ sizes and Decide/NewView wire sizes at n = 4, 100 and 512 (quorum signers 0..2f);
    for n = 100 the public keys and the full Decide and NewView wire bytes are included so another client can
    verify the aggregates (keys: sha256("n42-profile1-v6-validator-" || i), first byte cleared).

## Rules covered

- A13/AM19/AM23 Round 1: a prepare vote only after the static header check (incl. the B4 clock bound), the
  deferred-execution proposal check and the parent's import; never on a header alone or an import alone.
- A13/AM19 Round 2: a commit vote only after this node's own execution of the block (not a checked block on an
  imported parent).
- AM19 locking: lock on the highest PrepareQC seen; a proposal is voted only if its justification is not below
  the lock (justify.view >= locked.view) and the block's parent is the justify block.
- AM19 TC rule: a leader entering a view by a TC proposes on the highest QC the TC carries.
- A14/E13/AM5: every QC/TC verifies against the fixed genesis set in the H2-v4 domain; a foreign set, a wrong
  bitmap width, a legacy preimage or (for anchors: Decide) the vote domain are refused. A proposal's JustifyQC
  may be vote or commit domain (E6).
- SYNC §10 with T = 1: the TC of view E lands the next view on the epoch boundary E+1; the set is unchanged.

## Verdicts

| case | verdict | rules |
| --- | --- | --- |
`)
	for _, c := range v.Verdicts {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", c.Case, c.Verdict, c.Rules)
	}
	b.WriteString("\n## Sizes (bytes)\n\n| n | quorum | QC | TC | Decide wire | NewView wire | NewView envelope | NewView gossip |\n| --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, s := range v.Sizes {
		fmt.Fprintf(&b, "| %d | %d | %d | %d | %d | %d | %d | %d |\n", s.N, s.Quorum, s.QCBytes, s.TCBytes, s.DecideWire, s.NewViewWire, s.NewViewEnv, s.NewViewGossip)
	}
	return b.String()
}
