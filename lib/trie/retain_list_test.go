package trie

import (
	"sort"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestRetainListAddKeyAndSort(t *testing.T) {
	rl := NewRetainList(0)
	rl.AddKey([]byte{0x12, 0x34})
	rl.AddKey([]byte{0x01})
	if rl.Len() != 2 {
		t.Fatalf("Len = %d, want 2", rl.Len())
	}
	// not yet sorted necessarily; force sort check via sort.Sort using Less/Swap
	sort.Sort(rl)
	if !sort.IsSorted(rl) {
		t.Error("expected sorted retain list")
	}
}

func TestRetainListAddKeyWithMarker(t *testing.T) {
	rl := NewRetainList(0)
	nibbles := rl.AddKeyWithMarker([]byte{0xAB}, true)
	want := []byte{0x0A, 0x0B}
	if len(nibbles) != 2 || nibbles[0] != want[0] || nibbles[1] != want[1] {
		t.Errorf("nibbles = %v, want %v", nibbles, want)
	}
}

func TestRetainListClone(t *testing.T) {
	rl := NewRetainList(2)
	rl.AddKey([]byte{0x01})
	rl.AddKey([]byte{0x02})
	clone := rl.Clone()
	if clone.Len() != rl.Len() {
		t.Fatalf("clone len mismatch: %d vs %d", clone.Len(), rl.Len())
	}
	// mutate original hexes slice pointer independent from clone
	rl.AddKey([]byte{0x03})
	if clone.Len() == rl.Len() {
		t.Error("expected clone to be independent after further additions to original")
	}
}

func TestRetainListNibbleSublist(t *testing.T) {
	rl := NewRetainList(0)
	rl.AddHex([]byte{0x01, 0x02})
	rl.AddHex([]byte{0x02, 0x03})
	rl.AddHex([]byte{0x01, 0x05})
	sub := rl.NibbleSublist(0x01)
	if sub.Len() != 2 {
		t.Fatalf("expected 2 entries for nibble 0x01, got %d", sub.Len())
	}
	for _, h := range sub.hexes {
		if h[0] != 0x01 {
			t.Errorf("unexpected nibble in sublist: %x", h)
		}
	}
}

func TestRetainListCodeTouch(t *testing.T) {
	rl := NewRetainList(0)
	h := types.Hash{1, 2, 3}
	if rl.IsCodeTouched(h) {
		t.Error("expected not touched initially")
	}
	rl.AddCodeTouch(h)
	if !rl.IsCodeTouched(h) {
		t.Error("expected touched after AddCodeTouch")
	}
}

func TestRetainListRetainBasic(t *testing.T) {
	rl := NewRetainList(1)
	rl.AddHex([]byte{0x01, 0x02, 0x03})
	rl.AddHex([]byte{0x05, 0x06})

	if !rl.Retain([]byte{0x01}) {
		t.Error("expected Retain true for prefix of stored key")
	}
	if !rl.Retain([]byte{0x05}) {
		t.Error("expected Retain true for second key prefix")
	}
	if rl.Retain([]byte{0x09}) {
		t.Error("expected Retain false for unrelated prefix")
	}
}

func TestRetainListRetainShortPrefix(t *testing.T) {
	rl := NewRetainList(3)
	rl.AddHex([]byte{0x01, 0x02, 0x03})
	if !rl.Retain([]byte{0x01}) {
		t.Error("expected Retain true when prefix shorter than minLength")
	}
}

func TestRetainListRewindAndString(t *testing.T) {
	rl := NewRetainList(0)
	rl.AddHex([]byte{0x01})
	rl.Retain([]byte{0x01}) // advances internal state
	rl.Rewind()
	if rl.lteIndex != 0 {
		t.Errorf("Rewind did not reset lteIndex: %d", rl.lteIndex)
	}
	s := rl.String()
	if s == "" {
		t.Error("expected non-empty String()")
	}
}

func TestRetainListRetainWithMarker(t *testing.T) {
	rl := NewRetainList(0)
	rl.AddKeyWithMarker([]byte{0x01}, false)
	rl.AddKeyWithMarker([]byte{0x02}, true)

	retained, marker := rl.RetainWithMarker([]byte{0x00})
	_ = retained
	if marker == nil {
		t.Error("expected a marker to be found ahead")
	}
}
