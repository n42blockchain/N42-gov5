package commitment

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// TestUnwindPlainStateNewAccountDeletion reproduces the live-fire "nonce too
// low" wedge shape: a block CREATES a fresh account (a txgen test account's
// first transactions), the branch is unwound, and the account must be GONE
// from PlainState afterwards — a leftover account keeps a ghost nonce that
// makes every future replay of that account's early transactions fail with
// nonce-too-low, wedging the node permanently.
func TestUnwindPlainStateNewAccountDeletion(t *testing.T) {
	db := memdb.NewTestDB(t)
	tx, _ := db.BeginRw(context.Background())
	defer tx.Rollback()

	emptyCH := crypto.Keccak256Hash(nil)
	var fresh, existing types.Address
	fresh[19], existing[19] = 0xF1, 0xE1

	// Pre-state: only `existing` is present (nonce 7).
	e0 := &account.StateAccount{Initialised: true, Nonce: 7, CodeHash: emptyCH}
	e0.Balance.SetUint64(500)
	if err := tx.Put(modules.Account, existing[:], e0.MarshalV2()); err != nil {
		t.Fatal(err)
	}

	// Block 42 execution: `fresh` is created with nonce 31 (it sent 31 txs in
	// this block), `existing` bumps to nonce 8. Mirror CommitBlock's writer
	// calls, then persist the changesets exactly like writeBlockWithState.
	const blockNum = 42
	w := state.NewPlainStateWriter(tx, tx, blockNum)

	f1 := &account.StateAccount{Initialised: true, Nonce: 31, CodeHash: emptyCH}
	f1.Balance.SetUint64(12345)
	if err := w.UpdateAccountData(fresh, &account.StateAccount{}, f1); err != nil { // new account: empty original
		t.Fatal(err)
	}
	e1 := &account.StateAccount{Initialised: true, Nonce: 8, CodeHash: emptyCH}
	e1.Balance.SetUint64(400)
	if err := w.UpdateAccountData(existing, e0, e1); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteAccountStorage(fresh, types.Hash{31: 1}, *uint256.NewInt(0), *uint256.NewInt(9)); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteChangeSets(); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteHistory(); err != nil {
		t.Fatal(err)
	}

	// Forward state sanity: fresh exists with nonce 31.
	if v, _ := tx.GetOne(modules.Account, fresh[:]); len(v) == 0 {
		t.Fatal("forward: fresh account missing from PlainState")
	}

	// Unwind block 42.
	if err := UnwindPlainStateBlock(tx, blockNum); err != nil {
		t.Fatalf("unwind: %v", err)
	}

	// The freshly created account must be DELETED — not left with nonce 31.
	if v, _ := tx.GetOne(modules.Account, fresh[:]); len(v) != 0 {
		var got account.StateAccount
		_ = got.DecodeForStorageV2(v)
		t.Fatalf("unwind left ghost account: nonce=%d (want deleted)", got.Nonce)
	}
	// The pre-existing account must be restored to nonce 7.
	v, _ := tx.GetOne(modules.Account, existing[:])
	if len(v) == 0 {
		t.Fatal("unwind deleted a pre-existing account")
	}
	var e account.StateAccount
	if err := e.DecodeForStorageV2(v); err != nil {
		t.Fatal(err)
	}
	if e.Nonce != 7 {
		t.Fatalf("existing account nonce after unwind = %d, want 7", e.Nonce)
	}
	// The fresh account's storage must be gone too.
	ck := modules.PlainGenerateCompositeStorageKey(fresh.Bytes(), types.Hash{31: 1}.Bytes())
	if v, _ := tx.GetOne(modules.Storage, ck); len(v) != 0 {
		t.Fatal("unwind left ghost storage slot")
	}
}
