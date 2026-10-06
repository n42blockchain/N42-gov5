// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// eth_beacon_fetcher_cov_test.go covers the pure JSON-parsing helpers used
// by BeaconFetcher (parseBeaconFinalityUpdate, parseBeaconHeaderToEthHeader,
// decodeHex) and NewBeaconFetcher/NewETHSubmitter construction — no real
// HTTP requests are made.

package bridge

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestDecodeHex(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"0x1a2b", "\x1a\x2b"},
		{"1a2b", "\x1a\x2b"},
		{"0xa", "\x0a"}, // odd-length -> left-padded
		{"", ""},
	}
	for _, c := range cases {
		got, err := decodeHex(c.in)
		if err != nil {
			t.Fatalf("decodeHex(%q): %v", c.in, err)
		}
		if string(got) != c.want {
			t.Fatalf("decodeHex(%q) = %x, want %x", c.in, got, c.want)
		}
	}
}

func TestDecodeHex_Invalid(t *testing.T) {
	if _, err := decodeHex("0xzz"); err == nil {
		t.Fatal("expected error for non-hex input")
	}
}

func TestParseBeaconHeaderToEthHeader(t *testing.T) {
	var bh beaconHeader
	bh.Beacon.Slot = "100"
	bh.Beacon.ProposerIndex = "7"
	bh.Beacon.ParentRoot = "0x01"
	bh.Beacon.StateRoot = "0x02"
	bh.Beacon.BodyRoot = "0x03"

	h, err := parseBeaconHeaderToEthHeader(bh)
	if err != nil {
		t.Fatalf("parseBeaconHeaderToEthHeader: %v", err)
	}
	if h.Slot != 100 || h.ProposerIndex != 7 {
		t.Fatalf("got slot=%d proposerIndex=%d", h.Slot, h.ProposerIndex)
	}
}

func TestParseBeaconHeaderToEthHeader_BadSlot(t *testing.T) {
	var bh beaconHeader
	bh.Beacon.Slot = "not-a-number"
	if _, err := parseBeaconHeaderToEthHeader(bh); err == nil {
		t.Fatal("expected error for non-numeric slot")
	}
}

func TestParseBeaconHeaderToEthHeader_BadProposerIndex(t *testing.T) {
	var bh beaconHeader
	bh.Beacon.Slot = "1"
	bh.Beacon.ProposerIndex = "not-a-number"
	if _, err := parseBeaconHeaderToEthHeader(bh); err == nil {
		t.Fatal("expected error for non-numeric proposer index")
	}
}

func TestParseBeaconFinalityUpdate_InvalidJSON(t *testing.T) {
	if _, err := parseBeaconFinalityUpdate([]byte("not json")); err == nil {
		t.Fatal("expected JSON parse error")
	}
}

func TestParseBeaconFinalityUpdate_Success(t *testing.T) {
	body := []byte(`{
		"data": {
			"attested_header": {"beacon": {"slot": "10", "proposer_index": "1", "parent_root": "0x01", "state_root": "0x02", "body_root": "0x03"}},
			"finalized_header": {"beacon": {"slot": "9", "proposer_index": "2", "parent_root": "0x04", "state_root": "0x05", "body_root": "0x06"}},
			"finality_branch": ["0x0a", "0x0b"],
			"sync_aggregate": {"sync_committee_bits": "0xff", "sync_committee_signature": "0xaabbcc"}
		}
	}`)

	update, err := parseBeaconFinalityUpdate(body)
	if err != nil {
		t.Fatalf("parseBeaconFinalityUpdate: %v", err)
	}
	if update.AttestedHeader.Slot != 10 {
		t.Fatalf("AttestedHeader.Slot = %d, want 10", update.AttestedHeader.Slot)
	}
	if update.FinalizedHeader.Slot != 9 {
		t.Fatalf("FinalizedHeader.Slot = %d, want 9", update.FinalizedHeader.Slot)
	}
	if len(update.FinalityBranch) != 2 {
		t.Fatalf("FinalityBranch len = %d, want 2", len(update.FinalityBranch))
	}
	if update.FinalityBranch[0] != types.HexToHash("0x0a") {
		t.Fatalf("FinalityBranch[0] = %x", update.FinalityBranch[0])
	}
	if len(update.SyncAggregate.SyncCommitteeSignature) != 3 {
		t.Fatalf("signature bytes = %d, want 3", len(update.SyncAggregate.SyncCommitteeSignature))
	}
}

func TestParseBeaconFinalityUpdate_BadAttestedHeader(t *testing.T) {
	body := []byte(`{"data": {"attested_header": {"beacon": {"slot": "bad"}}}}`)
	if _, err := parseBeaconFinalityUpdate(body); err == nil {
		t.Fatal("expected error for bad attested header slot")
	}
}

func TestParseBeaconFinalityUpdate_BadSignatureHex(t *testing.T) {
	body := []byte(`{
		"data": {
			"attested_header": {"beacon": {"slot": "10", "proposer_index": "1"}},
			"finalized_header": {"beacon": {"slot": "9", "proposer_index": "2"}},
			"sync_aggregate": {"sync_committee_bits": "0xff", "sync_committee_signature": "0xzz"}
		}
	}`)
	if _, err := parseBeaconFinalityUpdate(body); err == nil {
		t.Fatal("expected error for bad signature hex")
	}
}

func TestNewBeaconFetcher_DefaultsPollInterval(t *testing.T) {
	f := NewBeaconFetcher("http://127.0.0.1:0", nil, 0)
	if f.pollInterval != 12*time.Second {
		t.Fatalf("pollInterval = %v, want default 12s", f.pollInterval)
	}

	f2 := NewBeaconFetcher("http://127.0.0.1:0", nil, 3*time.Second)
	if f2.pollInterval != 3*time.Second {
		t.Fatalf("pollInterval = %v, want 3s", f2.pollInterval)
	}
}

func TestNewETHSubmitter(t *testing.T) {
	verifierAddr := types.HexToAddress("0x4000000000000000000000000000000000000009")
	from := types.HexToAddress("0x5000000000000000000000000000000000000009")

	s, err := NewETHSubmitter("http://127.0.0.1:0", verifierAddr, from)
	if err != nil {
		t.Fatalf("NewETHSubmitter: %v", err)
	}
	defer s.Close()
	if s.verifierAddr != verifierAddr {
		t.Fatalf("verifierAddr = %s, want %s", s.verifierAddr, verifierAddr)
	}
}

func TestNewETHSubmitter_InvalidEndpoint(t *testing.T) {
	verifierAddr := types.HexToAddress("0x4000000000000000000000000000000000000009")
	from := types.HexToAddress("0x5000000000000000000000000000000000000009")
	if _, err := NewETHSubmitter("://bad-url", verifierAddr, from); err == nil {
		t.Fatal("expected error for invalid endpoint")
	}
}

func TestETHSubmitter_SubmitHeaderChainProof_Validation(t *testing.T) {
	verifierAddr := types.HexToAddress("0x4000000000000000000000000000000000000009")
	from := types.HexToAddress("0x5000000000000000000000000000000000000009")
	s, err := NewETHSubmitter("http://127.0.0.1:0", verifierAddr, from)
	if err != nil {
		t.Fatalf("NewETHSubmitter: %v", err)
	}
	defer s.Close()

	if err := s.SubmitHeaderChainProof(nil, nil); err == nil {
		t.Fatal("expected error for nil proof")
	}
	if err := s.SubmitHeaderChainProof(nil, &HeaderChainProof{}); err == nil {
		t.Fatal("expected error for proof without ZK proof data")
	}
}
