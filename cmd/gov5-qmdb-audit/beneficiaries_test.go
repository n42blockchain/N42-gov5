package main

import (
	"bytes"
	"errors"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func TestAuditEmptyBeneficiaryRequiresExactAccountAndMissingPlainRow(t *testing.T) {
	db := auditTestDB(t)
	addr := types.Address{19: 9}
	key := qmdb.Hash(commitment.AccountKeyHash(addr))
	known := map[qmdb.Hash]types.Address{key: addr}
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		for _, tc := range []struct {
			name  string
			value []byte
			known map[qmdb.Hash]types.Address
		}{
			{"unknown-zero", []byte{0}, nil}, {"nonempty-account", []byte{1, 1}, known},
			{"storage-sized-zero", make([]byte, 32), known}, {"noncanonical-empty", []byte{0, 0}, known},
			{"wrong-preimage", []byte{0}, map[qmdb.Hash]types.Address{key: {19: 8}}},
		} {
			live := map[qmdb.Hash]liveValue{key: {slot: 17, value: tc.value}}
			if _, err := auditEmptyBeneficiaries(tx, live, tc.known); err == nil || len(live) != 1 {
				t.Fatalf("%s accepted or consumed unverified entry: %v", tc.name, err)
			}
		}
		live := map[qmdb.Hash]liveValue{key: {slot: 17, value: []byte{0}}}
		report, err := auditEmptyBeneficiaries(tx, live, known)
		if err != nil || len(report) != 1 || len(live) != 0 || report[0].Address != addr || report[0].ValueHex != "00" {
			t.Fatalf("exact empty beneficiary not classified: %+v %v", report, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(modules.Account, addr[:], []byte{0}) }); err != nil {
		t.Fatal(err)
	}
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		_, err := auditEmptyBeneficiaries(tx, map[qmdb.Hash]liveValue{key: {value: []byte{0}}}, known)
		if err == nil {
			t.Fatal("accepted existing empty PlainState row as absent")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAuditForestClassifiesRetainedEmptyBeneficiaryWithoutChangingRoot(t *testing.T) {
	db, genesis := auditTestDB(t), auditTestDB(t)
	funded, beneficiary := types.Address{19: 1}, types.Address{19: 2}
	seed := func(tx kv.RwTx) error {
		a := account.NewAccount()
		a.Balance.SetUint64(1)
		if err := tx.Put(modules.Account, funded[:], a.MarshalV2()); err != nil {
			return err
		}
		_, err := internal.SeedQMDBGenesis(tx)
		return err
	}
	for _, d := range []kv.RwDB{db, genesis} {
		if err := d.Update(t.Context(), seed); err != nil {
			t.Fatal(err)
		}
	}
	rc := commitment.NewQMDBRootComputer()
	if err := db.View(t.Context(), func(tx kv.Tx) error { rc.SetCold(tx); defer rc.SetCold(nil); return rc.LoadFrom(tx) }); err != nil {
		t.Fatal(err)
	}
	var root types.Hash
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		rc.SetCold(tx)
		defer rc.SetCold(nil)
		a := account.NewAccount()
		a.Reset() // real stateObject snapshots carry Initialised=true
		if !bytes.Equal(a.MarshalV2(), []byte{0}) {
			t.Fatal("empty account encoding changed")
		}
		var err error
		root, err = rc.ComputeRoot(map[types.Address]*account.StateAccount{beneficiary: &a}, nil)
		if err != nil {
			return err
		}
		_, err = rc.FlushTo(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	gtx, err := genesis.BeginRo(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer gtx.Rollback()
	known := map[qmdb.Hash]types.Address{qmdb.Hash(commitment.AccountKeyHash(beneficiary)): beneficiary}
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		if _, err := auditForest(tx, gtx, root); !errors.Is(err, errUnmatchedPlainState) {
			t.Fatalf("strict path changed: %v", err)
		}
		r, err := auditForestWithBeneficiaries(tx, gtx, root, known)
		if err != nil || r.Root != root || r.LiveKeys != 2 || len(r.EmptyBeneficiaries) != 1 {
			t.Fatalf("root/count/exception lost: %+v %v", r, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A structural PlainState error must not be mistaken for unmatched keys.
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { return tx.Put(modules.Storage, []byte{9}, []byte{1}) }); err != nil {
		t.Fatal(err)
	}
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		if _, err := auditForestWithBeneficiaries(tx, gtx, root, known); err == nil {
			t.Fatal("beneficiary exception hid malformed storage")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUsedBeneficiariesRequiresExactFullAncestryAndFixedValidators(t *testing.T) {
	db := auditTestDB(t)
	addr := types.Address{19: 7}
	g := &block.Header{Number: uint256.NewInt(0), Difficulty: uint256.NewInt(0)}
	h := &block.Header{Number: uint256.NewInt(1), Difficulty: uint256.NewInt(0), ParentHash: g.Hash(), Coinbase: addr, GasUsed: 21000}
	if err := db.Update(t.Context(), func(tx kv.RwTx) error { rawdb.WriteHeader(tx, g); rawdb.WriteHeader(tx, h); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := db.View(t.Context(), func(tx kv.Tx) error {
		r, err := usedBeneficiaries(tx, h, g.Hash(), map[types.Address]bool{addr: true})
		if err != nil || len(r) != 1 {
			t.Fatalf("valid ancestry rejected: %v", err)
		}
		if _, err := usedBeneficiaries(tx, h, g.Hash(), nil); err == nil {
			t.Fatal("accepted non-validator beneficiary")
		}
		if _, err := usedBeneficiaries(tx, h, types.Hash{31: 9}, map[types.Address]bool{addr: true}); err == nil {
			t.Fatal("accepted other genesis")
		}
		bad := block.CopyHeader(h)
		bad.ParentHash = types.Hash{31: 8}
		if _, err := usedBeneficiaries(tx, bad, g.Hash(), map[types.Address]bool{addr: true}); err == nil {
			t.Fatal("accepted missing parent")
		}
		empty := block.CopyHeader(h)
		empty.GasUsed = 0
		if r, err := usedBeneficiaries(tx, empty, g.Hash(), map[types.Address]bool{addr: true}); err != nil || len(r) != 0 {
			t.Fatal("empty block authorized beneficiary exception")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
