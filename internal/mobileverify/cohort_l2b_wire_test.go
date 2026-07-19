// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// End-to-end verification of the Layer 2B commit-reveal defense over the REAL
// relay wire path: every cross-node announcement is routed through
// encode*Announcement (with the reporter's validator BLS signature) and
// decode*Announcement (with BLS verification + reporter authorization),
// exactly as CohortRelay does on GossipSub — not the direct OnPeer* shortcut
// the other coordinator tests use. This exercises the full stack the P0 fix
// touches: L1 reporter authentication AND L2B proof-of-possession, against an
// authenticated but malicious validator (the exact f-collusion threat model).

package mobileverify

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

// wireGrid is a set of cohort validators wired to each other through the real
// announcement encode/decode+auth path. Each node signs its own outbound
// announcements with its validator BLS key; every receiver verifies the
// signature against the sender's registered validator public key (L1) before
// admitting the announcement.
type wireGrid struct {
	t      *testing.T
	addrs  []types.Address
	keys   []common.SecretKey
	coords []*CohortCoordinator
	stores []*CertStore
	reg    *Registry
	verify CohortVerifier
	misb   map[types.Address]int // reporter -> #MisbehaviorReports seen
}

func newWireGrid(t *testing.T, reg *Registry, blockHash types.Hash, number uint64, cfg CohortConfig, n int) *wireGrid {
	t.Helper()
	g := &wireGrid{t: t, reg: reg, misb: map[types.Address]int{}}
	pubByAddr := map[types.Address]common.PublicKey{}
	for i := 0; i < n; i++ {
		sk, err := bls.RandKey()
		if err != nil {
			t.Fatal(err)
		}
		var addr types.Address
		addr[19] = byte(i + 1)
		g.addrs = append(g.addrs, addr)
		g.keys = append(g.keys, sk)
		pubByAddr[addr] = sk.PublicKey()
		g.stores = append(g.stores, NewCertStore(64))
	}
	// The verifier is the on-wire L1 authentication: reporter must be a known
	// validator and the signature must verify against its registered key.
	g.verify = func(reporter types.Address, msg, sig []byte) bool {
		pk, ok := pubByAddr[reporter]
		if !ok {
			return false
		}
		s, err := bls.SignatureFromBytes(sig)
		if err != nil {
			return false
		}
		return s.Verify(pk, msg)
	}
	lookup := fixedLookupAt(blockHash, number)
	for i := 0; i < n; i++ {
		g.coords = append(g.coords, NewCohortCoordinator(reg, lookup, g.stores[i], g.addrs[i], cfg))
	}
	for i := range g.coords {
		i := i
		sign := g.signer(i)
		g.coords[i].SetIndexAnnounceSink(func(bh types.Hash, bn uint64, reporter types.Address, indices []IndexCommitment) {
			g.routeIndex(i, bh, bn, reporter, indices, sign)
		})
		g.coords[i].SetRevealAnnounceSink(func(bh types.Hash, bn uint64, reporter types.Address, reveal map[MobileIndex]RevealedSig) {
			g.routeReveal(i, bh, bn, reporter, reveal, sign)
		})
		g.coords[i].SetCertAnnounceSink(func(reporter types.Address, cert *MobileAttestationCert) {
			g.routeCert(i, reporter, cert, sign)
		})
		g.coords[i].SetMisbehaviorSink(func(r MisbehaviorReport) { g.misb[r.Reporter]++ })
	}
	return g
}

func (g *wireGrid) signer(i int) CohortSigner {
	key := g.keys[i]
	return func(msg []byte) []byte { return key.Sign(msg).Marshal() }
}

// routeIndex encodes node i's index announcement on the wire and delivers it to
// every other node through decode+auth — the same path a GossipSub message
// takes. A decode failure (bad auth / malformed) is dropped, exactly as the
// relay's receive loop drops it.
func (g *wireGrid) routeIndex(from int, bh types.Hash, bn uint64, reporter types.Address, indices []IndexCommitment, sign CohortSigner) {
	payload, err := encodeIndexAnnouncement(bh, bn, reporter, indices, sign)
	if err != nil {
		return
	}
	for j := range g.coords {
		if j == from {
			continue
		}
		gbh, _, grep, gidx, derr := decodeIndexAnnouncement(payload, g.reg.IndexBound(), g.verify)
		if derr != nil {
			continue
		}
		g.coords[j].OnPeerIndexSet(gbh, grep, gidx)
	}
}

func (g *wireGrid) routeReveal(from int, bh types.Hash, bn uint64, reporter types.Address, reveal map[MobileIndex]RevealedSig, sign CohortSigner) {
	payload := encodeRevealAnnouncement(bh, bn, reporter, reveal, sign)
	for j := range g.coords {
		if j == from {
			continue
		}
		gbh, _, grep, grev, derr := decodeRevealAnnouncement(payload, g.reg.IndexBound(), g.verify)
		if derr != nil {
			continue
		}
		g.coords[j].OnPeerReveal(gbh, grep, grev)
	}
}

func (g *wireGrid) routeCert(from int, reporter types.Address, cert *MobileAttestationCert, sign CohortSigner) {
	payload := encodeCertAnnouncement(reporter, cert, sign)
	for j := range g.coords {
		if j == from {
			continue
		}
		grep, gcert, derr := decodeCertAnnouncement(payload, g.reg.IndexBound(), g.verify)
		if derr != nil {
			continue
		}
		g.coords[j].OnPeerCert(grep, gcert)
	}
}

// injectIndex simulates an authenticated but malicious validator (node atk)
// broadcasting an index announcement it manufactured — e.g. replaying a victim
// device's public commitment it observed from an honest node's announcement.
// It goes through the exact same encode(sign)+decode(verify) path, so if L1
// auth or the wire format could be bypassed, this would slip through.
func (g *wireGrid) injectIndex(atk int, targets []int, bh types.Hash, bn uint64, indices []IndexCommitment) {
	payload, err := encodeIndexAnnouncement(bh, bn, g.addrs[atk], indices, g.signer(atk))
	if err != nil {
		g.t.Fatalf("inject encode: %v", err)
	}
	for _, j := range targets {
		gbh, _, grep, gidx, derr := decodeIndexAnnouncement(payload, g.reg.IndexBound(), g.verify)
		if derr != nil {
			g.t.Fatalf("inject decode (auth) unexpectedly failed: %v", derr)
		}
		g.coords[j].OnPeerIndexSet(gbh, grep, gidx)
	}
}

// injectReveal is the reveal-phase counterpart of injectIndex.
func (g *wireGrid) injectReveal(atk int, targets []int, bh types.Hash, bn uint64, reveal map[MobileIndex]RevealedSig) {
	payload := encodeRevealAnnouncement(bh, bn, g.addrs[atk], reveal, g.signer(atk))
	for _, j := range targets {
		gbh, _, grep, grev, derr := decodeRevealAnnouncement(payload, g.reg.IndexBound(), g.verify)
		if derr != nil {
			g.t.Fatalf("inject reveal decode (auth) unexpectedly failed: %v", derr)
		}
		g.coords[j].OnPeerReveal(gbh, grep, grev)
	}
}

func (g *wireGrid) drive(number, mergeDelay uint64) {
	for height := number; height <= number+mergeDelay; height++ {
		for _, c := range g.coords {
			c.OnBlockCommitted(height)
		}
	}
}

// TestCohortL2BReactiveBackfillDefeated is the STRONGER P0 scenario an audit
// surfaced: instead of naively replaying the public commitment during the
// commit phase, a malicious authenticated validator waits, HARVESTS the
// victim's raw device signature from an honest reveal, computes a valid
// SELF-bound commitment Keccak256(sig‖itsOwnAddr), and backfills a commit +
// reveal AFTER the reveal boundary. Without a commit cutoff this reaches the
// ban threshold with a genuinely-valid self-bound witness and censors the
// victim. The fix freezes the accepted commit set the moment this node reveals
// or sees any peer reveal, so the post-reveal backfill is rejected and never
// counted — the honest victim survives.
func TestCohortL2BReactiveBackfillDefeated(t *testing.T) {
	blockHash, number, root := h(1), uint64(1000), h(2)
	reg := NewRegistry()
	cfg := CohortConfig{IndexAnnounceDelay: 1, RevealDelay: 2, ReconcileDelay: 3, MergeDelay: 5}
	g := newWireGrid(t, reg, blockHash, number, cfg, 3)
	for _, c := range g.coords {
		c.SetBanThreshold(2) // f+1 = 2; one honest witness + one backfill would suffice if uncaught
	}
	const atkNode = 2

	// Honest filler devices so the finalized cert is non-trivial.
	for i := 0; i < 4; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		if _, err := g.coords[i%2].Submit(d.receipt(blockHash, number, root)); err != nil {
			t.Fatalf("submit filler %d: %v", i, err)
		}
	}
	// The victim: a single honest submission to node0 only.
	victim := newDevice(t)
	victimIdx := registerCommitted(t, reg, victim.pubkey, victim.pop())
	victimReceipt := victim.receipt(blockHash, number, root)
	if _, err := g.coords[0].Submit(victimReceipt); err != nil {
		t.Fatalf("victim submit: %v", err)
	}

	// Drive commit (N+1) and reveal (N+2). At N+2 node0 reveals — its reveal set
	// contains the victim's raw signature, and it freezes its commit set.
	for _, height := range []uint64{number, number + cfg.IndexAnnounceDelay, number + cfg.RevealDelay} {
		for _, c := range g.coords {
			c.OnBlockCommitted(height)
		}
	}

	// The attacker now has the victim's raw signature (harvested from the reveal
	// it observed on gossip; here taken directly) and backfills, AFTER the reveal
	// boundary, a valid self-bound commitment plus the matching reveal.
	harvested := victimReceipt.Signature
	attackerBound := ReporterBoundCommitment(harvested, g.addrs[atkNode])
	g.injectIndex(atkNode, []int{0, 1}, blockHash, number,
		[]IndexCommitment{{Index: victimIdx, Commitment: attackerBound}})
	g.injectReveal(atkNode, []int{0, 1}, blockHash, number,
		map[MobileIndex]RevealedSig{victimIdx: {Sig: harvested, Root: root}})

	// Finish reconcile (N+3) and merge (N+5).
	for _, height := range []uint64{number + cfg.ReconcileDelay, number + 4, number + cfg.MergeDelay} {
		for _, c := range g.coords {
			c.OnBlockCommitted(height)
		}
	}

	// The victim MUST survive: the post-reveal backfill was rejected by the
	// frozen commit set, so the victim has only its one honest witness (< f+1)
	// and is not banned.
	got := g.stores[0].Get(blockHash)
	if len(got) != 1 {
		t.Fatalf("node0: got %d certs, want 1", len(got))
	}
	signers, err := got[0].Verify(reg)
	if err != nil {
		t.Fatalf("cert verify: %v", err)
	}
	found := false
	for _, idx := range signers {
		if idx == victimIdx {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("victim device %d was CENSORED by a reactive post-reveal backfill — the commit cutoff failed", victimIdx)
	}
}

// TestCohortL2BReplayCensorshipOverWire is the P0 scenario end-to-end over the
// real wire: an authenticated but malicious validator tries to censor an
// honest device by replaying that device's PUBLIC commitment (which it can
// observe from the honest reporter's index announcement). Under the old
// bare-index / public-commitment rule this replay, combined with the victim's
// own honest announcement, would reach the ban threshold and censor the device.
// With L2B the replay is defeated: the malicious validator cannot produce a
// reveal that reproduces ITS reporter-bound commitment (it never held the raw
// device signature), so it is flagged as misbehaving and never counted — the
// honest device survives.
func TestCohortL2BReplayCensorshipOverWire(t *testing.T) {
	blockHash, number, root := h(1), uint64(1000), h(2)
	reg := NewRegistry()
	cfg := CohortConfig{IndexAnnounceDelay: 1, RevealDelay: 2, ReconcileDelay: 3, MergeDelay: 5}
	// 3 validators, threshold f+1 = 2. node2 is the malicious validator.
	g := newWireGrid(t, reg, blockHash, number, cfg, 3)
	for _, c := range g.coords {
		c.SetBanThreshold(2)
	}
	const honestNode, victimNode, atkNode = 0, 0, 2

	// A pool of honest devices submit to node0/node1 so the final cert is
	// non-trivial; the victim is one specific device that submits ONCE (to
	// node0 only) — a fully honest, non-duplicate submission.
	var others []*device
	for i := 0; i < 4; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		if _, err := g.coords[i%2].Submit(d.receipt(blockHash, number, root)); err != nil {
			t.Fatalf("submit other %d: %v", i, err)
		}
		others = append(others, d)
	}
	victim := newDevice(t)
	victimIdx := registerCommitted(t, reg, victim.pubkey, victim.pop())
	victimReceipt := victim.receipt(blockHash, number, root)
	if _, err := g.coords[victimNode].Submit(victimReceipt); err != nil {
		t.Fatalf("victim submit: %v", err)
	}

	// The public commitment node0 (honestNode) will broadcast for the victim is
	// reporter-bound to node0's address — this is exactly the value the
	// malicious validator can observe on the wire and tries to replay.
	victimPublicCommitment := ReporterBoundCommitment(victimReceipt.Signature, g.addrs[honestNode])

	// Advance to the index-announced phase so honest announcements are routed
	// over the wire (and node2 would have observed the victim's public
	// commitment).
	for _, c := range g.coords {
		c.OnBlockCommitted(number + cfg.IndexAnnounceDelay)
	}
	// The malicious validator replays the victim's public commitment as its
	// OWN index announcement (reporter=node2), authenticated with node2's real
	// validator key — L1 auth passes (node2 IS a validator); L2B must still
	// defeat it. Delivered to the honest reconcilers.
	g.injectIndex(atkNode, []int{0, 1}, blockHash, number,
		[]IndexCommitment{{Index: victimIdx, Commitment: victimPublicCommitment}})

	// Finish the phases. node2 never reveals a raw signature for the victim
	// (it has none), so it is a "committed but not revealed" misbehavior.
	g.drive(number, cfg.MergeDelay)

	// The victim device MUST survive in node0's finalized certificate: it was
	// an honest single submission and the replay must not have censored it.
	got := g.stores[honestNode].Get(blockHash)
	if len(got) != 1 {
		t.Fatalf("node%d: got %d certs, want 1", honestNode, len(got))
	}
	signers, err := got[0].Verify(reg)
	if err != nil {
		t.Fatalf("cert verify: %v", err)
	}
	found := false
	for _, idx := range signers {
		if idx == victimIdx {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("victim device %d was CENSORED out of the final cert by the replay — L2B failed", victimIdx)
	}
	// And the malicious validator must have been flagged as misbehaving.
	if g.misb[g.addrs[atkNode]] == 0 {
		t.Fatalf("malicious validator %s replayed a commitment it could not prove but was NOT flagged", g.addrs[atkNode])
	}
	_ = others
}

// TestCohortL2BGenuineDuplicateBannedOverWire is the control: a device that
// genuinely reaches TWO honest validators (each holding its raw signature)
// over the real wire IS banned — proving L2B's proof-of-possession does not
// break the legitimate cross-node conflict exclusion. Both reporters can
// reveal a raw signature that reproduces their own reporter-bound commitment,
// so both count and the f+1 threshold is met.
func TestCohortL2BGenuineDuplicateBannedOverWire(t *testing.T) {
	blockHash, number, root := h(1), uint64(1000), h(2)
	reg := NewRegistry()
	cfg := CohortConfig{IndexAnnounceDelay: 1, RevealDelay: 2, ReconcileDelay: 3, MergeDelay: 5}
	g := newWireGrid(t, reg, blockHash, number, cfg, 3)
	for _, c := range g.coords {
		c.SetBanThreshold(2)
	}

	var honest []*device
	for i := 0; i < 4; i++ {
		d := newDevice(t)
		registerCommitted(t, reg, d.pubkey, d.pop())
		if _, err := g.coords[i%2].Submit(d.receipt(blockHash, number, root)); err != nil {
			t.Fatalf("submit honest %d: %v", i, err)
		}
		honest = append(honest, d)
	}
	// The protocol-violating device submits to BOTH node0 and node1.
	dup := newDevice(t)
	dupIdx := registerCommitted(t, reg, dup.pubkey, dup.pop())
	if _, err := g.coords[0].Submit(dup.receipt(blockHash, number, root)); err != nil {
		t.Fatal(err)
	}
	if _, err := g.coords[1].Submit(dup.receipt(blockHash, number, root)); err != nil {
		t.Fatal(err)
	}

	g.drive(number, cfg.MergeDelay)

	got := g.stores[0].Get(blockHash)
	if len(got) != 1 {
		t.Fatalf("node0: got %d certs, want 1", len(got))
	}
	signers, err := got[0].Verify(reg)
	if err != nil {
		t.Fatalf("cert verify: %v", err)
	}
	if len(signers) != len(honest) {
		t.Fatalf("node0 cert has %d signers, want %d (all honest present, dup excluded)", len(signers), len(honest))
	}
	for _, idx := range signers {
		if idx == dupIdx {
			t.Fatal("genuinely cross-node-duplicated device was NOT excluded — reconcile ban broke")
		}
	}
}
