package types

import "testing"

func TestBloomSetBytesTrimsOversizedInput(t *testing.T) {
	input := make([]byte, BloomByteLength+8)
	for i := range input {
		input[i] = byte(i)
	}

	var bloom Bloom
	bloom.SetBytes(input)

	want := input[len(input)-BloomByteLength:]
	if got := bloom.Bytes(); string(got) != string(want) {
		t.Fatalf("SetBytes() did not keep the trailing %d bytes", BloomByteLength)
	}
}
