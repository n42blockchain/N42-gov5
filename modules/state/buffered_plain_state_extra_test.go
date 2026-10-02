// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"os"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
)

// TestPlainStateBufferStatsAndResetReadCache covers the buffer-level Stats
// (distinct from BufferSnapshot.Stats) and ResetReadCache.
func TestPlainStateBufferStatsAndResetReadCache(t *testing.T) {
	buf := NewPlainStateBuffer()

	addr := types.HexToAddress("0x1000000000000000000000000000000000000009")
	buf.CacheAccount(addr, &account.StateAccount{Nonce: 1})
	if _, ok := buf.LookupReadAccount(addr); !ok {
		t.Fatal("expected cached account before reset")
	}

	accounts, storage := buf.Stats()
	if accounts != 0 || storage != 0 {
		t.Fatalf("Stats on an unwritten buffer = %d, %d, want 0, 0", accounts, storage)
	}

	buf.ResetReadCache()
	if _, ok := buf.LookupReadAccount(addr); ok {
		t.Fatal("ResetReadCache did not clear the read account cache")
	}
}

// TestAuditLogHelpers exercises ensureAuditLog/auditWrite/FlushAuditLog.
// These write to ./audit_nil.log (relative to the test binary's working
// directory); the file is removed afterward so the repo stays clean.
func TestAuditLogHelpers(t *testing.T) {
	t.Cleanup(func() { _ = os.Remove("audit_nil.log") })

	auditWrite("test entry %d\n", 1)
	FlushAuditLog()

	data, err := os.ReadFile("audit_nil.log")
	if err != nil {
		t.Fatalf("expected audit_nil.log to exist after auditWrite+FlushAuditLog: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("audit_nil.log is empty after a write")
	}
}

// TestBufferedPlainStateReaderCodeSourceAndCodeSize covers SetCodeSource,
// ReadAccountCode (via the code source), and ReadAccountCodeSize.
func TestBufferedPlainStateReaderCodeSourceAndCodeSize(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	buf := NewPlainStateBuffer()
	reader := NewBufferedPlainStateReader(buf, tx)

	var addr types.Address
	addr[0] = 0x55
	code := []byte{0x60, 0x01, 0x60, 0x02}
	codeHash := crypto.Keccak256Hash(code)

	reader.SetCodeSource(stubCodeSource{code: code})

	got, err := reader.ReadAccountCode(addr, codeHash)
	if err != nil {
		t.Fatalf("ReadAccountCode: %v", err)
	}
	if string(got) != string(code) {
		t.Fatalf("ReadAccountCode = %x, want %x", got, code)
	}

	size, err := reader.ReadAccountCodeSize(addr, codeHash)
	if err != nil {
		t.Fatalf("ReadAccountCodeSize: %v", err)
	}
	if size != len(code) {
		t.Fatalf("ReadAccountCodeSize = %d, want %d", size, len(code))
	}

	// Empty code hash short-circuits to nil, 0 without consulting any source.
	empty, err := reader.ReadAccountCode(addr, types.BytesToHash(emptyCodeHash))
	if err != nil || empty != nil {
		t.Fatalf("ReadAccountCode(emptyCodeHash) = %v, %v, want nil, nil", empty, err)
	}
}

// TestBufferedPlainStateForEachStorageEnumerates covers the enumeration
// path (active-buffer slots visible through the memdb cursor-backed base)
// and the ErrNoStorageEnumeration fallback for a base without Cursor.
func TestBufferedPlainStateForEachStorageEnumerates(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	buf := NewPlainStateBuffer()
	writer := NewBufferedPlainStateWriterNoHistory(buf)
	reader := NewBufferedPlainStateReader(buf, tx)

	var addr types.Address
	addr[0] = 0x66
	slot := types.HexToHash("0x01")
	if err := writer.WriteAccountStorage(addr, slot, uint256.Int{}, *uint256.NewInt(42)); err != nil {
		t.Fatalf("WriteAccountStorage: %v", err)
	}

	seen := map[types.Hash][]byte{}
	if err := reader.ForEachStorage(addr, func(s types.Hash, v []byte) bool {
		seen[s] = v
		return true
	}); err != nil {
		t.Fatalf("ForEachStorage: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("ForEachStorage visited %d slots, want 1", len(seen))
	}

	// A base reader without Cursor support cannot enumerate.
	reader2 := NewBufferedPlainStateReader(buf, noCursorGetter{})
	if err := reader2.ForEachStorage(addr, func(types.Hash, []byte) bool { return true }); err != ErrNoStorageEnumeration {
		t.Fatalf("ForEachStorage(no cursor) err = %v, want ErrNoStorageEnumeration", err)
	}
}

// TestBufferedPlainStateWriterNoHistoryIsNoop covers WriteChangeSets/
// WriteHistory/RefreshLRUForBlock on a writer built without a ChangeSetWriter.
func TestBufferedPlainStateWriterNoHistoryIsNoop(t *testing.T) {
	buf := NewPlainStateBuffer()
	w := NewBufferedPlainStateWriterNoHistory(buf)

	if err := w.WriteChangeSets(); err != nil {
		t.Fatalf("WriteChangeSets (no history) = %v, want nil", err)
	}
	if err := w.WriteHistory(); err != nil {
		t.Fatalf("WriteHistory (no history) = %v, want nil", err)
	}

	// No touched entries yet -- RefreshLRUForBlock must be a safe no-op.
	w.RefreshLRUForBlock()

	var addr types.Address
	addr[0] = 0x77
	orig := account.StateAccount{}
	acct := account.StateAccount{Nonce: 1}
	if err := w.UpdateAccountData(addr, &orig, &acct); err != nil {
		t.Fatalf("UpdateAccountData: %v", err)
	}
	w.RefreshLRUForBlock()
	if _, ok := buf.LookupReadAccount(addr); !ok {
		t.Fatal("RefreshLRUForBlock did not promote the touched account into the read LRU")
	}
}

// stubCodeSource implements CodeSource for the test above.
type stubCodeSource struct{ code []byte }

func (s stubCodeSource) GetCode(types.Address) ([]byte, error) { return s.code, nil }

// noCursorGetter implements kv.Getter but not cursorProvider.
type noCursorGetter struct{}

func (noCursorGetter) GetOne(string, []byte) ([]byte, error) { return nil, nil }
func (noCursorGetter) Has(string, []byte) (bool, error)      { return false, nil }
func (noCursorGetter) ForEach(string, []byte, func(k, v []byte) error) error {
	return nil
}
func (noCursorGetter) ForPrefix(string, []byte, func(k, v []byte) error) error {
	return nil
}
func (noCursorGetter) ForAmount(string, []byte, uint32, func(k, v []byte) error) error {
	return nil
}
