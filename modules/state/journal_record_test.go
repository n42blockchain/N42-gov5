// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"reflect"
	"testing"
	"unsafe"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// TestJournalRecordRoundTrip: every entry type must survive record() -> entry()
// with identical field values. Pointer fields are compared by pointee, since a
// record stores the address by value and hands back a pointer into itself.
func TestJournalRecordRoundTrip(t *testing.T) {
	addr := types.Address{1, 2, 3}
	key := types.Hash{4, 5, 6}
	val := uint256.NewInt(123456789)
	bi := &BalanceIncrease{count: 2}
	so := &stateObject{address: addr}

	entries := []journalEntry{
		createObjectChange{account: &addr},
		resetObjectChange{account: &addr, prev: so},
		selfdestructChange{account: &addr, prev: true, prevbalance: *val},
		balanceChange{account: &addr, prev: *val},
		balanceIncrease{account: &addr, increase: *val},
		balanceIncreaseTransfer{account: &addr, bi: bi},
		nonceChange{account: &addr, prev: 77},
		storageChange{account: &addr, key: key, prevalue: *val},
		fakeStorageChange{account: &addr, key: key, prevalue: *val},
		codeChange{account: &addr, prevhash: key, prevcode: []byte{0x60, 0x00}},
		refundChange{prev: 4242},
		addLogChange{txhash: key},
		touchChange{account: &addr},
		accessListAddAccountChange{address: addr},
		accessListAddSlotChange{address: addr, slot: key},
		transientStorageChange{account: &addr, key: key, prevalue: *val},
		storageWipeAddChange{account: &addr},
	}
	if len(entries) != 17 {
		t.Fatalf("expected 17 entry types, got %d — add the new type to this test and to journal_record.go", len(entries))
	}
	for _, e := range entries {
		rec := e.(interface{ record() journalRecord }).record()
		got := rec.entry()
		if reflect.TypeOf(got) != reflect.TypeOf(e) {
			t.Fatalf("%T: round trip returned %T", e, got)
		}
		if !derefEqual(got, e) {
			t.Fatalf("%T: round trip changed values:\n got  %+v\n want %+v", e, got, e)
		}
		// dirtied() and the record's dirties flag must agree, and the dirty
		// address must be the one the record will count.
		wantDirty := e.dirtied()
		if (wantDirty != nil) != rec.dirties {
			t.Fatalf("%T: dirties=%v but dirtied()=%v", e, rec.dirties, wantDirty)
		}
		if wantDirty != nil && *wantDirty != rec.addr {
			t.Fatalf("%T: dirty address %x, record addr %x", e, *wantDirty, rec.addr)
		}
	}
}

// derefEqual compares two same-typed entry structs field by field, following
// pointer-to-Address fields by value and other pointers by identity. Entry
// fields are unexported, so values are read through addressable copies.
func derefEqual(a, b journalEntry) bool {
	ca := reflect.New(reflect.TypeOf(a)).Elem()
	cb := reflect.New(reflect.TypeOf(b)).Elem()
	ca.Set(reflect.ValueOf(a))
	cb.Set(reflect.ValueOf(b))
	for i := 0; i < ca.NumField(); i++ {
		fa := reflect.NewAt(ca.Field(i).Type(), unsafe.Pointer(ca.Field(i).UnsafeAddr())).Elem()
		fb := reflect.NewAt(cb.Field(i).Type(), unsafe.Pointer(cb.Field(i).UnsafeAddr())).Elem()
		if fa.Kind() == reflect.Ptr {
			if fa.IsNil() != fb.IsNil() {
				return false
			}
			if fa.IsNil() {
				continue
			}
			if fa.Type() == reflect.TypeOf(&types.Address{}) {
				if fa.Elem().Interface() != fb.Elem().Interface() {
					return false
				}
				continue
			}
			if fa.Pointer() != fb.Pointer() { // *stateObject, *BalanceIncrease: identity
				return false
			}
			continue
		}
		if !reflect.DeepEqual(fa.Interface(), fb.Interface()) {
			return false
		}
	}
	return true
}

// TestJournalPushRevertMatchesInterfacePath drives the journal through a
// snapshot/revert cycle with the by-value records and checks the dirty
// accounting and entry count behave as the interface version did.
func TestJournalPushRevertMatchesInterfacePath(t *testing.T) {
	j := newJournal()
	a := types.Address{9}
	b := types.Address{8}
	j.push(balanceChange{account: &a, prev: *uint256.NewInt(1)}.record())
	j.push(nonceChange{account: &a, prev: 5}.record())
	mark := j.length()
	j.push(touchChange{account: &b}.record())
	j.push(refundChange{prev: 10}.record())
	if j.dirties[a] != 2 || j.dirties[b] != 1 {
		t.Fatalf("dirty counts before revert: %v", j.dirties)
	}
	// Revert the tail on an IBS-less path: only the bookkeeping is checked here.
	// Reverting entries that touch state objects needs a real IBS, which the
	// broader state suite covers; here we revert only the touch/refund tail.
	sdb := New(nil)
	j.revert(sdb, mark)
	if j.length() != mark {
		t.Fatalf("length after revert: %d want %d", j.length(), mark)
	}
	if _, still := j.dirties[b]; still {
		t.Fatal("touch dirty not dropped on revert")
	}
	if j.dirties[a] != 2 {
		t.Fatalf("unrelated dirty count changed: %d", j.dirties[a])
	}
	if sdb.refund != 10 {
		t.Fatalf("refund not restored: %d", sdb.refund)
	}
}

func BenchmarkJournalPush(b *testing.B) {
	j := newJournal()
	a := types.Address{1}
	k := types.Hash{2}
	v := *uint256.NewInt(3)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		j.push(storageChange{account: &a, key: k, prevalue: v}.record())
		if i%4096 == 4095 {
			j.reset()
		}
	}
}

func BenchmarkJournalAppendInterface(b *testing.B) {
	j := newJournal()
	a := types.Address{1}
	k := types.Hash{2}
	v := *uint256.NewInt(3)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		j.append(storageChange{account: &a, key: k, prevalue: v})
		if i%4096 == 4095 {
			j.reset()
		}
	}
}
