package state

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

type historicalCursorErrorTx struct {
	kv.Tx
	err error
}

func (tx *historicalCursorErrorTx) Cursor(string) (kv.Cursor, error) {
	return nil, tx.err
}

// TestHashedHistoricalReader verifies the hashed-canonical state-as-of reader:
// a value that changed at block 5 must read as its pre-block-5 (old) value for
// any query timestamp ≤ 5, and as the tip value (from HashedAccounts) once the
// query is past the last change. It exercises the same FindByHistory + hashed
// base path the RPC uses, populated through the real ChangeSetWriter.
func TestHashedHistoricalReader(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()

	var addr types.Address
	addr[0] = 0xAB
	addrHash := crypto.Keccak256Hash(addr[:])

	old := &account.StateAccount{Initialised: true, Nonce: 1, Balance: *uint256.NewInt(50)}
	cur := &account.StateAccount{Initialised: true, Nonce: 2, Balance: *uint256.NewInt(100)}

	// Tip hashed state: account at balance 100 (post-block-5).
	curEnc := make([]byte, cur.EncodingLengthForStorage())
	cur.EncodeForStorage(curEnc)
	if err := tx.Put(modules.HashedAccounts, addrHash[:], curEnc); err != nil {
		t.Fatalf("put hashed account: %v", err)
	}

	// Block 5 changeset + history index: account changed 50 -> 100 at block 5.
	w := NewChangeSetWriterPlain(tx, 5)
	if err := w.UpdateAccountData(addr, old, cur); err != nil {
		t.Fatalf("update account: %v", err)
	}
	if err := w.WriteChangeSets(); err != nil {
		t.Fatalf("write changesets: %v", err)
	}
	if err := w.WriteHistory(); err != nil {
		t.Fatalf("write history: %v", err)
	}

	// As-of the start of block 5 (post-state of block 4): the OLD value (50).
	r5 := NewHashedHistoricalReader(tx, 5)
	a5, err := r5.ReadAccountData(addr)
	if err != nil {
		t.Fatalf("read @5: %v", err)
	}
	if a5 == nil || a5.Balance.Uint64() != 50 {
		t.Fatalf("as-of block 5 balance = %v, want 50", a5)
	}

	// Past the last change (post-state of block 5): the tip value (100).
	r6 := NewHashedHistoricalReader(tx, 6)
	a6, err := r6.ReadAccountData(addr)
	if err != nil {
		t.Fatalf("read @6: %v", err)
	}
	if a6 == nil || a6.Balance.Uint64() != 100 {
		t.Fatalf("post block 5 balance = %v, want 100 (tip)", a6)
	}

	// An address that never changed falls through to the (empty) tip → nil.
	var other types.Address
	other[0] = 0xCD
	if a, err := r6.ReadAccountData(other); err != nil || a != nil {
		t.Fatalf("unknown account: got (%v, %v), want (nil, nil)", a, err)
	}
}

func TestHashedHistoricalReaderReportsCursorInitError(t *testing.T) {
	wantErr := errors.New("injected cursor failure")
	r := NewHashedHistoricalReader(&historicalCursorErrorTx{err: wantErr}, 1)
	if _, err := r.ReadAccountData(types.Address{}); !errors.Is(err, wantErr) {
		t.Fatalf("ReadAccountData error = %v, want %v", err, wantErr)
	}
}
