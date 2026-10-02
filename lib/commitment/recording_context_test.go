// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// recording_context.go had no test file: covers NewRecordingContext and every
// method (Branch/Account/Storage/PutBranch/TxNum), proving it both forwards to
// the wrapped PatriciaContext unchanged AND records what it saw, including the
// "not found" (nil) cases which must NOT be recorded.

package commitment

import (
	"errors"
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
)

// fakeRecordingInner is a minimal PatriciaContext for exercising RecordingContext.
type fakeRecordingInner struct {
	branches     map[string][]byte
	accounts     map[string]*Update
	storages     map[string]*Update
	putBranchErr error
	lastPut      struct{ prefix, data, prev []byte }
	txNum        uint64
}

func (f *fakeRecordingInner) Branch(prefix []byte) ([]byte, kv.Step, error) {
	return f.branches[string(prefix)], 0, nil
}
func (f *fakeRecordingInner) Account(plainKey []byte) (*Update, error) {
	return f.accounts[string(plainKey)], nil
}
func (f *fakeRecordingInner) Storage(plainKey []byte) (*Update, error) {
	return f.storages[string(plainKey)], nil
}
func (f *fakeRecordingInner) PutBranch(prefix, data, prevData []byte) error {
	f.lastPut.prefix, f.lastPut.data, f.lastPut.prev = prefix, data, prevData
	return f.putBranchErr
}
func (f *fakeRecordingInner) TxNum() uint64 { return f.txNum }

func TestRecordingContextBranch(t *testing.T) {
	inner := &fakeRecordingInner{branches: map[string][]byte{"p1": []byte("branch-data")}}
	rc := NewRecordingContext(inner)

	data, _, err := rc.Branch([]byte("p1"))
	if err != nil || string(data) != "branch-data" {
		t.Fatalf("Branch(p1) = (%q,%v), want (branch-data,nil)", data, err)
	}
	if got, ok := rc.branches["p1"]; !ok || string(got) != "branch-data" {
		t.Fatalf("expected recorded branch p1=branch-data, got %q (ok=%v)", got, ok)
	}

	// A miss (nil data, no error) must NOT be recorded.
	if data, _, err := rc.Branch([]byte("missing")); err != nil || data != nil {
		t.Fatalf("Branch(missing) = (%v,%v), want (nil,nil)", data, err)
	}
	if _, ok := rc.branches["missing"]; ok {
		t.Fatal("a miss must not be recorded")
	}
}

func TestRecordingContextAccountAndStorage(t *testing.T) {
	acctUpd := &Update{Nonce: 5}
	storUpd := &Update{Nonce: 9}
	inner := &fakeRecordingInner{
		accounts: map[string]*Update{"acct1": acctUpd},
		storages: map[string]*Update{"slot1": storUpd},
	}
	rc := NewRecordingContext(inner)

	u, err := rc.Account([]byte("acct1"))
	if err != nil || u != acctUpd {
		t.Fatalf("Account(acct1) = (%v,%v), want the same *Update", u, err)
	}
	if _, ok := rc.accounts["acct1"]; !ok {
		t.Fatal("expected acct1 to be recorded")
	}
	if _, ok := rc.accounts["nope"]; ok {
		t.Fatal("unrecorded key must not appear")
	}
	if u, err := rc.Account([]byte("nope")); err != nil || u != nil {
		t.Fatalf("Account(nope) = (%v,%v), want (nil,nil)", u, err)
	}

	u2, err := rc.Storage([]byte("slot1"))
	if err != nil || u2 != storUpd {
		t.Fatalf("Storage(slot1) = (%v,%v), want the same *Update", u2, err)
	}
	if _, ok := rc.storages["slot1"]; !ok {
		t.Fatal("expected slot1 to be recorded")
	}
}

func TestRecordingContextPutBranchAndTxNum(t *testing.T) {
	inner := &fakeRecordingInner{txNum: 77}
	rc := NewRecordingContext(inner)

	if err := rc.PutBranch([]byte("pfx"), []byte("new"), []byte("prev")); err != nil {
		t.Fatal(err)
	}
	if string(inner.lastPut.prefix) != "pfx" || string(inner.lastPut.data) != "new" || string(inner.lastPut.prev) != "prev" {
		t.Fatalf("PutBranch did not forward correctly: %+v", inner.lastPut)
	}
	bw, ok := rc.putBranches["pfx"]
	if !ok || string(bw.NewData) != "new" || string(bw.PrevData) != "prev" {
		t.Fatalf("expected recorded BranchWrite{prev,new}, got %+v (ok=%v)", bw, ok)
	}
	if !reflect.DeepEqual(bw.NewData, []byte("new")) {
		t.Fatalf("NewData mismatch: %v", bw.NewData)
	}

	if got := rc.TxNum(); got != 77 {
		t.Fatalf("TxNum() = %d, want 77", got)
	}

	// Error propagation from the inner context's PutBranch.
	inner.putBranchErr = errors.New("boom")
	if err := rc.PutBranch([]byte("pfx2"), nil, nil); err == nil {
		t.Fatal("expected PutBranch to propagate the inner error")
	}
}
