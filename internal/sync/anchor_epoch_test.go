// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/syncanchor"
	"github.com/n42blockchain/N42/params"
)

// hsAnchorVerifier is the real HotStuff strict header-QC verifier with the
// consensus notifications muted (the engine is not running here).
type hsAnchorVerifier struct{ *hotstuff.Service }

func (hsAnchorVerifier) NotifyBlockImported(comtypes.Hash, comtypes.Hash)            {}
func (hsAnchorVerifier) NotifyBlockChecked(comtypes.Hash, comtypes.Hash)             {}
func (hsAnchorVerifier) NotifyBlockRejected(comtypes.Hash)                           {}
func (hsAnchorVerifier) NotifyBlockHeaderKnown(comtypes.Hash, comtypes.Hash, uint64) {}

type hsCommittee struct {
	keys []blscommon.SecretKey
	vals []hotstuff.ValidatorInfo
}

func newHSCommittee(t *testing.T, n int) *hsCommittee {
	t.Helper()
	c := &hsCommittee{}
	for i := 0; i < n; i++ {
		sk, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		var addr comtypes.Address
		addr[0] = byte(i + 1)
		c.keys = append(c.keys, sk)
		c.vals = append(c.vals, hotstuff.ValidatorInfo{Address: addr, PublicKey: sk.PublicKey()})
	}
	return c
}

func (c *hsCommittee) qc(t *testing.T, vs *hotstuff.ValidatorSet, view hotstuff.ViewNumber, hash comtypes.Hash, msg []byte) *hotstuff.QuorumCertificate {
	t.Helper()
	vc := hotstuff.NewVoteCollector(view, hash, vs.Len())
	for i := 0; i < vs.QuorumSize(); i++ {
		if err := vc.AddVote(hotstuff.ValidatorIndex(i), c.keys[i].Sign(msg)); err != nil {
			t.Fatal(err)
		}
	}
	qc, err := vc.BuildQCWithMessage(vs, msg)
	if err != nil {
		t.Fatal(err)
	}
	return qc
}

// A restarted enforce-mode node (persisted view in epoch 122, epochLength
// 200, static committee, no epoch schedule) catches up a range whose header
// QCs span the boundary into epoch 123, after its view jumped to the network
// via a QC. Every block up to the highest QC-named block executes, nothing is
// refused for an unknown epoch, and the next commit anchor releases the top.
func TestAnchorEnforceStaticCommitteeAcrossEpochBoundary(t *testing.T) {
	const (
		epochLen = 200
		seedView = hotstuff.ViewNumber(24514) // epoch 122
		jumpView = hotstuff.ViewNumber(24766) // epoch 123
		viewBase = 24486                      // block n has view viewBase+n; block 115 opens epoch 123
		top      = 130
	)
	com := newHSCommittee(t, 4)
	h := hotstuff.New(&params.HotStuffConfig{BaseTimeout: 60000, MaxTimeout: 120000, EpochLength: epochLen}, nil)
	h.Authorize(com.vals[0].Address, com.keys[0])
	if err := h.InitEngine(com.vals, 1); err != nil {
		t.Fatal(err)
	}
	ce := h.Engine()
	vs := ce.CurrentValidatorSet()
	ce.RestoreState(seedView, hotstuff.QuorumCertificate{}, hotstuff.QuorumCertificate{}, 0)

	// The network is ahead: a prepare QC at jumpView-1 pulls the view forward.
	jumpHash := comtypes.HexToHash("0x24765")
	jumpQC := com.qc(t, vs, jumpView-1, jumpHash, hotstuff.SigningMessage(jumpView-1, jumpHash))
	if err := ce.ProcessEvent(hotstuff.ConsensusEvent{Type: hotstuff.EventMessage, Msg: hotstuff.ConsensusMsg{
		Type:    hotstuff.MsgPrepareQC,
		Payload: &hotstuff.PrepareQCMsg{View: jumpView + 30, BlockHash: jumpHash, QC: *jumpQC},
	}}); err != nil {
		t.Fatalf("view jump: %v", err)
	}
	if ce.CurrentView() != jumpView {
		t.Fatalf("view = %d, want %d", ce.CurrentView(), jumpView)
	}

	// Blocks 0..top; block n embeds the commit QC of block n-2.
	extraFor := func(n uint64, all []block.IBlock) []byte {
		var qc *hotstuff.QuorumCertificate
		if n >= 2 {
			v := hotstuff.ViewNumber(viewBase + n - 2)
			target := all[n-2].Hash()
			qc = com.qc(t, vs, v, target, hotstuff.CommitSigningMessage(v, target))
		}
		extra, err := hotstuff.BuildHeaderExtra(hotstuff.ViewNumber(viewBase+n), qc)
		if err != nil {
			t.Fatal(err)
		}
		return extra
	}
	all := []block.IBlock{anchorBlock(comtypes.Hash{}, 0, 0, extraFor(0, nil))}
	for n := uint64(1); n <= top; n++ {
		all = append(all, anchorBlock(all[n-1].Hash(), n, 0, extraFor(n, all)))
	}

	t.Cleanup(syncanchor.SetForTest(syncanchor.Enforce))
	net := newHarnessNet(t, newHarnessChain(all[:101]...))
	net.svc.cfg.blockImportNotifier = hsAnchorVerifier{hotstuff.NewService(h, nil, nil, "", "")}
	net.svc.claimQuorum.Store(1)
	p := net.addPeer(top, harnessHonest, all...)
	unknownBefore := refused(anchorReasonUnknownEpoch)
	badBefore := refused(anchorReasonBadQC)
	unanchoredBefore := refused(anchorReasonUnanchored)

	net.svc.catchUpTo(top, []peer.ID{p.id})
	if d := refused(anchorReasonUnknownEpoch) - unknownBefore; d != 0 {
		t.Fatalf("unknown-epoch refusals grew by %v, want 0", d)
	}
	if d := refused(anchorReasonBadQC) - badBefore; d != 0 {
		t.Fatalf("bad-QC refusals grew by %v, want 0", d)
	}
	// Only the two blocks above the highest QC-named block wait (no header
	// names them yet); everything across the boundary executed.
	if got := net.chain.headNumber(); got != top-2 {
		t.Fatalf("head = %d, want %d (QCs of epoch 123 must anchor)", got, top-2)
	}
	if d := refused(anchorReasonUnanchored) - unanchoredBefore; d != 2 {
		t.Fatalf("unanchored grew by %v, want 2 (the top awaiting its QC)", d)
	}
	if net.badResponses(p) != 0 {
		t.Fatal("honest peer scored")
	}

	net.svc.NoteCommitAnchor(all[top].Hash(), top)
	waitHead(t, net.chain, top)
}

// The unknown-epoch WARN fires once per epoch per interval.
func TestShouldWarnUnknownEpochPerEpochInterval(t *testing.T) {
	const e1, e2 = 1 << 40, 1<<40 + 1 // epochs no other test reports
	unknownEpochWarn.mu.Lock()
	delete(unknownEpochWarn.last, e1)
	delete(unknownEpochWarn.last, e2)
	unknownEpochWarn.mu.Unlock()
	now := time.Now()
	if !shouldWarnUnknownEpoch(e1, now) {
		t.Fatal("first warning for an epoch must fire")
	}
	if shouldWarnUnknownEpoch(e1, now.Add(time.Second)) {
		t.Fatal("repeat within the interval must be suppressed")
	}
	if !shouldWarnUnknownEpoch(e2, now.Add(time.Second)) {
		t.Fatal("another epoch is limited separately")
	}
	if !shouldWarnUnknownEpoch(e1, now.Add(unknownEpochWarnInterval+time.Second)) {
		t.Fatal("warning must fire again after the interval")
	}
}
