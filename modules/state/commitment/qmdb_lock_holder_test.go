// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package commitment

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
)

// A timed lookup that waits for a writer-side holder records the wait apart
// from the read, the holder's name and how long it had held the lock; an
// uncontended one records no wait over the threshold and the same value as
// Lookup.
func TestLookupTimedReportsHolder(t *testing.T) {
	rc := NewQMDBRootComputer()
	a := types.BytesToAddress([]byte{0x42})
	if _, err := rc.ComputeRoot(map[types.Address]*account.StateAccount{a: testAcct(3, 9)}, nil); err != nil {
		t.Fatal(err)
	}
	kh := qmdb.Hash(AccountKeyHash(a))
	want, wantFound, _ := rc.Lookup(kh, nil)
	var tm LookupTiming
	v, found, _ := rc.LookupTimed(kh, nil, &tm)
	if found != wantFound || string(v) != string(want) || tm.Waits.Load() != 0 {
		t.Fatalf("uncontended: found %v/%v, waits %d", found, wantFound, tm.Waits.Load())
	}
	unlock := rc.lockWriter(holderFlushAsync)
	done := make(chan struct{})
	go func() {
		defer close(done)
		rc.LookupTimed(kh, nil, &tm)
	}()
	time.Sleep(5 * time.Millisecond)
	unlock()
	<-done
	if tm.Waits.Load() != 1 || tm.Wait.Load() < int64(time.Millisecond) ||
		QMDBHolderName(tm.Holder.Load()) != "flushAsync" || tm.HolderAge.Load() <= 0 {
		t.Fatalf("contended: waits %d wait %d holder %s age %d", tm.Waits.Load(), tm.Wait.Load(),
			QMDBHolderName(tm.Holder.Load()), tm.HolderAge.Load())
	}
	if rc.holder.Load() != holderNone || QMDBHolderName(99) != "unknown" || len(holderNames) != int(holderRoot)+1 {
		t.Fatal("holder state")
	}
}
