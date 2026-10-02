package abi

import (
	"bytes"
	"math/big"
	"testing"
)

func TestPackOffchainLookupResponse(t *testing.T) {
	response := []byte{0x01, 0x02, 0x03}
	extraData := []byte{0xAA, 0xBB}

	packed, err := PackOffchainLookupResponse(response, extraData)
	if err != nil {
		t.Fatalf("PackOffchainLookupResponse() error: %v", err)
	}
	if len(packed) == 0 {
		t.Fatal("PackOffchainLookupResponse() returned empty output")
	}

	unpacked, err := callbackResponseArgs.Unpack(packed)
	if err != nil {
		t.Fatalf("Unpack() error: %v", err)
	}
	if len(unpacked) != 2 {
		t.Fatalf("Unpack() length = %d, want 2", len(unpacked))
	}
	gotResponse, ok := unpacked[0].([]byte)
	if !ok || !bytes.Equal(gotResponse, response) {
		t.Errorf("unpacked response = %v, want %v", unpacked[0], response)
	}
	gotExtra, ok := unpacked[1].([]byte)
	if !ok || !bytes.Equal(gotExtra, extraData) {
		t.Errorf("unpacked extraData = %v, want %v", unpacked[1], extraData)
	}
}

func TestUnpackRevertErrorString(t *testing.T) {
	args := Arguments{{Type: mustNewType("string")}}
	payload, err := args.Pack("revert reason")
	if err != nil {
		t.Fatalf("Pack() error: %v", err)
	}
	raw := append(append([]byte{}, revertSelector...), payload...)

	reason, err := UnpackRevert(raw)
	if err != nil {
		t.Fatalf("UnpackRevert() error: %v", err)
	}
	if reason != "revert reason" {
		t.Errorf("UnpackRevert() = %q, want %q", reason, "revert reason")
	}
}

func TestUnpackRevertUnknownPanicCode(t *testing.T) {
	args := Arguments{{Type: mustNewType("uint256")}}
	payload, err := args.Pack(big.NewInt(0x99))
	if err != nil {
		t.Fatalf("Pack() error: %v", err)
	}
	raw := append(append([]byte{}, panicSelector...), payload...)

	reason, err := UnpackRevert(raw)
	if err != nil {
		t.Fatalf("UnpackRevert() error: %v", err)
	}
	if reason != "unknown panic code: 0x99" {
		t.Errorf("UnpackRevert() = %q, want it to report the unknown code", reason)
	}
}

func TestUnpackRevertErrors(t *testing.T) {
	if _, err := UnpackRevert([]byte{0x01}); err == nil {
		t.Error("expected error for too-short data")
	}
	if _, err := UnpackRevert([]byte{0xDE, 0xAD, 0xBE, 0xEF}); err == nil {
		t.Error("expected error for an unrecognized selector")
	}
}
