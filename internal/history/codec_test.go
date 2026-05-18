package history

import (
	"bytes"
	"testing"
)

func TestPackUnpackRoundTrip(t *testing.T) {
	entries := []Change{
		{Block: 100, Value: []byte{0x01}},
		{Block: 500, Value: []byte{0x02, 0x03}},
		{Block: 1000, Value: []byte{}},
		{Block: 1_000_000, Value: bytes.Repeat([]byte{0xff}, 32)},
		{Block: 25_000_000, Value: []byte{0x42}},
	}

	packed := PackHistory(nil, entries)
	unpacked, err := UnpackHistory(packed)
	if err != nil {
		t.Fatalf("UnpackHistory: %v", err)
	}
	if len(unpacked) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(unpacked), len(entries))
	}
	for i, e := range entries {
		if unpacked[i].Block != e.Block {
			t.Errorf("entry %d block: got %d, want %d", i, unpacked[i].Block, e.Block)
		}
		if !bytes.Equal(unpacked[i].Value, e.Value) {
			t.Errorf("entry %d value: got %x, want %x", i, unpacked[i].Value, e.Value)
		}
	}
}

func TestPackEmpty(t *testing.T) {
	packed := PackHistory(nil, nil)
	out, err := UnpackHistory(packed)
	if err != nil {
		t.Fatalf("UnpackHistory(empty): %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty, got %d", len(out))
	}
}

func TestAsOfBoundaries(t *testing.T) {
	entries := []Change{
		{Block: 100, Value: []byte{0x01}},
		{Block: 500, Value: []byte{0x02}},
		{Block: 1000, Value: []byte{0x03}},
	}
	packed := PackHistory(nil, entries)

	cases := []struct {
		block    uint64
		want     []byte
		wantOk   bool
		describe string
	}{
		{50, nil, false, "before first change"},
		{99, nil, false, "one before first"},
		{100, []byte{0x01}, true, "exactly first"},
		{101, []byte{0x01}, true, "just after first"},
		{499, []byte{0x01}, true, "right before second"},
		{500, []byte{0x02}, true, "exactly second"},
		{750, []byte{0x02}, true, "between second and third"},
		{1000, []byte{0x03}, true, "exactly third"},
		{1_000_000, []byte{0x03}, true, "long after last"},
	}
	for _, c := range cases {
		got, ok, err := AsOf(packed, c.block)
		if err != nil {
			t.Errorf("%s: AsOf err: %v", c.describe, err)
			continue
		}
		if ok != c.wantOk {
			t.Errorf("%s: ok=%v, want %v", c.describe, ok, c.wantOk)
		}
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s: got %x, want %x", c.describe, got, c.want)
		}
	}
}

// TestPackSizeRealistic measures bytes-per-entry for a typical
// storage slot with 4 changes scattered across 25M blocks.
func TestPackSizeRealistic(t *testing.T) {
	entries := []Change{
		{Block: 5_123_456, Value: []byte{0x01, 0x42}},
		{Block: 12_456_789, Value: []byte{0xab}},
		{Block: 18_901_234, Value: bytes.Repeat([]byte{0xff}, 20)},
		{Block: 24_987_654, Value: []byte{}},
	}
	packed := PackHistory(nil, entries)
	bytesPerEntry := float64(len(packed)) / float64(len(entries))
	t.Logf("4-write slot: packed=%d bytes, per-entry=%.2f B", len(packed), bytesPerEntry)
	// Target: < 15 B/entry. count(1) + 4×(deltaVarint(~3) + lenVarint(1) + valueBytes(avg 6)) = ~41 B / 4 = ~10 B
	if bytesPerEntry > 15 {
		t.Errorf("packed too large: %.2f B/entry (want < 15)", bytesPerEntry)
	}
}
