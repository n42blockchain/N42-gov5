package state

import (
	"errors"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/lib/kv/order"
)

func TestHistoryRangeRejectsDescendingOrder(t *testing.T) {
	ht := &HistoryRoTx{}

	if _, err := ht.HistoryRange(0, 1, order.Desc, -1, nil); err == nil || !strings.Contains(err.Error(), "descending order") {
		t.Fatalf("HistoryRange() error = %v, want descending order error", err)
	}
	if _, err := ht.iterateChangedFrozen(0, 1, order.Desc, -1); err == nil || !strings.Contains(err.Error(), "descending order") {
		t.Fatalf("iterateChangedFrozen() error = %v, want descending order error", err)
	}
	if _, err := ht.iterateChangedRecent(0, 1, order.Desc, -1, nil); err == nil || !strings.Contains(err.Error(), "descending order") {
		t.Fatalf("iterateChangedRecent() error = %v, want descending order error", err)
	}
}

func TestIdxRangeRecentRejectsDescendingOrder(t *testing.T) {
	ht := &HistoryRoTx{h: &History{}}

	if _, err := ht.idxRangeRecent([]byte("k"), 0, 1, order.Desc, -1, nil); err == nil || !strings.Contains(err.Error(), "descending order") {
		t.Fatalf("idxRangeRecent() error = %v, want descending order error", err)
	}

	ht.h.largeValues = true
	if _, err := ht.idxRangeRecent([]byte("k"), 0, 1, order.Desc, -1, nil); err == nil || !strings.Contains(err.Error(), "descending order") {
		t.Fatalf("idxRangeRecent() with largeValues error = %v, want descending order error", err)
	}
}

func TestErrKVIterEmitsErrorOnce(t *testing.T) {
	want := errors.New("boom")
	it := &errKVIter{err: want}

	if !it.HasNext() {
		t.Fatal("HasNext() = false, want true")
	}
	_, _, err := it.Next()
	if !errors.Is(err, want) {
		t.Fatalf("Next() error = %v, want %v", err, want)
	}
	if it.HasNext() {
		t.Fatal("HasNext() after Next = true, want false")
	}
}
