// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"testing"
	"time"
)

// SetCold stamps its wait for the tree's lock, the install and the holder.
func TestSetColdStamp(t *testing.T) {
	r := NewQMDBRootComputer()
	if s := r.LastSetCold(); s != (SetColdStamp{}) {
		t.Fatalf("before any SetCold: %+v", s)
	}
	store := emptyGetter{}
	r.SetCold(store)
	if s := r.LastSetCold(); s.Holder != holderNone {
		t.Fatalf("free lock: holder %s", QMDBHolderName(s.Holder))
	}
	unlock := r.lockWriter(holderFlushAsync)
	go func() {
		time.Sleep(30 * time.Millisecond)
		unlock()
	}()
	time.Sleep(time.Millisecond) // the holder is set before SetCold begins
	r.SetCold(store)
	s := r.LastSetCold()
	if s.Wait < 20*time.Millisecond {
		t.Fatalf("wait %v, want >= 20ms", s.Wait)
	}
	if QMDBHolderName(s.Holder) != "flushAsync" {
		t.Fatalf("holder %s, want flushAsync", QMDBHolderName(s.Holder))
	}
}
