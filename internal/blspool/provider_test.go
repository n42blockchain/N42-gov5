// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package blspool

import (
	"bytes"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type rawdbCE = rawdb.ConsensusEvidence

func timeNow() time.Time                  { return time.Now() }
func timeSince(t time.Time) time.Duration { return time.Since(t) }

func testPool(t *testing.T) *Pool {
	t.Helper()
	var seed [32]byte
	seed[0], seed[31] = 0x11, 0x42
	p, err := NewSimulatedPool(PoolConfig{Seed: seed, PoolSize: 2048, CommitteeSize: 64, RampBlocks: 100})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPoolAllSimulatedCE: with no replacements, BuildCE must verify against the
// pool's own keys, cover the full committee, and be DETERMINISTIC (same inputs,
// same CE bytes) — the property that lets a live node continue a resealed chain.
func TestPoolAllSimulatedCE(t *testing.T) {
	p := testPool(t)
	var bh, rr types.Hash
	bh[0], rr[0] = 0xaa, 0xbb

	ce1, missing, err := p.BuildCE(7, bh, rr, nil)
	if err != nil || len(missing) != 0 {
		t.Fatalf("build: err=%v missing=%v", err, missing)
	}
	covered, ok, err := p.VerifyCE(ce1)
	if err != nil || !ok {
		t.Fatalf("verify: ok=%v err=%v", ok, err)
	}
	if covered != 64 {
		t.Fatalf("covered=%d want full committee", covered)
	}
	ce2, _, _ := p.BuildCE(7, bh, rr, nil)
	if !bytes.Equal(ce1.Marshal(), ce2.Marshal()) {
		t.Fatal("CE not deterministic")
	}
	// Tampered evidence must fail.
	bad := *ce1
	bad.AggregateSignature[5] ^= 0xff
	if _, ok, _ := p.VerifyCE(&bad); ok {
		t.Fatal("tampered aggregate verified")
	}
}

// TestPoolHandover: registering a real validator removes the simulated key for
// that slot. BuildCE without the real signature excludes the member (reported
// missing, bitmap gap) yet still verifies over the remaining signers; supplying
// the real signature restores full coverage — a mixed simulated+real aggregate
// verified against mixed public keys.
func TestPoolHandover(t *testing.T) {
	p := testPool(t)
	var bh, rr types.Hash
	bh[0] = 0xcc

	members := append([]int(nil), p.CommitteeAt(9, bh)...)
	taken := members[3] // a slot that IS in this committee

	realKey, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Register(taken, realKey.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if !p.Replaced(taken) || p.ReplacedCount() != 1 {
		t.Fatal("replacement not recorded")
	}

	// Without the real signature: member is missing, CE verifies over the rest.
	ce, missing, err := p.BuildCE(9, bh, rr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 1 || missing[0] != taken {
		t.Fatalf("missing=%v want [%d]", missing, taken)
	}
	covered, ok, err := p.VerifyCE(ce)
	if err != nil || !ok {
		t.Fatalf("partial CE should verify: ok=%v err=%v", ok, err)
	}
	if covered != len(members)-1 {
		t.Fatalf("covered=%d want %d", covered, len(members)-1)
	}

	// With the real validator's signature: full coverage again.
	msg := SigningMessage(9, bh)
	ceFull, missing2, err := p.BuildCE(9, bh, rr, map[int]common.Signature{taken: realKey.Sign(msg)})
	if err != nil || len(missing2) != 0 {
		t.Fatalf("full build: err=%v missing=%v", err, missing2)
	}
	covered, ok, err = p.VerifyCE(ceFull)
	if err != nil || !ok || covered != len(members) {
		t.Fatalf("mixed CE verify: covered=%d ok=%v err=%v", covered, ok, err)
	}

	// A WRONG real signature must not verify even with the bit set.
	wrongKey, _ := bls.RandKey()
	ceBad, _, err := p.BuildCE(9, bh, rr, map[int]common.Signature{taken: wrongKey.Sign(msg)})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := p.VerifyCE(ceBad); ok {
		t.Fatal("CE with forged member signature verified")
	}
}

// TestPoolSigningMessageMatchesHotstuff is asserted in the replay package test
// (which can import hotstuff without a cycle); here we pin the byte layout.
func TestPoolSigningMessageLayout(t *testing.T) {
	var h types.Hash
	h[0], h[31] = 0xde, 0xad
	msg := SigningMessage(0x0102030405060708, h)
	if len(msg) != 40 {
		t.Fatalf("len=%d", len(msg))
	}
	if msg[0] != 0x08 || msg[7] != 0x01 { // little-endian view
		t.Fatal("view encoding wrong")
	}
	if msg[8] != 0xde || msg[39] != 0xad {
		t.Fatal("hash placement wrong")
	}
}

// TestAssembleCEArchitecture: per the N42 architecture, the CE's QC section
// carries the IDC HotStuff validators' certificate while the mobile section
// carries the scalable phone-verifier attestation. AssembleCE must place each
// layer in its section, and the pool must verify its own mobile aggregate.
func TestAssembleCEArchitecture(t *testing.T) {
	p := testPool(t)
	var bh, rr types.Hash
	bh[0], rr[0] = 0xee, 0xef

	// Mobile layer: pool attestation.
	ma, err := p.BuildMobileAttestation(11, bh, nil)
	if err != nil || len(ma.Missing) != 0 {
		t.Fatalf("mobile attestation: %v missing=%v", err, ma.Missing)
	}

	// IDC layer: a small HotStuff validator QC (3-of-4 signed).
	idcKeys := make([]common.SecretKey, 4)
	qcMsg := SigningMessage(11, bh)
	var qcSigs []common.Signature
	signers := []bool{true, true, false, true}
	for i := range idcKeys {
		idcKeys[i], _ = bls.RandKey()
		if signers[i] {
			qcSigs = append(qcSigs, idcKeys[i].Sign(qcMsg))
		}
	}
	qcAgg := bls.AggregateSignatures(qcSigs)

	ce := AssembleCE(11, bh, qcAgg.Marshal(), signers, rr, ma)
	if ce.SignerCount != 4 || ce.SignersPacked[0] != 0b1011 {
		t.Fatalf("QC section wrong: count=%d bitmap=%08b", ce.SignerCount, ce.SignersPacked[0])
	}
	if !ce.HasMobile || ce.MobReceiptsRoot != rr || int(ce.MobParticipantCount) != 64 {
		t.Fatal("mobile section wrong")
	}
	if !bytes.Equal(ce.MobAggSignature[:], ma.AggSig[:]) {
		t.Fatal("mobile aggregate not carried")
	}
	// CE round-trips through its storage codec.
	var rt rawdbCE
	if err := rt.Unmarshal(ce.Marshal()); err != nil {
		t.Fatalf("CE codec round-trip: %v", err)
	}
	// The QC aggregate verifies against the signing IDC keys.
	pubs := []common.PublicKey{idcKeys[0].PublicKey(), idcKeys[1].PublicKey(), idcKeys[3].PublicKey()}
	aggSig, _ := bls.SignatureFromBytes(ce.AggregateSignature[:])
	if !aggSig.Verify(bls.AggregateMultiplePubkeys(pubs), qcMsg) {
		t.Fatal("IDC QC aggregate failed to verify")
	}
}

// TestPoolMillionScale: the architecture targets ETH-scale verifier counts
// (1M+). Derivation is a one-time startup cost; committee sampling and signing
// stay O(committee) regardless of pool size.
func TestPoolMillionScale(t *testing.T) {
	if testing.Short() {
		t.Skip("1M-key derivation in -short mode")
	}
	var seed [32]byte
	seed[0] = 0x99
	start := timeNow()
	p, err := NewSimulatedPool(PoolConfig{Seed: seed, PoolSize: 1_000_000, CommitteeSize: 512, RampBlocks: 0})
	if err != nil {
		t.Fatal(err)
	}
	deriveDur := timeSince(start)

	var bh, rr types.Hash
	bh[0] = 0x77
	start = timeNow()
	ce, missing, err := p.BuildCE(123456, bh, rr, nil)
	buildDur := timeSince(start)
	if err != nil || len(missing) != 0 {
		t.Fatalf("build: %v", err)
	}
	covered, ok, err := p.VerifyCE(ce)
	if err != nil || !ok || covered != 512 {
		t.Fatalf("verify: covered=%d ok=%v err=%v", covered, ok, err)
	}
	t.Logf("1M pool: derive=%v buildCE=%v", deriveDur, buildDur)
	if buildDur.Milliseconds() > 200 {
		t.Fatalf("per-block CE build too slow at 1M pool: %v", buildDur)
	}
}
