package cscompact

import (
	"encoding/binary"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestStorageCSRoundtrip(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	comp := NewStorageCSCompactor(db, t.TempDir())

	start := uint64(10_000_000)
	end := start + 8192 // one segment
	entries, perBlock, rawBytes, err := comp.readSegment(start, end)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Read %d entries, %d raw bytes for %d blocks", len(entries), rawBytes, end-start)

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()
	compressed := encodeStorageCSSegment(entries, perBlock, enc)

	dec, _ := zstd.NewReader(nil)
	defer dec.Close()
	decoded, decodedPB, err := DecodeStorageCSSegment(compressed, dec)
	if err != nil {
		t.Fatal(err)
	}

	if len(decoded) != len(entries) {
		t.Fatalf("entries: got %d, want %d", len(decoded), len(entries))
	}
	if len(decodedPB) != len(perBlock) {
		t.Fatalf("blocks: got %d, want %d", len(decodedPB), len(perBlock))
	}

	mismatches := 0
	for i, got := range decoded {
		want := entries[i]
		if got.Address != want.Address {
			t.Errorf("entry %d: addr mismatch", i)
			mismatches++
		}
		if got.Incarnation != want.Incarnation {
			t.Errorf("entry %d: incarnation %d != %d", i, got.Incarnation, want.Incarnation)
			mismatches++
		}
		if got.Slot != want.Slot {
			t.Errorf("entry %d: slot mismatch", i)
			mismatches++
		}
		if len(got.OldValue) != len(want.OldValue) {
			t.Errorf("entry %d: value len %d != %d", i, len(got.OldValue), len(want.OldValue))
			mismatches++
		} else {
			for j := range got.OldValue {
				if got.OldValue[j] != want.OldValue[j] {
					t.Errorf("entry %d: value byte %d mismatch", i, j)
					mismatches++
					break
				}
			}
		}
		if mismatches >= 10 {
			t.Fatal("too many mismatches")
		}
	}

	ratio := float64(len(compressed)) / float64(rawBytes) * 100
	t.Logf("Roundtrip OK: %d entries, ratio=%.1f%% (%.1fx), 0 mismatches",
		len(entries), ratio, float64(rawBytes)/float64(len(compressed)))
}

func TestStorageCSMetrics(t *testing.T) {
	db := openErigonRO(t)
	defer db.Close()

	enc, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	defer enc.Close()

	ranges := []struct {
		name  string
		start uint64
	}{
		{"early (1M)", 1_000_000},
		{"mid (10M)", 10_000_000},
		{"late (15M)", 15_000_000},
	}

	for _, r := range ranges {
		end := r.start + 8192
		comp := NewStorageCSCompactor(db, t.TempDir())
		entries, perBlock, rawBytes, err := comp.readSegment(r.start, end)
		if err != nil || len(entries) == 0 {
			t.Logf("%s: skip", r.name)
			continue
		}

		compressed := encodeStorageCSSegment(entries, perBlock, enc)
		ratio := float64(len(compressed)) / float64(rawBytes) * 100

		// Also test raw zstd for comparison.
		var rawBuf []byte
		for _, e := range entries {
			rawBuf = append(rawBuf, e.Address[:]...)
			var incBuf [8]byte
			binary.BigEndian.PutUint64(incBuf[:], e.Incarnation)
			rawBuf = append(rawBuf, incBuf[:]...)
			rawBuf = append(rawBuf, e.Slot[:]...)
			if e.OldValue != nil {
				rawBuf = append(rawBuf, e.OldValue...)
			}
		}
		rawZstd := enc.EncodeAll(rawBuf, nil)
		rawRatio := float64(len(rawZstd)) / float64(rawBytes) * 100

		zeroVals := 0
		for _, e := range entries {
			if len(e.OldValue) == 0 {
				zeroVals++
			}
		}

		t.Logf("%s: entries=%d zeroVals=%d (%.0f%%)", r.name, len(entries), zeroVals,
			float64(zeroVals)/float64(len(entries))*100)
		t.Logf("  columnar+zstd: %d bytes ratio=%.1f%% (%.1fx)",
			len(compressed), ratio, float64(rawBytes)/float64(len(compressed)))
		t.Logf("  raw zstd:      %d bytes ratio=%.1f%% (%.1fx)",
			len(rawZstd), rawRatio, float64(rawBytes)/float64(len(rawZstd)))
	}
}
