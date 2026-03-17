package abi

import (
	"math/big"
	"strings"
	"testing"

	common "github.com/n42blockchain/N42/internal/avm/common"
)

func TestUnpackOffchainLookup(t *testing.T) {
	payload, err := offchainLookupError.Inputs.Pack(
		common.HexToAddress("0x1111111111111111111111111111111111111111"),
		[]string{
			"https://example.com/{sender}/{data}",
			"https://fallback.example.com/{sender}",
		},
		[]byte{0xde, 0xad, 0xbe, 0xef},
		[4]byte{0xca, 0xfe, 0xba, 0xbe},
		[]byte{0xaa, 0xbb},
	)
	if err != nil {
		t.Fatalf("Pack() error = %v", err)
	}
	raw := append(append([]byte{}, offchainLookupError.ID[:4]...), payload...)

	lookup, err := UnpackOffchainLookup(raw)
	if err != nil {
		t.Fatalf("UnpackOffchainLookup() error = %v", err)
	}
	if !IsOffchainLookup(raw) {
		t.Fatal("IsOffchainLookup() = false, want true")
	}
	if lookup.Sender != common.HexToAddress("0x1111111111111111111111111111111111111111") {
		t.Fatalf("Sender = %s", lookup.Sender.Hex())
	}
	if len(lookup.URLs) != 2 || !strings.Contains(lookup.URLs[0], "{sender}") {
		t.Fatalf("URLs = %#v", lookup.URLs)
	}
	if got := string(lookup.CallData); got != string([]byte{0xde, 0xad, 0xbe, 0xef}) {
		t.Fatalf("CallData = %x", lookup.CallData)
	}
	if lookup.CallbackFunction != [4]byte{0xca, 0xfe, 0xba, 0xbe} {
		t.Fatalf("CallbackFunction = %x", lookup.CallbackFunction)
	}
	if got := string(lookup.ExtraData); got != string([]byte{0xaa, 0xbb}) {
		t.Fatalf("ExtraData = %x", lookup.ExtraData)
	}
}

func TestABIErrorByID(t *testing.T) {
	parsed, err := JSON(strings.NewReader(`[{"type":"error","name":"OffchainLookup","inputs":[{"name":"sender","type":"address"},{"name":"urls","type":"string[]"},{"name":"callData","type":"bytes"},{"name":"callbackFunction","type":"bytes4"},{"name":"extraData","type":"bytes"}]}]`))
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	lookupErr := parsed.Errors["OffchainLookup"]
	var id [4]byte
	copy(id[:], lookupErr.ID[:4])

	errABI, err := parsed.ErrorByID(id)
	if err != nil {
		t.Fatalf("ErrorByID() error = %v", err)
	}
	if errABI.Name != "OffchainLookup" {
		t.Fatalf("ErrorByID() name = %s", errABI.Name)
	}
}

func TestUnpackRevertPanicReason(t *testing.T) {
	args := Arguments{{Type: mustNewType("uint256")}}
	payload, err := args.Pack(big.NewInt(0x12))
	if err != nil {
		t.Fatalf("Pack() error = %v", err)
	}
	raw := append(panicSelector, payload...)

	reason, err := UnpackRevert(raw)
	if err != nil {
		t.Fatalf("UnpackRevert() error = %v", err)
	}
	if reason != "division or modulo by zero" {
		t.Fatalf("UnpackRevert() = %q", reason)
	}
}
