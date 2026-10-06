// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

func sampleEvidence(withMobile bool) *ConsensusEvidence {
	ce := &ConsensusEvidence{
		View:        7,
		SignerCount: 10,
	}
	ce.BlockHash[0] = 0xAA
	ce.AggregateSignature[0] = 0xBB
	ce.SignersPacked = []byte{0xFF, 0x03} // ceil(10/8) = 2 bytes
	if withMobile {
		ce.HasMobile = true
		ce.MobReceiptsRoot[0] = 0xCC
		ce.MobAggSignature[0] = 0xDD
		ce.MobParticipantCount = 3
		ce.MobParticipantsPacked = []byte{0x07} // ceil(3/8) = 1 byte
		ce.MobCreatedAtMs = 1234567
	}
	return ce
}

// TestConsensusEvidenceMarshalRoundTrip covers both the plain and the
// mobile-BLS-extended wire layouts.
func TestConsensusEvidenceMarshalRoundTrip(t *testing.T) {
	for _, withMobile := range []bool{false, true} {
		ce := sampleEvidence(withMobile)
		data := ce.Marshal()

		got := &ConsensusEvidence{}
		if err := got.Unmarshal(data); err != nil {
			t.Fatalf("Unmarshal (mobile=%v): %v", withMobile, err)
		}
		if got.View != ce.View || got.SignerCount != ce.SignerCount || got.HasMobile != ce.HasMobile {
			t.Fatalf("round trip mismatch (mobile=%v): %+v vs %+v", withMobile, got, ce)
		}
		if got.BlockHash != ce.BlockHash || got.AggregateSignature != ce.AggregateSignature {
			t.Fatalf("hash/signature mismatch (mobile=%v)", withMobile)
		}
		if withMobile {
			if got.MobReceiptsRoot != ce.MobReceiptsRoot || got.MobAggSignature != ce.MobAggSignature {
				t.Fatalf("mobile fields mismatch")
			}
			if got.MobParticipantCount != ce.MobParticipantCount || got.MobCreatedAtMs != ce.MobCreatedAtMs {
				t.Fatalf("mobile count/time mismatch")
			}
		}
	}
}

func TestConsensusEvidenceUnmarshalErrors(t *testing.T) {
	ce := &ConsensusEvidence{}
	if err := ce.Unmarshal(make([]byte, 10)); err == nil {
		t.Fatal("expected error for too-short payload")
	}

	// Valid header but truncated before signers.
	full := sampleEvidence(false).Marshal()
	if err := ce.Unmarshal(full[:8+32+96+2]); err == nil {
		t.Fatal("expected error for truncated signers section")
	}

	// Valid non-mobile header+signers but HasMobile claims extra data that
	// isn't present.
	base := sampleEvidence(false)
	data := base.Marshal()
	data[len(data)-1] = 1 // force HasMobile=1 with no trailing mobile bytes
	if err := ce.Unmarshal(data); err == nil {
		t.Fatal("expected error for truncated mobile section")
	}
}

func TestConsensusEvidenceHashAndBeaconRoot(t *testing.T) {
	ce := sampleEvidence(true)
	h1 := ce.Hash()
	h2 := ce.Hash()
	if h1 != h2 {
		t.Fatal("Hash is not deterministic")
	}
	b1 := ce.BeaconRoot()
	b2 := ce.BeaconRoot()
	if b1 != b2 {
		t.Fatal("BeaconRoot is not deterministic")
	}
	if h1 == b1 {
		t.Fatal("Hash (keccak256) and BeaconRoot (blake3) unexpectedly collided")
	}
}

func TestWriteReadConsensusEvidence(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	// Missing entries read back nil, no error.
	got, err := ReadConsensusEvidence(tx, 5)
	if err != nil {
		t.Fatalf("read missing: %v", err)
	}
	if got != nil {
		t.Fatal("expected nil for missing evidence")
	}

	ce := sampleEvidence(true)
	if err := WriteConsensusEvidence(tx, 5, ce); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err = ReadConsensusEvidence(tx, 5)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got == nil || got.View != ce.View || got.MobCreatedAtMs != ce.MobCreatedAtMs {
		t.Fatalf("read-back mismatch: %+v", got)
	}
}

func TestParentBeaconRoot(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	// Genesis/block 1 always returns the zero hash.
	if r := ParentBeaconRoot(tx, 0); r != (types.Hash{}) {
		t.Fatal("ParentBeaconRoot(0) should be zero hash")
	}
	if r := ParentBeaconRoot(tx, 1); r != (types.Hash{}) {
		t.Fatal("ParentBeaconRoot(1) should be zero hash")
	}

	// No parent evidence stored -> zero hash.
	if r := ParentBeaconRoot(tx, 10); r != (types.Hash{}) {
		t.Fatal("ParentBeaconRoot with no parent evidence should be zero hash")
	}

	parent := sampleEvidence(false)
	if err := WriteConsensusEvidence(tx, 9, parent); err != nil {
		t.Fatalf("write parent evidence: %v", err)
	}
	want := parent.BeaconRoot()
	if got := ParentBeaconRoot(tx, 10); got != want {
		t.Fatalf("ParentBeaconRoot(10) = %x, want %x", got, want)
	}
}
