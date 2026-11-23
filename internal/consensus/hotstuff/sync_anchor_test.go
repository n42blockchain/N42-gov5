// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"encoding/hex"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/syncanchor"
)

// syncAnchorService builds a Service around a real engine for setup's set.
func syncAnchorService(t *testing.T, setup *testSetup) (*Service, *ConsensusEngine) {
	t.Helper()
	h := New(nil, nil)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	return &Service{engine: h}, h.Engine()
}

// signedQC builds a QC over msg signed by the first quorum of setup's keys,
// with a bitmap sized to setup's set.
func signedQC(t *testing.T, setup *testSetup, view ViewNumber, hash types.Hash, msg []byte) *QuorumCertificate {
	t.Helper()
	vc := NewVoteCollector(view, hash, setup.vs.Len())
	for i := 0; i < setup.vs.QuorumSize(); i++ {
		if err := vc.AddVote(ValidatorIndex(i), setup.keys[i].Sign(msg)); err != nil {
			t.Fatal(err)
		}
	}
	qc, err := vc.BuildQCWithMessage(setup.vs, msg)
	if err != nil {
		t.Fatal(err)
	}
	return qc
}

func headerExtraFor(t *testing.T, view ViewNumber, qc *QuorumCertificate) []byte {
	t.Helper()
	extra, err := buildHeaderExtra(view, qc)
	if err != nil {
		t.Fatal(err)
	}
	return extra
}

func TestSyncAnchorVerifierDomains(t *testing.T) {
	setup := newTestSetup(t, 4)
	s, ce := syncAnchorService(t, setup)
	hash := types.HexToHash("0xabc1")
	const view ViewNumber = 7

	commit := signedQC(t, setup, view, hash, ce.commitSigningMessage(view, hash))
	extra := headerExtraFor(t, view+2, commit)
	if r := s.DecodeHeaderQC(extra); r.Verdict != syncanchor.Unverified || r.BlockHash != hash || r.View != uint64(view) {
		t.Fatalf("decode = %+v", r)
	}
	r := s.VerifyHeaderQC(extra)
	if r.Verdict != syncanchor.Verified || r.BlockHash != hash || r.Cached {
		t.Fatalf("commit QC: %+v", r)
	}
	if r := s.VerifyHeaderQC(extra); r.Verdict != syncanchor.Verified || !r.Cached {
		t.Fatalf("second verification not cached: %+v", r)
	}

	prepare := signedQC(t, setup, view, hash, ce.voteSigningMessage(view, hash))
	if r := s.VerifyHeaderQC(headerExtraFor(t, view+2, prepare)); r.Verdict != syncanchor.PrepareDomain {
		t.Fatalf("prepare-domain QC must not anchor: %+v", r)
	}

	other := newTestSetup(t, 4) // an equal-size set that is not this chain's
	forged := signedQC(t, other, view, hash, ce.commitSigningMessage(view, hash))
	if r := s.VerifyHeaderQC(headerExtraFor(t, view+2, forged)); r.Verdict != syncanchor.BadQC {
		t.Fatalf("QC of a foreign set: %+v", r)
	}

	for _, tc := range []struct {
		name  string
		extra []byte
		want  syncanchor.Verdict
	}{
		{"no magic", []byte("plain replay block"), syncanchor.NonConsensus},
		{"magic only", []byte("N42H"), syncanchor.BadExtra},
		{"no qc", headerExtraFor(t, view, nil), syncanchor.NoQC},
		{"garbage qc", append(append([]byte{}, headerExtraFor(t, view, nil)[:extraMinLen]...), 1, 2, 3), syncanchor.BadExtra},
	} {
		if r := s.VerifyHeaderQC(tc.extra); r.Verdict != tc.want {
			t.Errorf("%s: verdict %v, want %v", tc.name, r.Verdict, tc.want)
		}
	}
}

// An H2-v4 chain anchors only on the H2-v4 commit domain.
func TestSyncAnchorVerifierH2V4(t *testing.T) {
	setup := newTestSetup(t, 4)
	s, ce := syncAnchorService(t, setup)
	id := H2V4ChainIdentity{ChainID: 1143, GenesisHash: types.HexToHash("0x42")}
	ce.EnableH2V4(id)
	hash := types.HexToHash("0xbeef")
	const view ViewNumber = 11

	v4 := signedQC(t, setup, view, hash, H2V4CommitSigningMessage(id, view, hash, types.Hash{}))
	if r := s.VerifyHeaderQC(headerExtraFor(t, view+1, v4)); r.Verdict != syncanchor.Verified {
		t.Fatalf("H2-v4 commit QC: %+v", r)
	}
	legacy := signedQC(t, setup, view, hash, CommitSigningMessage(view, hash))
	if r := s.VerifyHeaderQC(headerExtraFor(t, view+1, legacy)); r.Verdict != syncanchor.BadQC {
		t.Fatalf("legacy-domain QC on an H2-v4 chain: %+v", r)
	}
	nonZero := signedQC(t, setup, view, hash, H2V4CommitSigningMessage(id, view, hash, types.HexToHash("0x01")))
	if r := s.VerifyHeaderQC(headerExtraFor(t, view+1, nonZero)); r.Verdict == syncanchor.Verified {
		t.Fatal("a non-zero changesHash QC must fail closed")
	}
}

// Each QC is checked against its own epoch's set: no same-size fallback, no
// current-set fallback, unknown epochs fail closed.
func TestSyncAnchorVerifierEpochs(t *testing.T) {
	old := newTestSetup(t, 4)
	cur := newTestSetup(t, 4)
	s, ce := syncAnchorService(t, cur)
	em := NewEpochManagerFromEpoch(cur.vs, 10, 2) // epoch 2 = views 21..30
	em.historicalSets[1] = old.vs
	ce.mu.Lock()
	ce.epochManager = em
	ce.mu.Unlock()
	hash := types.HexToHash("0xcafe")

	// Epoch 1 (views 11..20) signed by its own set: an anchor.
	const v1 ViewNumber = 15
	if r := s.VerifyHeaderQC(headerExtraFor(t, v1+1, signedQC(t, old, v1, hash, ce.commitSigningMessage(v1, hash)))); r.Verdict != syncanchor.Verified || r.Epoch != 1 {
		t.Fatalf("epoch-1 QC by the epoch-1 set: %+v", r)
	}
	// Epoch 1 signed by the (equal-size) current set: refused, no fallback.
	if r := s.VerifyHeaderQC(headerExtraFor(t, v1+1, signedQC(t, cur, v1, hash, ce.commitSigningMessage(v1, hash)))); r.Verdict != syncanchor.BadQC {
		t.Fatalf("epoch-1 QC by the current set: %+v", r)
	}
	// Epoch 2 (current) by the current set.
	const v2 ViewNumber = 25
	if r := s.VerifyHeaderQC(headerExtraFor(t, v2+1, signedQC(t, cur, v2, hash, ce.commitSigningMessage(v2, hash)))); r.Verdict != syncanchor.Verified {
		t.Fatalf("current-epoch QC: %+v", r)
	}
	// Epoch 0 and epoch 5 have no known set: fail closed, not cached.
	for _, v := range []ViewNumber{5, 55} {
		extra := headerExtraFor(t, v+1, signedQC(t, cur, v, hash, ce.commitSigningMessage(v, hash)))
		if r := s.VerifyHeaderQC(extra); r.Verdict != syncanchor.UnknownEpoch {
			t.Fatalf("view %d: %+v", v, r)
		}
	}

	// The epoch schedule is the next set source.
	entry := EpochScheduleEntry{Epoch: 5}
	for i, vi := range cur.validators {
		entry.Validators = append(entry.Validators, EpochScheduleValidator{
			Address: vi.Address.Hex(),
			BLSKey:  "0x" + hex.EncodeToString(cur.pubKeys[i].Marshal()),
		})
	}
	s.epochSchedule = &EpochSchedule{Entries: []EpochScheduleEntry{entry}}
	const v5 ViewNumber = 55
	if r := s.VerifyHeaderQC(headerExtraFor(t, v5+1, signedQC(t, cur, v5, hash, ce.commitSigningMessage(v5, hash)))); r.Verdict != syncanchor.Verified || r.Epoch != 5 {
		t.Fatalf("epoch 5 from the schedule: %+v", r)
	}
}

func TestStrictSetForEpoch(t *testing.T) {
	a := newTestSetup(t, 4)
	b := newTestSetup(t, 7)
	single := NewEpochManager(a.vs)
	if vs, ok := single.StrictSetForEpoch(0); !ok || vs != a.vs {
		t.Fatal("single-epoch chain: epoch 0 is the active set")
	}
	if _, ok := single.StrictSetForEpoch(1); ok {
		t.Fatal("single-epoch chain has no epoch 1")
	}
	em := NewEpochManagerWithLength(a.vs, 100)
	em.StageNextEpoch(b.validators, b.f)
	if vs, ok := em.StrictSetForEpoch(1); !ok || vs.Len() != 7 {
		t.Fatal("staged next epoch must resolve")
	}
	if _, ok := em.StrictSetForEpoch(2); ok {
		t.Fatal("epoch 2 is unknown")
	}
	if em.FindValidatorSetByLen(4) == nil {
		t.Fatal("precondition: the size fallback would have matched")
	}
	var nilEM *EpochManager
	if _, ok := nilEM.StrictSetForEpoch(0); ok {
		t.Fatal("nil manager")
	}
}

// BenchmarkSyncAnchorVerifyHeaderQC measures one uncached strict header-QC
// verification (decode, set lookup, BLS aggregate check) on a 7-validator
// set: the cost the S1 gate pays per honest range.
func BenchmarkSyncAnchorVerifyHeaderQC(b *testing.B) {
	t := &testing.T{}
	setup := newTestSetup(t, 7)
	s, ce := syncAnchorService(t, setup)
	hash := types.HexToHash("0x77")
	const view ViewNumber = 9
	extra := headerExtraFor(t, view+2, signedQC(t, setup, view, hash, ce.commitSigningMessage(view, hash)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.syncQCs = syncQCCache{}
		if r := s.VerifyHeaderQC(extra); r.Verdict != syncanchor.Verified {
			b.Fatal(r.Verdict)
		}
	}
}
