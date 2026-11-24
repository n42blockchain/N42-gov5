// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bridge

import (
	"strings"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

type p1BridgeNet struct {
	keys []blscommon.SecretKey
	vs   *hotstuff.ValidatorSet
}

func newP1BridgeNet(t *testing.T, n int) *p1BridgeNet {
	t.Helper()
	keys := make([]blscommon.SecretKey, n)
	vals := make([]hotstuff.ValidatorInfo, n)
	for i := range keys {
		sk, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = sk
		vals[i] = hotstuff.ValidatorInfo{Address: types.Address{byte(i + 1)}, PublicKey: sk.PublicKey()}
	}
	return &p1BridgeNet{keys: keys, vs: hotstuff.NewValidatorSet(vals, uint32((n-1)/3))}
}

func (n *p1BridgeNet) qc(t *testing.T, view hotstuff.ViewNumber, blockHash types.Hash, msg []byte) hotstuff.QuorumCertificate {
	t.Helper()
	vc := hotstuff.NewVoteCollector(view, blockHash, n.vs.Len())
	for i := 0; i < int(n.vs.QuorumSize()); i++ {
		if err := vc.AddVote(hotstuff.ValidatorIndex(i), n.keys[i].Sign(msg)); err != nil {
			t.Fatal(err)
		}
	}
	qc, err := vc.BuildQCWithMessage(n.vs, msg)
	if err != nil {
		t.Fatal(err)
	}
	return *qc
}

func p1BridgeChain(t *testing.T, parentQC hotstuff.QuorumCertificate, header1 *block.Header) []*block.Header {
	t.Helper()
	header2 := &block.Header{
		ParentHash: header1.Hash(),
		Number:     uint256.NewInt(2),
		Time:       2,
		Extra:      hotstuffExtraForTest(2, &parentQC),
	}
	return []*block.Header{header1, header2}
}

// Profile 1 verifies the header chain's certificates only in the chain-bound
// commit domain against the genesis set; a prepare-domain, legacy-domain or
// foreign-chain certificate is refused, as is a missing set.
func TestVerifyHeaderChainLocallyProfile1CommitDomainOnly(t *testing.T) {
	net := newP1BridgeNet(t, 4)
	id := hotstuff.H2V4ChainIdentity{ChainID: 1144, GenesisHash: types.Hash{0x6f, 0x73}}
	header1 := &block.Header{Number: uint256.NewInt(1), Time: 1, Extra: hotstuffExtraForTest(1, nil)}
	h1 := header1.Hash()

	verify := func(qc hotstuff.QuorumCertificate, vs *hotstuff.ValidatorSet, ident hotstuff.H2V4ChainIdentity) error {
		return VerifyHeaderChainLocallyProfile1(p1BridgeChain(t, qc, header1),
			[]hotstuff.QuorumCertificate{hotstuff.GenesisQC(), qc}, vs, ident)
	}

	commit := net.qc(t, 1, h1, hotstuff.H2V4CommitSigningMessage(id, 1, h1, types.Hash{}))
	if err := verify(commit, net.vs, id); err != nil {
		t.Fatalf("commit-domain QC refused: %v", err)
	}

	prepare := net.qc(t, 1, h1, hotstuff.H2V4VoteSigningMessage(id, 1, h1))
	if err := verify(prepare, net.vs, id); err == nil {
		t.Fatal("prepare-domain QC accepted on profile 1")
	}
	legacy := net.qc(t, 1, h1, hotstuff.CommitSigningMessage(1, h1))
	if err := verify(legacy, net.vs, id); err == nil {
		t.Fatal("legacy commit-domain QC accepted on profile 1")
	}
	if err := VerifyHeaderChainLocally(p1BridgeChain(t, legacy, header1),
		[]hotstuff.QuorumCertificate{hotstuff.GenesisQC(), legacy}, net.vs); err != nil {
		t.Fatalf("legacy path no longer accepts its own QC: %v", err)
	}
	foreign := hotstuff.H2V4ChainIdentity{ChainID: 1145, GenesisHash: id.GenesisHash}
	if err := verify(commit, net.vs, foreign); err == nil {
		t.Fatal("a QC of another chain identity accepted")
	}
	other := newP1BridgeNet(t, 4)
	if err := verify(commit, other.vs, id); err == nil {
		t.Fatal("QC accepted against a set other than the signing genesis set")
	}
	if err := verify(commit, nil, id); err == nil || !strings.Contains(err.Error(), "genesis validator set") {
		t.Fatalf("nil genesis set: err = %v, want a refusal naming the genesis set", err)
	}
}
