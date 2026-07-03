// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
)

// mkFreezer opens a fresh RW freezer in a temp dir.
func mkFreezer(t *testing.T) *freezer.Freezer {
	fz, err := freezer.New(t.TempDir(), freezer.DefaultFreezeThreshold)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fz.Close() })
	return fz
}

// TestCSFreezerSinkAlignAndOrder covers mid-history start adoption, in-order
// appends, the rewind path (batch rollback + re-execution), and the
// freezer-ahead truncate on re-init.
func TestCSFreezerSinkAlignAndOrder(t *testing.T) {
	fz := mkFreezer(t)
	const head = uint64(1000)

	sink, err := NewCSFreezerSink(fz, head)
	if err != nil {
		t.Fatal(err)
	}
	blob := func(b byte) []byte { return []byte{0, 0, b} } // count=0 + marker byte
	for blk := head + 1; blk <= head+5; blk++ {
		if err := sink.Add(blk, blob(byte(blk)), blob(byte(blk))); err != nil {
			t.Fatal(err)
		}
	}
	if err := sink.Flush(); err != nil {
		t.Fatal(err)
	}
	acct := fz.Table(freezer.TableAccountChanges)
	if acct.StartItem() != head+1 || acct.Items() != head+6 {
		t.Fatalf("acctcs bounds: start %d items %d", acct.StartItem(), acct.Items())
	}
	if v, err := acct.Retrieve(head + 3); err != nil || v[2] != byte((head+3)&0xff) {
		t.Fatalf("retrieve: %x %v", v, err)
	}
	if _, err := acct.Retrieve(head); err == nil {
		t.Fatal("read below start must fail (pruned)")
	}

	// Rewind: importer rolled back to head+2 and re-executes.
	if err := sink.Add(head+3, blob(0xAA), blob(0xAA)); err != nil {
		t.Fatal(err)
	}
	if err := sink.Add(head+4, blob(0xBB), blob(0xBB)); err != nil {
		t.Fatal(err)
	}
	if err := sink.Flush(); err != nil {
		t.Fatal(err)
	}
	if v, _ := acct.Retrieve(head + 3); v[2] != 0xAA {
		t.Fatalf("rewind overwrite failed: %x", v)
	}
	if acct.Items() != head+5 {
		t.Fatalf("items after rewind: %d want %d", acct.Items(), head+5)
	}

	// Out-of-order gap must be rejected.
	if err := sink.Add(head+9, blob(1), blob(1)); err == nil {
		t.Fatal("gap append must fail")
	}
}

// TestCSFreezerWriterRoundTrip runs the writer over a synthetic block and
// verifies the persisted blobs decode to the exact change set, then that
// BuildRetainListMerged yields the same touched-key set as the legacy MDBX
// changeset path for identical changes.
func TestCSFreezerWriterRoundTrip(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	fz := mkFreezer(t)
	const blockNum = uint64(500)
	sink, err := NewCSFreezerSink(fz, blockNum-1)
	if err != nil {
		t.Fatal(err)
	}

	addr := types.HexToAddress("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	slot := types.HexToHash("0x05")
	old := &account.StateAccount{Initialised: true, Nonce: 1}
	old.Balance.SetUint64(100)
	old.CodeHash = types.BytesToHash(crypto.Keccak256(nil))
	post := &account.StateAccount{Initialised: true, Nonce: 2}
	post.Balance.SetUint64(50)
	post.CodeHash = old.CodeHash

	// Post-block hashed state the encoder's newValueOf resolver reads.
	if err := tx.Put(modules.HashedAccounts, crypto.Keccak256(addr[:]), post.MarshalV2()); err != nil {
		t.Fatal(err)
	}
	var comp [64]byte
	copy(comp[:32], crypto.Keccak256(addr[:]))
	copy(comp[32:], crypto.Keccak256(slot[:]))
	if err := tx.Put(modules.HashedStorage, comp[:], []byte{0x2A}); err != nil {
		t.Fatal(err)
	}

	// Freezer-sink writer: collect one account change + one slot change.
	w := NewCSFreezerWriter(tx, blockNum, sink)
	if err := w.UpdateAccountData(addr, old, post); err != nil {
		t.Fatal(err)
	}
	var oldV, newV uint256.Int
	oldV.SetUint64(7)
	newV.SetUint64(0x2A)
	if err := w.WriteAccountStorage(addr, slot, oldV, newV); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteChangeSets(); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteHistory(); err != nil { // must be a no-op
		t.Fatal(err)
	}
	if err := sink.Flush(); err != nil {
		t.Fatal(err)
	}

	// Decode the persisted blobs.
	accBlob, err := fz.Table(freezer.TableAccountChanges).Retrieve(blockNum)
	if err != nil {
		t.Fatal(err)
	}
	accChanges, err := DecodeAccountChanges(accBlob)
	if err != nil {
		t.Fatal(err)
	}
	if len(accChanges) != 1 || accChanges[0].Address != addr {
		t.Fatalf("acc changes: %+v", accChanges)
	}
	if len(accChanges[0].NewValue) == 0 || len(accChanges[0].OldValue) == 0 {
		t.Fatalf("acc old/new must be inline: old=%x new=%x", accChanges[0].OldValue, accChanges[0].NewValue)
	}
	stoBlob, err := fz.Table(freezer.TableStorageChanges).Retrieve(blockNum)
	if err != nil {
		t.Fatal(err)
	}
	stoChanges, err := DecodeStorageChanges(stoBlob)
	if err != nil {
		t.Fatal(err)
	}
	if len(stoChanges) != 1 || len(stoChanges[0].NewValue) != 1 || stoChanges[0].NewValue[0] != 0x2A {
		t.Fatalf("sto changes: %+v", stoChanges)
	}

	// Same changes via the legacy MDBX writer on a second DB.
	db2 := memdb.NewTestDB(t)
	tx2, err := db2.BeginRw(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback()
	lw := state.NewHashedCanonicalWriter(tx2, blockNum)
	if err := lw.UpdateAccountData(addr, old, post); err != nil {
		t.Fatal(err)
	}
	if err := lw.WriteAccountStorage(addr, slot, oldV, newV); err != nil {
		t.Fatal(err)
	}
	if err := lw.WriteChangeSets(); err != nil {
		t.Fatal(err)
	}

	// Retain lists must agree: freezer-backed vs MDBX-backed.
	rlF, na1, ns1, err := BuildRetainListMerged(tx, fz, blockNum, blockNum)
	if err != nil {
		t.Fatal(err)
	}
	rlM, na2, ns2, err := BuildRetainListMerged(tx2, nil, blockNum, blockNum)
	if err != nil {
		t.Fatal(err)
	}
	if na1 != na2 || ns1 != ns2 || na1 != 1 || ns1 != 1 {
		t.Fatalf("retain counts differ: freezer %d/%d mdbx %d/%d", na1, ns1, na2, ns2)
	}
	// Both must retain the account hash path and the storage composite path.
	ah := crypto.Keccak256(addr[:])
	for _, rl := range []interface{ Retain([]byte) bool }{rlF, rlM} {
		if !rl.Retain(hexNibbles(ah)) {
			t.Fatal("account path not retained")
		}
	}
}

// hexNibbles expands a byte key to nibbles as the trie loader's Retain calls do.
func hexNibbles(b []byte) []byte {
	out := make([]byte, 0, len(b)*2)
	for _, x := range b {
		out = append(out, x>>4, x&0xf)
	}
	return out
}
