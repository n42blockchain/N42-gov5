package encoder

import (
	"bytes"
	"encoding/hex"
	"testing"
)

type rawTestBytes struct{ data []byte }

func (r *rawTestBytes) MarshalSSZ() ([]byte, error)             { return r.data, nil }
func (r *rawTestBytes) MarshalSSZTo(buf []byte) ([]byte, error) { return append(buf, r.data...), nil }
func (r *rawTestBytes) SizeSSZ() int                            { return len(r.data) }
func (r *rawTestBytes) UnmarshalSSZ(buf []byte) error {
	r.data = append(r.data[:0], buf...)
	return nil
}

func TestBlockChunkLimitDoesNotWidenOrdinaryRPCs(t *testing.T) {
	payload := &rawTestBytes{data: make([]byte, MaxChunkSize+1)}
	if _, err := (SszNetworkEncoder{}).EncodeWithMaxLength(new(bytes.Buffer), payload); err == nil {
		t.Fatal("ordinary 1 MiB RPC accepted an oversized payload")
	}

	var encoded bytes.Buffer
	if _, err := EncodeWithMaxLengthLimit(&encoded, payload, MaxBlockChunkSize); err != nil {
		t.Fatalf("block-specific encoder rejected payload: %v", err)
	}
	decoded := &rawTestBytes{}
	if err := DecodeWithMaxLengthLimit(&encoded, decoded, MaxBlockChunkSize); err != nil {
		t.Fatalf("block-specific decoder failed: %v", err)
	}
	if !bytes.Equal(decoded.data, payload.data) {
		t.Fatal("block-specific round trip changed payload")
	}
}

func TestRustCrossClientDirectBlockWireFixture(t *testing.T) {
	var wire bytes.Buffer
	wire.WriteByte(0)
	wire.Write([]byte{0xde, 0xad, 0xbe, 0xef})
	if _, err := EncodeWithMaxLengthLimit(
		&wire,
		&rawTestBytes{data: []byte{0xc5, 1, 2, 3, 4, 5}},
		MaxBlockChunkSize,
	); err != nil {
		t.Fatal(err)
	}
	const want = "00deadbeef06ff060000734e61507059010a00005808110dc50102030405"
	if got := hex.EncodeToString(wire.Bytes()); got != want {
		t.Fatalf("direct block wire mismatch\n got %s\nwant %s", got, want)
	}
}
