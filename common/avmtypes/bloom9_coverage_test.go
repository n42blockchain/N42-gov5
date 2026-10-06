package avmtypes

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/avmutil"
)

func TestBytesToBloom(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03}
	b := BytesToBloom(data)
	if !bytes.HasSuffix(b.Bytes(), data) {
		t.Fatalf("BytesToBloom did not preserve trailing bytes: %x", b.Bytes())
	}
}

func TestBloomAddAndTest(t *testing.T) {
	var b Bloom
	topic := []byte("hello-topic")
	if b.Test(topic) {
		t.Fatal("empty bloom should not contain topic")
	}
	b.Add(topic)
	if !b.Test(topic) {
		t.Fatal("bloom should contain topic after Add")
	}
	if b.Test([]byte("other-topic-not-added")) {
		// Not a hard guarantee (false positives possible) but exceedingly
		// unlikely for this fixed input; flags a Test() regression.
		t.Log("warning: unrelated topic unexpectedly matched bloom (could be a false positive)")
	}
}

func TestBloomBig(t *testing.T) {
	var b Bloom
	b.Add([]byte("x"))
	got := b.Big()
	if got.Cmp(big.NewInt(0)) == 0 {
		t.Fatal("Big() should be non-zero after Add")
	}
}

func TestBloomMarshalUnmarshalText(t *testing.T) {
	var b Bloom
	b.Add([]byte("marshal-me"))
	text, err := b.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText error: %v", err)
	}
	var b2 Bloom
	if err := b2.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText error: %v", err)
	}
	if b != b2 {
		t.Fatalf("round-trip mismatch: %x != %x", b, b2)
	}
}

func TestBloomUnmarshalTextInvalid(t *testing.T) {
	var b Bloom
	if err := b.UnmarshalText([]byte("not-hex")); err == nil {
		t.Fatal("expected error for invalid hex text")
	}
}

func TestLogsBloomAndBloom9(t *testing.T) {
	addr := avmutil.BytesToAddress([]byte{0xAA})
	topic := avmutil.BytesToHash([]byte{0xBB})
	logs := []*Log{
		{Address: addr, Topics: []avmutil.Hash{topic}},
	}
	bits := LogsBloom(logs)
	var b Bloom
	copy(b[:], bits)
	if !b.Test(addr.Bytes()) {
		t.Fatal("LogsBloom result should match the log's address")
	}
	if !b.Test(topic.Bytes()) {
		t.Fatal("LogsBloom result should match the log's topic")
	}

	data := make([]byte, BloomByteLength)
	data[0] = 0xFF
	out := Bloom9(data)
	if len(out) != BloomByteLength {
		t.Fatalf("Bloom9 result length = %d, want %d", len(out), BloomByteLength)
	}
}

type fakeBytesBacked struct{ b []byte }

func (f fakeBytesBacked) Bytes() []byte { return f.b }

func TestBloomLookup(t *testing.T) {
	var b Bloom
	topic := []byte("lookup-topic")
	b.Add(topic)
	if !BloomLookup(b, fakeBytesBacked{b: topic}) {
		t.Fatal("BloomLookup should find the added topic")
	}
}
