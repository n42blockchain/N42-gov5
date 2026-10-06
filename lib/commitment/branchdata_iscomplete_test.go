package commitment

import (
	"encoding/binary"
	"testing"
)

// buildTouchAfter builds the 4-byte touchMap/afterMap header used by
// BranchData.IsComplete.
func buildTouchAfter(touchMap, afterMap uint16) BranchData {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint16(buf[0:], touchMap)
	binary.BigEndian.PutUint16(buf[2:], afterMap)
	return buf
}

func TestBranchDataIsComplete(t *testing.T) {
	// Complete: every bit present in afterMap is also touched
	// (^touchMap & afterMap == 0 means afterMap is a subset of touchMap).
	bd := buildTouchAfter(0xFFFF, 0x00FF)
	if !bd.IsComplete() {
		t.Fatalf("expected complete when afterMap subset of touchMap")
	}

	// Equal maps are trivially complete.
	bd2 := buildTouchAfter(0x00FF, 0x00FF)
	if !bd2.IsComplete() {
		t.Fatalf("expected complete for equal maps")
	}

	// Incomplete: afterMap has a bit not present in touchMap.
	bd3 := buildTouchAfter(0x00F0, 0x00FF)
	if bd3.IsComplete() {
		t.Fatalf("expected incomplete when afterMap has untouched bits")
	}

	// Both zero: complete (no children touched or present).
	bd4 := buildTouchAfter(0x0000, 0x0000)
	if !bd4.IsComplete() {
		t.Fatalf("expected complete for empty maps")
	}
}
