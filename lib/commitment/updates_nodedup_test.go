// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers Updates.TouchPlainKeyNoDedup (the bulk-bootstrap path that skips the
// t.keys dedup map in ModeDirect, and falls back to TouchPlainKey's dedup-aware
// path in ModeUpdate) and Update.Copy's deep-copy semantics.

package commitment

import (
	"bytes"
	"context"
	"sort"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/lib/common/empty"
)

func TestTouchPlainKeyNoDedupModeDirect(t *testing.T) {
	ut := NewUpdates(ModeDirect, t.TempDir(), keyHasherNoop)

	keys := [][]byte{
		hexDec("c17fa85f22306d37cec90b0ec74c5623dbbac68f"),
		hexDec("553bba1d92398a69fbc9f01593bbc51b58862366"),
		hexDec("2452345febefe553bba1d92398a69fbc9f01593b"),
	}
	for _, k := range keys {
		ut.TouchPlainKeyNoDedup(string(k), []byte("v"), ut.TouchStorage)
	}
	// NOTE: Size() is NOT meaningful for TouchPlainKeyNoDedup in ModeDirect —
	// by design it skips t.keys (the dedup map Size() reads for ModeDirect),
	// so Size() stays 0 regardless of how many keys were touched. HashSort,
	// which reads the etl collector that TouchPlainKeyNoDedup DOES populate,
	// is the correct way to observe what was touched.

	sorted := append([][]byte{}, keys...)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i], sorted[j]) < 0 })

	warmuper := NewWarmuper(context.Background(), WarmupConfig{})
	i := 0
	err := ut.HashSort(context.Background(), warmuper, func(hk, pk []byte, upd *Update) error {
		if !bytes.Equal(sorted[i], pk) {
			t.Fatalf("at %d: pk = %x, want %x", i, pk, sorted[i])
		}
		i++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if i != len(keys) {
		t.Fatalf("HashSort visited %d keys, want %d", i, len(keys))
	}
}

// TestTouchPlainKeyNoDedupModeUpdateFallbackPanics documents a found defect:
// the doc comment on TouchPlainKeyNoDedup says ModeUpdate falls back to the
// dedup-aware TouchPlainKey "to preserve correctness", but the fallback call
// is `t.TouchPlainKey(key, nil, nil)` — it hardcodes a NIL callback instead of
// forwarding the caller's fn. TouchPlainKey's ModeUpdate branch unconditionally
// invokes fn(item, val) for a not-yet-seen key, so this is a guaranteed nil
// pointer dereference the first time TouchPlainKeyNoDedup is ever called in
// ModeUpdate. Left unfixed per task scope; this test pins the current
// (buggy) behavior so a future fix is a deliberate, visible change.
func TestTouchPlainKeyNoDedupModeUpdateFallbackPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected TouchPlainKeyNoDedup in ModeUpdate to panic (nil fn forwarded to TouchPlainKey) -- if this no longer panics, the defect noted above has been fixed and this test should be updated")
		}
	}()
	ut := NewUpdates(ModeUpdate, t.TempDir(), keyHasherNoop)
	key := hexDec("553bba1d92398a69fbc9f01593bbc51b58862366")
	ut.TouchPlainKeyNoDedup(string(key), []byte("v1"), ut.TouchStorage)
}

func TestUpdateCopy(t *testing.T) {
	var nilUpd *Update
	if nilUpd.Copy() != nil {
		t.Fatal("Copy() on a nil *Update must return nil")
	}

	u := &Update{
		CodeHash:   empty.CodeHash,
		Flags:      BalanceUpdate,
		StorageLen: 3,
		Nonce:      7,
	}
	u.Balance = *uint256.NewInt(12345)
	copy(u.Storage[:3], []byte{1, 2, 3})

	c := u.Copy()
	if c == u {
		t.Fatal("Copy() must return a distinct instance")
	}
	if c.Nonce != u.Nonce || c.Flags != u.Flags || c.StorageLen != u.StorageLen || c.CodeHash != u.CodeHash {
		t.Fatalf("Copy() field mismatch: got %+v, want fields of %+v", c, u)
	}
	if c.Balance.Cmp(&u.Balance) != 0 {
		t.Fatalf("Copy() balance = %v, want %v", c.Balance, u.Balance)
	}

	// Mutating the copy's balance must not affect the original (deep copy).
	c.Balance.SetUint64(999)
	if u.Balance.Cmp(uint256.NewInt(12345)) != 0 {
		t.Fatal("Copy() must deep-copy Balance; mutating the copy changed the original")
	}
}
