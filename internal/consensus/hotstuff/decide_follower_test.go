// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/hex"
	"errors"
	"math/big"
	"sync"
	"testing"

	pubsub "github.com/libp2p/go-libp2p-pubsub"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/params"
)

// followerChain records commits; blocks in missing are not imported yet.
type followerChain struct {
	mu        sync.Mutex
	missing   map[types.Hash]bool
	committed []types.Hash
}

func (c *followerChain) CommitToCanonical(hash types.Hash) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.missing[hash] {
		return errors.New("committed block not in db")
	}
	c.committed = append(c.committed, hash)
	return nil
}

type noopFollowerP2P struct{}

func (noopFollowerP2P) SubscribeToTopic(string, ...pubsub.SubOpt) (*pubsub.Subscription, error) {
	return nil, errors.New("no network in this test")
}

func newFollowerTestEngine(t *testing.T, setup *testSetup) *HotStuff {
	t.Helper()
	cfg := &params.ChainConfig{ChainID: big.NewInt(94), N42Profile: params.N42Profile1, HotStuff: &params.HotStuffConfig{}}
	for _, v := range setup.validators {
		cfg.HotStuff.Validators = append(cfg.HotStuff.Validators, params.HotStuffValidatorConfig{
			Address: v.Address.Hex(), BLSKey: hex.EncodeToString(v.PublicKey.Marshal()),
		})
	}
	return New(cfg.HotStuff, cfg)
}

func followerDecideGossip(t *testing.T, setup *testSetup, identity H2V4ChainIdentity, view ViewNumber, blockHash types.Hash, signers []int) []byte {
	t.Helper()
	msg := H2V4CommitSigningMessage(identity, view, blockHash, types.Hash{})
	bitmap := make([]bool, len(setup.keys))
	var sigs []blscommon.Signature
	for _, i := range signers {
		bitmap[i] = true
		sigs = append(sigs, setup.keys[i].Sign(msg))
	}
	qc := QuorumCertificate{View: view, BlockHash: blockHash, AggregateSignature: bls.AggregateSignatures(sigs).Marshal(), Signers: bitmap}
	data, err := EncodeH2V4Gossip(H2V4Envelope{
		Identity: identity,
		Message:  &ConsensusMsg{Type: MsgDecide, Payload: &Decide{View: view, BlockHash: blockHash, CommitQC: qc}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// A non-validator commits a block on a verified Decide, keeps a Decide whose
// block has not arrived and commits it on retry, and refuses a Decide whose
// CommitQC the genesis set did not sign.
func TestDecideFollowerCommitsVerifiedDecides(t *testing.T) {
	setup := newTestSetup(t, 4)
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	chain := &followerChain{missing: map[types.Hash]bool{}}
	f, err := NewDecideFollower(newFollowerTestEngine(t, setup), chain, nil, identity)
	if err == nil {
		t.Fatal("follower without p2p accepted")
	}
	f, err = NewDecideFollower(newFollowerTestEngine(t, setup), chain, noopFollowerP2P{}, identity)
	if err != nil {
		t.Fatal(err)
	}

	b1, b2 := repeatedHash(0x01), repeatedHash(0x02)
	if err := f.HandleMessage(followerDecideGossip(t, setup, identity, 2, b1, []int{0, 1, 2})); err != nil {
		t.Fatalf("valid Decide refused: %v", err)
	}
	chain.mu.Lock()
	chain.missing[b2] = true
	chain.mu.Unlock()
	if err := f.HandleMessage(followerDecideGossip(t, setup, identity, 3, b2, []int{1, 2, 3})); err != nil {
		t.Fatalf("valid Decide refused: %v", err)
	}
	if f.PendingCount() != 1 {
		t.Fatalf("pending %d, want the Decide whose block is missing", f.PendingCount())
	}
	chain.mu.Lock()
	delete(chain.missing, b2)
	chain.mu.Unlock()
	f.retryPending()
	if f.PendingCount() != 0 || len(chain.committed) != 2 || chain.committed[0] != b1 || chain.committed[1] != b2 {
		t.Fatalf("commits %v pending %d, want b1 then b2", chain.committed, f.PendingCount())
	}

	// Below quorum, a foreign key, and a different chain identity are refused.
	if err := f.HandleMessage(followerDecideGossip(t, setup, identity, 4, repeatedHash(0x03), []int{0, 1})); err == nil {
		t.Fatal("Decide below quorum accepted")
	}
	other := newTestSetup(t, 4)
	if err := f.HandleMessage(followerDecideGossip(t, other, identity, 4, repeatedHash(0x03), []int{0, 1, 2})); err == nil {
		t.Fatal("Decide signed by foreign keys accepted")
	}
	foreign := H2V4ChainIdentity{ChainID: 95, GenesisHash: identity.GenesisHash}
	if err := f.HandleMessage(followerDecideGossip(t, setup, foreign, 4, repeatedHash(0x03), []int{0, 1, 2})); err == nil {
		t.Fatal("Decide of another chain accepted")
	}
	if len(chain.committed) != 2 {
		t.Fatalf("refused Decides committed: %v", chain.committed)
	}
}

// A chain outside profile 1 has no engine-free validator set: no follower.
func TestDecideFollowerRequiresProfile1(t *testing.T) {
	h := New(&params.HotStuffConfig{}, &params.ChainConfig{})
	if _, err := NewDecideFollower(h, &followerChain{}, noopFollowerP2P{}, H2V4ChainIdentity{}); err == nil {
		t.Fatal("follower built outside profile 1")
	}
}

// A verified Decide whose block the follower does not hold asks the sync
// layer to catch up to it, again after decideFollowerAskEvery, and not for a
// block it commits at once.
func TestDecideFollowerAsksCatchUpForMissingBlocks(t *testing.T) {
	setup := newTestSetup(t, 4)
	identity := H2V4ChainIdentity{ChainID: 94, GenesisHash: repeatedHash(0x11)}
	b1, b2 := repeatedHash(0x01), repeatedHash(0x02)
	chain := &followerChain{missing: map[types.Hash]bool{b2: true}}
	f, err := NewDecideFollower(newFollowerTestEngine(t, setup), chain, noopFollowerP2P{}, identity)
	if err != nil {
		t.Fatal(err)
	}
	asked := make(chan types.Hash, 4)
	f.SetCatchUp(func(h types.Hash) { asked <- h })
	if err := f.HandleMessage(followerDecideGossip(t, setup, identity, 2, b1, []int{0, 1, 2})); err != nil {
		t.Fatal(err)
	}
	if err := f.HandleMessage(followerDecideGossip(t, setup, identity, 3, b2, []int{0, 1, 2})); err != nil {
		t.Fatal(err)
	}
	if h := <-asked; h != b2 {
		t.Fatalf("catch-up asked for %x, want the missing block", h)
	}
	f.retryPending() // within decideFollowerAskEvery: no second request
	f.mu.Lock()
	p := f.pending[b2]
	p.asked = p.asked.Add(-decideFollowerAskEvery)
	f.pending[b2] = p
	f.mu.Unlock()
	f.retryPending()
	if h := <-asked; h != b2 {
		t.Fatalf("second catch-up asked for %x", h)
	}
	select {
	case h := <-asked:
		t.Fatalf("unexpected catch-up request %x", h)
	default:
	}
}
