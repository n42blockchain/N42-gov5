package history

import (
	"bytes"
	"testing"
)

func TestGroupedRoundTrip(t *testing.T) {
	entries := []GroupedHistory{
		{
			SubKey: bytes.Repeat([]byte{0x01}, 32),
			Changes: []Change{
				{Block: 100, Value: []byte{0xaa}},
				{Block: 500, Value: []byte{0xbb}},
			},
		},
		{
			SubKey: bytes.Repeat([]byte{0x02}, 32),
			Changes: []Change{
				{Block: 200, Value: bytes.Repeat([]byte{0xff}, 32)},
			},
		},
		{
			SubKey: bytes.Repeat([]byte{0x03}, 32),
			Changes: []Change{}, // empty timeline allowed
		},
	}

	packed := PackGrouped(nil, 32, entries)
	out, err := UnpackGrouped(packed, 32)
	if err != nil {
		t.Fatalf("UnpackGrouped: %v", err)
	}
	if len(out) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(out), len(entries))
	}
	for i, e := range entries {
		if !bytes.Equal(out[i].SubKey, e.SubKey) {
			t.Errorf("entry %d subKey mismatch", i)
		}
		if len(out[i].Changes) != len(e.Changes) {
			t.Errorf("entry %d: %d changes, want %d", i, len(out[i].Changes), len(e.Changes))
			continue
		}
		for j, ch := range e.Changes {
			if out[i].Changes[j].Block != ch.Block {
				t.Errorf("entry %d change %d block mismatch", i, j)
			}
			if !bytes.Equal(out[i].Changes[j].Value, ch.Value) {
				t.Errorf("entry %d change %d value mismatch", i, j)
			}
		}
	}
}

func TestAsOfGrouped(t *testing.T) {
	slotA := bytes.Repeat([]byte{0xaa}, 32)
	slotB := bytes.Repeat([]byte{0xbb}, 32)
	missing := bytes.Repeat([]byte{0xff}, 32)

	entries := []GroupedHistory{
		{SubKey: slotA, Changes: []Change{
			{Block: 100, Value: []byte{0x01}},
			{Block: 500, Value: []byte{0x02}},
		}},
		{SubKey: slotB, Changes: []Change{
			{Block: 200, Value: []byte{0x03}},
		}},
	}
	packed := PackGrouped(nil, 32, entries)

	// slotA before any change → not found
	v, ok, err := AsOfGrouped(packed, 32, slotA, 50)
	if err != nil || ok {
		t.Errorf("slotA@50: ok=%v err=%v", ok, err)
	}
	// slotA at exactly first change
	v, ok, err = AsOfGrouped(packed, 32, slotA, 100)
	if err != nil || !ok || !bytes.Equal(v, []byte{0x01}) {
		t.Errorf("slotA@100: v=%x ok=%v err=%v", v, ok, err)
	}
	// slotA between changes
	v, ok, err = AsOfGrouped(packed, 32, slotA, 300)
	if err != nil || !ok || !bytes.Equal(v, []byte{0x01}) {
		t.Errorf("slotA@300: v=%x ok=%v err=%v", v, ok, err)
	}
	// slotA at second change
	v, ok, err = AsOfGrouped(packed, 32, slotA, 500)
	if err != nil || !ok || !bytes.Equal(v, []byte{0x02}) {
		t.Errorf("slotA@500: v=%x ok=%v err=%v", v, ok, err)
	}
	// slotB at its change
	v, ok, err = AsOfGrouped(packed, 32, slotB, 200)
	if err != nil || !ok || !bytes.Equal(v, []byte{0x03}) {
		t.Errorf("slotB@200: v=%x ok=%v err=%v", v, ok, err)
	}
	// missing subKey
	v, ok, err = AsOfGrouped(packed, 32, missing, 999)
	if err != nil || ok {
		t.Errorf("missing: ok=%v err=%v", ok, err)
	}
}

// TestGroupedSizeReal validates v2 is meaningfully smaller than v1 for
// a typical address with many slots.
func TestGroupedSizeReal(t *testing.T) {
	// Simulate one EOA-heavy contract address with 100 touched slots,
	// each with 2 changes over 25M blocks (typical token-like contract
	// pattern).
	const numSlots = 100
	const changesPerSlot = 2

	v1TotalBytes := 0 // would use 52B keys: each (key,history) page entry costs 52 + history.
	groupedEntries := make([]GroupedHistory, 0, numSlots)
	for i := 0; i < numSlots; i++ {
		slot := make([]byte, 32)
		slot[31] = byte(i)
		changes := make([]Change, changesPerSlot)
		for j := 0; j < changesPerSlot; j++ {
			changes[j] = Change{
				Block: uint64(j+1) * 5_000_000,
				Value: []byte{byte(i), byte(j), 0x42},
			}
		}
		groupedEntries = append(groupedEntries, GroupedHistory{SubKey: slot, Changes: changes})
		// v1 equivalent: 52B key + PackHistory(changes)
		v1TotalBytes += 52 + len(PackHistory(nil, changes))
	}

	v2Bytes := len(PackGrouped(nil, 32, groupedEntries))
	t.Logf("v1 raw  : %d B for %d slots × %d changes (52B key per slot)",
		v1TotalBytes, numSlots, changesPerSlot)
	t.Logf("v2 group: %d B (20B addr key shared once at coldstore layer)",
		v2Bytes)
	saved := v1TotalBytes - v2Bytes
	t.Logf("Saved %d B in raw form (%.1f%% reduction)",
		saved, float64(saved)*100/float64(v1TotalBytes))

	if v2Bytes >= v1TotalBytes {
		t.Errorf("v2 (%d B) not smaller than v1 (%d B)", v2Bytes, v1TotalBytes)
	}
}
