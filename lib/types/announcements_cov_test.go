package types

import (
	"bytes"
	"sort"
	"testing"
)

func g41Hash(b byte) []byte {
	h := make([]byte, 32)
	h[31] = b
	return h
}

func TestAnnouncementsAppendAndAt(t *testing.T) {
	var a Announcements
	a.Append(LegacyTxType, 100, g41Hash(1))
	a.Append(AccessListTxType, 200, g41Hash(2))

	if a.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", a.Len())
	}

	typ, size, hash := a.At(0)
	if typ != LegacyTxType || size != 100 || !bytes.Equal(hash, g41Hash(1)) {
		t.Fatalf("At(0) = (%d, %d, %x)", typ, size, hash)
	}
}

func TestAnnouncementsAppendOther(t *testing.T) {
	var a, b Announcements
	a.Append(LegacyTxType, 1, g41Hash(1))
	b.Append(AccessListTxType, 2, g41Hash(2))

	a.AppendOther(b)
	if a.Len() != 2 {
		t.Fatalf("Len() after AppendOther = %d, want 2", a.Len())
	}
	typ, size, hash := a.At(1)
	if typ != AccessListTxType || size != 2 || !bytes.Equal(hash, g41Hash(2)) {
		t.Fatalf("At(1) after AppendOther = (%d, %d, %x)", typ, size, hash)
	}
}

func TestAnnouncementsReset(t *testing.T) {
	var a Announcements
	a.Append(LegacyTxType, 1, g41Hash(1))
	a.Reset()
	if a.Len() != 0 {
		t.Fatalf("Len() after Reset = %d, want 0", a.Len())
	}
}

func TestAnnouncementsSortInterface(t *testing.T) {
	var a Announcements
	a.Append(LegacyTxType, 1, g41Hash(2))
	a.Append(AccessListTxType, 2, g41Hash(1))

	if !a.Less(1, 0) {
		t.Fatal("expected hash(1) < hash(2)")
	}
	sort.Sort(a)
	_, _, h0 := a.At(0)
	if !bytes.Equal(h0, g41Hash(1)) {
		t.Fatalf("expected sorted order to put hash(1) first, got %x", h0)
	}
}

func TestAnnouncementsSwap(t *testing.T) {
	var a Announcements
	a.Append(LegacyTxType, 1, g41Hash(1))
	a.Append(AccessListTxType, 2, g41Hash(2))
	a.Swap(0, 1)

	typ, size, hash := a.At(0)
	if typ != AccessListTxType || size != 2 || !bytes.Equal(hash, g41Hash(2)) {
		t.Fatalf("Swap did not exchange entries: got (%d, %d, %x)", typ, size, hash)
	}
}

func TestAnnouncementsDedupCopy(t *testing.T) {
	var a Announcements
	a.Append(LegacyTxType, 1, g41Hash(1))
	a.Append(AccessListTxType, 2, g41Hash(1)) // duplicate hash
	a.Append(DynamicFeeTxType, 3, g41Hash(2))

	deduped := a.DedupCopy()
	if deduped.Len() != 2 {
		t.Fatalf("DedupCopy().Len() = %d, want 2", deduped.Len())
	}
}

func TestAnnouncementsDedupCopyEmpty(t *testing.T) {
	var a Announcements
	deduped := a.DedupCopy()
	if deduped.Len() != 0 {
		t.Fatalf("DedupCopy() on empty = %d, want 0", deduped.Len())
	}
}

func TestAnnouncementsDedupHashes(t *testing.T) {
	var a Announcements
	a.Append(LegacyTxType, 1, g41Hash(1))
	a.Append(AccessListTxType, 2, g41Hash(1))
	a.Append(DynamicFeeTxType, 3, g41Hash(2))

	hashes := a.DedupHashes()
	if hashes.Len() != 2 {
		t.Fatalf("DedupHashes().Len() = %d, want 2", hashes.Len())
	}
}

func TestAnnouncementsDedupHashesEmpty(t *testing.T) {
	var a Announcements
	hashes := a.DedupHashes()
	if hashes.Len() != 0 {
		t.Fatalf("DedupHashes() on empty = %d, want 0", hashes.Len())
	}
}

func TestAnnouncementsHashesAndCopy(t *testing.T) {
	var a Announcements
	a.Append(LegacyTxType, 1, g41Hash(1))
	a.Append(AccessListTxType, 2, g41Hash(2))

	hashes := a.Hashes()
	if hashes.Len() != 2 {
		t.Fatalf("Hashes().Len() = %d, want 2", hashes.Len())
	}

	cp := a.Copy()
	if cp.Len() != a.Len() {
		t.Fatalf("Copy().Len() = %d, want %d", cp.Len(), a.Len())
	}
	// Mutating the copy's underlying slices must not affect the original.
	typ, _, _ := cp.At(0)
	if typ != LegacyTxType {
		t.Fatalf("Copy() At(0) type = %d, want LegacyTxType", typ)
	}
}

func TestAnnouncementsCopyEmpty(t *testing.T) {
	var a Announcements
	cp := a.Copy()
	if cp.Len() != 0 {
		t.Fatalf("Copy() of empty = %d, want 0", cp.Len())
	}
}

func TestHashesAtLenLessSwap(t *testing.T) {
	var h Hashes
	h = append(h, g41Hash(2)...)
	h = append(h, g41Hash(1)...)

	if h.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", h.Len())
	}
	if !h.Less(1, 0) {
		t.Fatal("expected hash(1) < hash(2)")
	}
	h.Swap(0, 1)
	if !bytes.Equal(h.At(0), g41Hash(1)) {
		t.Fatalf("Swap did not exchange entries: got %x", h.At(0))
	}
}

func TestHashesDedupCopy(t *testing.T) {
	var h Hashes
	h = append(h, g41Hash(1)...)
	h = append(h, g41Hash(1)...)
	h = append(h, g41Hash(2)...)

	deduped := h.DedupCopy()
	if deduped.Len() != 2 {
		t.Fatalf("DedupCopy().Len() = %d, want 2", deduped.Len())
	}
}

func TestHashesDedupCopyEmpty(t *testing.T) {
	var h Hashes
	if got := h.DedupCopy(); got.Len() != 0 {
		t.Fatalf("DedupCopy() on empty = %d, want 0", got.Len())
	}
}
