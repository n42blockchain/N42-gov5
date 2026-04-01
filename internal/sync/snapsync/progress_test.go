package snapsync

import (
	"bytes"
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func testDB(t *testing.T) kv.RwDB {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	return memdb.NewTestDB(t)
}

func TestSavePivotBlock(t *testing.T) {
	db := testDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SavePivotBlock(tx, 12345)
	}); err != nil {
		t.Fatal(err)
	}

	var loaded uint64
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		loaded, err = LoadPivotBlock(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if loaded != 12345 {
		t.Errorf("expected pivot block 12345, got %d", loaded)
	}
}

func TestSaveAccountCursor(t *testing.T) {
	db := testDB(t)
	cursor := bytes.Repeat([]byte{0xAB}, 20)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveAccountCursor(tx, cursor)
	}); err != nil {
		t.Fatal(err)
	}

	var loaded []byte
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		loaded, err = LoadAccountCursor(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(loaded, cursor) {
		t.Errorf("cursor mismatch: got %x, want %x", loaded, cursor)
	}
}

func TestSaveState(t *testing.T) {
	db := testDB(t)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return SaveState(tx, StateRunning)
	}); err != nil {
		t.Fatal(err)
	}

	var state string
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		var err error
		state, err = LoadState(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}

	if state != StateRunning {
		t.Errorf("expected state %q, got %q", StateRunning, state)
	}
}

func TestClearProgress(t *testing.T) {
	db := testDB(t)

	// Save some progress.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := SavePivotBlock(tx, 100); err != nil {
			return err
		}
		if err := SaveAccountCursor(tx, []byte{1, 2, 3}); err != nil {
			return err
		}
		return SaveState(tx, StateRunning)
	}); err != nil {
		t.Fatal(err)
	}

	// Clear all progress.
	if err := ClearProgress(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	// Verify cleared.
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		pivot, err := LoadPivotBlock(tx)
		if err != nil {
			return err
		}
		if pivot != 0 {
			t.Errorf("expected pivot 0 after clear, got %d", pivot)
		}

		cursor, err := LoadAccountCursor(tx)
		if err != nil {
			return err
		}
		if cursor != nil {
			t.Errorf("expected nil cursor after clear, got %x", cursor)
		}

		state, err := LoadState(tx)
		if err != nil {
			return err
		}
		if state != "" {
			t.Errorf("expected empty state after clear, got %q", state)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestParseAccountForTasks(t *testing.T) {
	emptyCodeHash := crypto.Keccak256Hash(nil)

	// Empty/nil data.
	valid, inc, code := parseAccountForTasks(nil)
	if !valid || inc != 0 || code != nil {
		t.Errorf("nil: valid=%v inc=%d code=%v", valid, inc, code)
	}

	// Invalid protobuf data.
	valid, inc, code = parseAccountForTasks([]byte{0xFF, 0xFF, 0xFF})
	if valid {
		t.Errorf("invalid: expected valid=false, got true")
	}

	// EOA: nonce=5, balance=1000, no code, no incarnation.
	eoa := account.StateAccount{
		Initialised: true,
		Nonce:       5,
		Balance:     *uint256.NewInt(1000),
		CodeHash:    emptyCodeHash,
	}
	eoaData, err := eoa.MarshalV2(), error(nil)
	if err != nil {
		t.Fatal(err)
	}
	valid, inc, code = parseAccountForTasks(eoaData)
	if !valid || inc != 0 || code != nil {
		t.Errorf("eoa: valid=%v inc=%d code=%v", valid, inc, code)
	}

	// Contract: incarnation=1, non-empty codeHash.
	contractCodeHash := types.BytesHash(crypto.Keccak256([]byte("contract code")))
	contract := account.StateAccount{
		Initialised: true,
		Nonce:       1,
		Balance:     *uint256.NewInt(0),
		CodeHash:    contractCodeHash,
		Incarnation: 1,
	}
	contractData, err := contract.MarshalV2(), error(nil)
	if err != nil {
		t.Fatal(err)
	}
	valid, inc, code = parseAccountForTasks(contractData)
	if !valid || inc != 1 {
		t.Errorf("contract: valid=%v expected inc=1, got %d", valid, inc)
	}
	if code == nil || !bytes.Equal(code, contractCodeHash[:]) {
		t.Errorf("contract: expected codeHash=%x, got %x", contractCodeHash[:], code)
	}

	// Contract with incarnation but empty codeHash (destroyed and recreated).
	destroyedContract := account.StateAccount{
		Initialised: true,
		Incarnation: 3,
		CodeHash:    emptyCodeHash,
	}
	destroyedData, err := destroyedContract.MarshalV2(), error(nil)
	if err != nil {
		t.Fatal(err)
	}
	valid, inc, code = parseAccountForTasks(destroyedData)
	if !valid || inc != 3 {
		t.Errorf("destroyed: valid=%v expected inc=3, got %d", valid, inc)
	}
	if code != nil {
		t.Errorf("destroyed: expected nil codeHash, got %x", code)
	}
}
