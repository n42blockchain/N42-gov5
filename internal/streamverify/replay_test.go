// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package streamverify

import (
	"bytes"
	"errors"
	"testing"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// The replay recorder answers from the recorded values by key, in whatever
// order the rebuild asks, never touching the original reader, and logs only
// what the rebuild reads.
func TestReplayRecorder_ServesRecordedValuesByKey(t *testing.T) {
	a, b, missing := types.Address{0xa}, types.Address{0xb}, types.Address{0xc}
	slot := types.HexToHash("0x07")
	known := types.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	fresh := types.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	accA := account.NewAccount()
	accA.Initialised, accA.Nonce, accA.CodeHash = true, 3, fresh
	accA.Balance.SetUint64(99)
	inner := &stubReader{
		accounts: map[types.Address]*account.StateAccount{a: &accA},
		storage:  map[types.Address]map[types.Hash][]byte{a: {slot: {0x42}}},
		codes:    map[types.Hash][]byte{known: {0x60, 0x00}, fresh: {0xfe}},
	}
	rec := NewReadLogRecorder(inner, map[types.Hash]struct{}{known: {}})
	// The build: a thrown-away read of b first, then a, its slot and codes.
	_, _ = rec.ReadAccountData(b)
	_, _ = rec.ReadAccountData(a)
	_, _ = rec.ReadAccountStorage(a, &slot)
	_, _ = rec.ReadAccountCode(a, known)
	_, _ = rec.ReadAccountCode(a, fresh)
	rec.MarkDiverged()
	if !rec.Diverged() {
		t.Fatal("MarkDiverged did not stick")
	}

	inner.err = errors.New("the build's database transaction is closed")
	replay, recorded := rec.ReplayRecorder()

	// The rebuild reads in its own order and skips b.
	v, err := replay.ReadAccountStorage(a, &slot)
	if err != nil || !bytes.Equal(v, []byte{0x42}) {
		t.Fatalf("storage = %x, %v", v, err)
	}
	got, err := replay.ReadAccountData(a)
	if err != nil || got == nil || got.Nonce != 3 || got.Balance.Uint64() != 99 || got.CodeHash != fresh {
		t.Fatalf("account = %+v, %v", got, err)
	}
	if code, err := replay.ReadAccountCode(a, known); err != nil || !bytes.Equal(code, []byte{0x60, 0x00}) {
		t.Fatalf("known code = %x, %v", code, err)
	}
	if code, err := replay.ReadAccountCode(a, fresh); err != nil || !bytes.Equal(code, []byte{0xfe}) {
		t.Fatalf("captured code = %x, %v", code, err)
	}
	if recorded.Err() != nil {
		t.Fatalf("unexpected reader error: %v", recorded.Err())
	}
	log := replay.Log()
	if len(log) != 2 {
		t.Fatalf("rebuilt log has %d reads, want 2 (slot, account)", len(log))
	}
	if _, ok := log[0].(evmsdk.ReadLogStorage); !ok {
		t.Fatalf("read 0 is %T, want the slot", log[0])
	}
	if e, ok := log[1].(evmsdk.ReadLogAccount); !ok || e.Nonce != 3 {
		t.Fatalf("read 1 is %+v, want account a", log[1])
	}
	// Only the code the rebuild read and the consumer does not know ships.
	if c := replay.UncachedBytecodes(); len(c) != 1 || c[fresh] == nil {
		t.Fatalf("rebuilt bytecodes = %v, want only the uncached one", c)
	}

	// b was recorded as absent; c was never read: refuse, do not guess.
	if acc, err := replay.ReadAccountData(b); err != nil || acc != nil {
		t.Fatalf("recorded-absent account = %+v, %v", acc, err)
	}
	if _, err := replay.ReadAccountData(missing); !errors.Is(err, ErrNotRecorded) {
		t.Fatalf("unrecorded account err = %v, want ErrNotRecorded", err)
	}
	if !errors.Is(recorded.Err(), ErrNotRecorded) {
		t.Fatalf("reader Err = %v, want ErrNotRecorded", recorded.Err())
	}

	rec.Reset()
	if rec.Diverged() {
		t.Fatal("Reset left the recorder diverged")
	}
}
