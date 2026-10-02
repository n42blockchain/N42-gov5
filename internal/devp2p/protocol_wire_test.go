package devp2p

import (
	"testing"

	"github.com/ethereum/go-ethereum/rlp"
	"github.com/n42blockchain/N42/common/types"
)

func TestHashOrNumberEncodeDecodeHash(t *testing.T) {
	want := hashOrNumber{Hash: types.HexToHash("0x0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")}
	encoded, err := rlp.EncodeToBytes(&want)
	if err != nil {
		t.Fatal(err)
	}
	var got hashOrNumber
	if err := rlp.DecodeBytes(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Hash != want.Hash || got.Number != 0 {
		t.Fatalf("decoded = %+v, want %+v", got, want)
	}
}

func TestHashOrNumberEncodeDecodeNumber(t *testing.T) {
	want := hashOrNumber{Number: 123456789}
	encoded, err := rlp.EncodeToBytes(&want)
	if err != nil {
		t.Fatal(err)
	}
	var got hashOrNumber
	if err := rlp.DecodeBytes(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Number != want.Number || got.Hash != (types.Hash{}) {
		t.Fatalf("decoded = %+v, want %+v", got, want)
	}
}

func TestHashOrNumberEncodeRejectsBothSet(t *testing.T) {
	h := hashOrNumber{Hash: types.HexToHash("0x01"), Number: 1}
	if _, err := rlp.EncodeToBytes(&h); err == nil {
		t.Fatal("expected error when both hash and number are set")
	}
}

func TestHashOrNumberDecodeRejectsBadSize(t *testing.T) {
	// A raw RLP list element (neither a 33-byte hash string nor a <=9-byte
	// number string) must be rejected with "invalid origin size".
	bad, err := rlp.EncodeToBytes([]byte("this string is way too long to be a uint64 but not 33 bytes"))
	if err != nil {
		t.Fatal(err)
	}
	var h hashOrNumber
	if err := rlp.DecodeBytes(bad, &h); err == nil {
		t.Fatal("expected invalid origin size error")
	}
}
