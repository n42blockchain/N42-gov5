// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

type h2V4DomainFixture struct {
	Schema      string `json:"schema"`
	ChainID     uint64 `json:"chain_id"`
	GenesisHash string `json:"genesis_hash"`
	View        uint64 `json:"view"`
	BlockHash   string `json:"block_hash"`
	ChangesHash string `json:"changes_hash"`
	ProposalHex string `json:"proposal_hex"`
	VoteHex     string `json:"vote_hex"`
	CommitHex   string `json:"commit_hex"`
	TimeoutHex  string `json:"timeout_hex"`
	NewViewHex  string `json:"new_view_hex"`
}

func TestH2V4CrossClientSigningDomains(t *testing.T) {
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	view := uint64(0x0102030405060708)
	blockHash := repeatedHash(0x22)
	changesHash := repeatedHash(0x33)
	fixture := h2V4DomainFixture{
		Schema: "n42-h2-v4-domains-v1", ChainID: identity.ChainID,
		GenesisHash: hex.EncodeToString(identity.GenesisHash[:]), View: view,
		BlockHash: hex.EncodeToString(blockHash[:]), ChangesHash: hex.EncodeToString(changesHash[:]),
		ProposalHex: hex.EncodeToString(H2V4ProposalSigningMessage(identity, view, blockHash, changesHash)),
		VoteHex:     hex.EncodeToString(H2V4VoteSigningMessage(identity, view, blockHash)),
		CommitHex:   hex.EncodeToString(H2V4CommitSigningMessage(identity, view, blockHash, changesHash)),
		TimeoutHex:  hex.EncodeToString(H2V4TimeoutSigningMessage(identity, view)),
		NewViewHex:  hex.EncodeToString(H2V4NewViewSigningMessage(identity, view)),
	}
	got, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	want, err := os.ReadFile("testdata/h2_v4_domains_v1.json")
	if os.IsNotExist(err) {
		t.Logf("fixture to pin:\n%s", got)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("H2-v4 domain fixture drift\nwant:\n%s\ngot:\n%s", want, got)
	}

	otherChain := identity
	otherChain.ChainID++
	if reflect.DeepEqual(
		H2V4CommitSigningMessage(identity, view, blockHash, changesHash),
		H2V4CommitSigningMessage(otherChain, view, blockHash, changesHash),
	) {
		t.Fatal("chain identity must change the signing preimage")
	}
}

func TestH2V4EngineFormsVerifiableDecide(t *testing.T) {
	setup := newTestSetup(t, 4)
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	blockHash := repeatedHash(0x55)
	leader, outputs := newTestEngine(t, setup, 1)
	leader.EnableH2V4(identity)

	if err := leader.ProcessEvent(ConsensusEvent{Type: EventBlockReady, Hash: blockHash}); err != nil {
		t.Fatal(err)
	}
	drainOutputs(outputs)
	for _, index := range []int{0, 2} {
		signature := setup.keys[index].Sign(H2V4VoteSigningMessage(identity, 1, blockHash))
		vote := &Vote{View: 1, BlockHash: blockHash, Voter: ValidatorIndex(index), Signature: signature.Marshal()}
		if err := leader.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgVote, Payload: vote}}); err != nil {
			t.Fatalf("v4 prepare vote %d failed: %v", index, err)
		}
	}
	drainOutputs(outputs)
	for _, index := range []int{0, 2} {
		signature := setup.keys[index].Sign(H2V4CommitSigningMessage(identity, 1, blockHash, types.Hash{}))
		vote := &CommitVote{View: 1, BlockHash: blockHash, Voter: ValidatorIndex(index), Signature: signature.Marshal()}
		if err := leader.ProcessEvent(ConsensusEvent{Type: EventMessage, Msg: ConsensusMsg{Type: MsgCommitVote, Payload: vote}}); err != nil {
			t.Fatalf("v4 commit vote %d failed: %v", index, err)
		}
	}

	var decide *Decide
	for _, output := range drainOutputs(outputs) {
		if output.Type == OutputBroadcast && output.Message != nil && output.Message.Type == MsgDecide {
			decide = output.Message.Payload.(*Decide)
		}
	}
	if decide == nil {
		t.Fatal("H2-v4 engine did not form Decide")
	}
	envelope := &H2V4Envelope{
		Identity: identity, ChangesHash: types.Hash{},
		Message: &ConsensusMsg{Type: MsgDecide, Payload: decide},
	}
	if _, err := VerifyH2V4Decide(envelope, setup.vs); err != nil {
		t.Fatalf("engine-produced H2-v4 Decide failed verification: %v", err)
	}
	if err := VerifyCommitQC(&decide.CommitQC, setup.vs); err == nil {
		t.Fatal("native legacy verifier accepted H2-v4 CommitQC")
	}
}

func TestH2V4SingleValidatorTimeoutFormsNewView(t *testing.T) {
	setup := newTestSetup(t, 1)
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	engine, outputs := newTestEngine(t, setup, 0)
	engine.EnableH2V4(identity)
	if err := timeoutNow(engine); err != nil {
		t.Fatal(err)
	}
	var newView *NewViewMsg
	for _, output := range drainOutputs(outputs) {
		if output.Type == OutputBroadcast && output.Message != nil && output.Message.Type == MsgNewView {
			newView = output.Message.Payload.(*NewViewMsg)
		}
	}
	if newView == nil {
		t.Fatal("H2-v4 timeout did not form NewView")
	}
	if !VerifyBLSSignature(newView.Signature, setup.pubKeys[0], H2V4NewViewSigningMessage(identity, 2)) {
		t.Fatal("H2-v4 NewView signature is invalid")
	}
}
