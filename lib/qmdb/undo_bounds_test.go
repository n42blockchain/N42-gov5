package qmdb

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestUnmarshalBlockUndoRejectsImpossibleEntryCounts(t *testing.T) {
	for _, version := range []byte{blockUndoVersion, blockUndoVersionV2} {
		for _, count := range []uint64{1, 1000000, math.MaxInt64, math.MaxUint64} {
			encoded := binary.AppendUvarint([]byte{version, 0}, count)
			if _, err := UnmarshalBlockUndo(encoded); err == nil {
				t.Fatalf("version %d accepted %d entries without data", version, count)
			}
		}
	}
}

func TestUnmarshalBlockUndoMinimumSizeEntry(t *testing.T) {
	for _, version := range []byte{blockUndoVersion, blockUndoVersionV2} {
		encoded := []byte{version, 0, 1, 0} // prev, count, slot
		encoded = append(encoded, make([]byte, 32)...)
		encoded = append(encoded, 0) // empty value
		if version == blockUndoVersionV2 {
			encoded = append(encoded, 0) // no appends
		}
		undo, err := UnmarshalBlockUndo(encoded)
		if err != nil || len(undo.Entries) != 1 {
			t.Fatalf("version %d rejected minimum-sized entry: %v", version, err)
		}
	}
}

func FuzzUnmarshalBlockUndo(f *testing.F) {
	f.Add((&BlockUndo{PrevNextSlot: 1, Entries: []UndoEntry{{Slot: 0, KeyHash: key(1), Value: []byte("old")}}, AppendedKeys: []Hash{key(2)}}).Marshal())
	f.Add(binary.AppendUvarint([]byte{blockUndoVersionV2, 0}, math.MaxUint64))
	f.Fuzz(func(t *testing.T, input []byte) {
		u, err := UnmarshalBlockUndo(input)
		if err != nil {
			return
		}
		if _, err := UnmarshalBlockUndo(u.Marshal()); err != nil {
			t.Fatalf("accepted record does not round-trip: %v", err)
		}
	})
}
