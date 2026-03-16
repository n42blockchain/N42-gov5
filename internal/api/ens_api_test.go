package api

import (
	"bytes"
	"errors"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/ens"
)

func TestDecodeABIStringUsesDeclaredOffset(t *testing.T) {
	data := encodeABIDynamicForTest([]byte("abc"), 96)

	got, err := decodeABIString(data)
	if err != nil {
		t.Fatalf("decodeABIString() error = %v", err)
	}
	if got != "abc" {
		t.Fatalf("decodeABIString() = %q, want %q", got, "abc")
	}
}

func TestDecodeABIBytesSupportsLengthOver255(t *testing.T) {
	payload := bytes.Repeat([]byte{0xab}, 300)
	data := encodeABIDynamicForTest(payload, 32)

	got, err := decodeABIBytes(data)
	if err != nil {
		t.Fatalf("decodeABIBytes() error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("decodeABIBytes() len = %d, want %d", len(got), len(payload))
	}
}

func TestDecodeABIBytesRejectsTruncatedOffset(t *testing.T) {
	data := encodeABIDynamicForTest([]byte("abc"), 96)
	data = data[:64]

	_, err := decodeABIBytes(data)
	if !errors.Is(err, ens.ErrResolutionFailed) {
		t.Fatalf("decodeABIBytes() error = %v, want %v", err, ens.ErrResolutionFailed)
	}
}

func encodeABIDynamicForTest(payload []byte, offset uint64) []byte {
	total := int(offset) + 32 + len(payload)
	data := make([]byte, total)
	putUint256ForTest(data, 0, offset)
	putUint256ForTest(data, int(offset), uint64(len(payload)))
	copy(data[int(offset)+32:], payload)
	return data
}

func putUint256ForTest(dst []byte, pos int, value uint64) {
	encoded := new(big.Int).SetUint64(value).Bytes()
	copy(dst[pos+32-len(encoded):pos+32], encoded)
}
