// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

//go:build integration

package state

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/quick"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
)

func TestSnapshotRandom(t *testing.T) {
	config := &quick.Config{MaxCount: 1000}
	err := quick.Check((*snapshotTest).run, config)
	if cerr, ok := err.(*quick.CheckError); ok {
		test := cerr.In[0].(*snapshotTest)
		t.Errorf("%v:\n%s", test.err, test)
	} else if err != nil {
		t.Error(err)
	}
}

func TestFinalizeTxDeletesCreatedSelfdestructedContract(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	addr := types.HexToAddress("0x10000000000000000000000000000000000000aa")
	beneficiary := types.HexToAddress("0x10000000000000000000000000000000000000bb")
	sentinel := types.HexToAddress("0x10000000000000000000000000000000000000dd")
	db := memdb.NewTestDB(t)

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := NewPlainStateWriter(tx, tx, 1)
		orig := account.NewAccount()
		sentinelAccount := account.NewAccount()
		sentinelAccount.Nonce = 1
		if err := writer.UpdateAccountData(sentinel, &orig, &sentinelAccount); err != nil {
			return err
		}

		statedb := New(NewPlainState(tx, 1))
		statedb.CreateAccount(addr, true)
		statedb.SetCode(addr, []byte{0x60, 0x00})
		statedb.SetBalance(addr, uint256.NewInt(3))
		statedb.AddBalance(beneficiary, uint256.NewInt(3))
		statedb.Selfdestruct6780(addr, beneficiary)

		if !statedb.HasSelfdestructed(addr) {
			t.Fatal("expected account to be marked selfdestructed before finalization")
		}

		if err := statedb.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsCancun: true}, writer); err != nil {
			return err
		}

		if statedb.Exist(addr) {
			t.Fatal("expected created+selfdestructed account to be removed after FinalizeTx")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = db.View(context.Background(), func(tx kv.Tx) error {
		reloaded := New(NewPlainStateReader(tx))
		if reloaded.Exist(addr) {
			t.Fatal("expected created+selfdestructed account to stay deleted when reloaded")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestFinalizeTxDeletesNewAccountNetZeroedByTransfer covers the non-selfdestruct
// analogue of reth's "elide empty new accounts from hashed state": an account
// that did not exist before the block is funded (a plain transfer in) and then
// has its whole balance transferred back out within the same block, netting to
// empty (nonce 0, balance 0, no code). EIP-158 must emit a deletion at tx end,
// not an empty leaf — otherwise a stale zero-value leaf pollutes the hashed
// state and corrupts the root. The delete decision must not depend on whether
// the account pre-existed.
func TestFinalizeTxDeletesNewAccountNetZeroedByTransfer(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	newAddr := types.HexToAddress("0x20000000000000000000000000000000000000aa")
	drain := types.HexToAddress("0x20000000000000000000000000000000000000bb")
	db := memdb.NewTestDB(t)

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := NewPlainStateWriter(tx, tx, 1)
		statedb := New(NewPlainState(tx, 1))

		// newAddr is created purely by receiving a transfer this block, then the
		// full amount leaves again — no CreateAccount, no code, no nonce bump.
		statedb.AddBalance(newAddr, uint256.NewInt(5))
		if !statedb.Exist(newAddr) {
			t.Fatal("expected newAddr to exist after being funded")
		}
		statedb.SubBalance(newAddr, uint256.NewInt(5))
		statedb.AddBalance(drain, uint256.NewInt(5))

		if err := statedb.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsCancun: true}, writer); err != nil {
			return err
		}

		if statedb.Exist(newAddr) {
			t.Fatal("expected net-zeroed new account to be removed after FinalizeTx, not written as an empty leaf")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = db.View(context.Background(), func(tx kv.Tx) error {
		reloaded := New(NewPlainStateReader(tx))
		if reloaded.Exist(newAddr) {
			t.Fatal("expected net-zeroed new account to stay deleted when reloaded")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCodeHashesSkipsCreatedSelfdestructedContract(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	addr := types.HexToAddress("0x10000000000000000000000000000000000000cc")
	db := memdb.NewTestDB(t)

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		statedb := New(NewPlainState(tx, 1))
		statedb.CreateAccount(addr, true)
		statedb.SetCode(addr, []byte{0x60, 0x00})
		statedb.SetBalance(addr, uint256.NewInt(1))
		statedb.Selfdestruct6780(addr, types.Address{})
		statedb.stateObjectsDirty[addr] = struct{}{}
		statedb.BeginWriteCodes()

		if got := statedb.CodeHashes(); len(got) != 0 {
			t.Fatalf("expected no code hashes for created+selfdestructed contract, got %d", len(got))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeTxPreCancunDeletesCreatedSelfdestructedContract(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	addr := types.HexToAddress("0x10000000000000000000000000000000000000de")
	db := memdb.NewTestDB(t)

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		statedb := New(NewPlainState(tx, 1))
		writer := NewPlainStateWriter(tx, tx, 1)

		statedb.CreateAccount(addr, true)
		statedb.SetCode(addr, []byte{0x60, 0x00})
		statedb.SetBalance(addr, uint256.NewInt(1))
		statedb.Selfdestruct6780(addr, types.Address{})

		if err := statedb.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, writer); err != nil {
			return err
		}

		if statedb.Exist(addr) {
			t.Fatal("expected pre-Cancun created+selfdestructed contract to be deleted at tx end")
		}
		if statedb.GetCodeSize(addr) != 0 {
			t.Fatal("expected deleted pre-Cancun contract code to be cleared")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFinalizeTxClearsCreatedFlagForSurvivingContract(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	addr := types.HexToAddress("0x10000000000000000000000000000000000000ee")
	db := memdb.NewTestDB(t)

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		statedb := New(NewPlainState(tx, 1))
		statedb.CreateAccount(addr, true)
		statedb.SetCode(addr, []byte{0x60, 0x00})

		if !statedb.WasCreatedInCurrentTx(addr) {
			t.Fatal("expected contract to be marked created before finalization")
		}
		if err := statedb.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsCancun: true}, NewNoopWriter()); err != nil {
			return err
		}
		if statedb.WasCreatedInCurrentTx(addr) {
			t.Fatal("expected created flag to be cleared after finalization")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSelfdestruct6780DoesNotDeleteContractCreatedInPreviousTx(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() {
		kv.ChaindataTablesCfg = prevTables
	})

	addr := types.HexToAddress("0x10000000000000000000000000000000000000ef")
	beneficiary := types.HexToAddress("0x10000000000000000000000000000000000000f0")
	db := memdb.NewTestDB(t)

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		statedb := New(NewPlainState(tx, 1))
		writer := NewPlainStateWriter(tx, tx, 1)

		statedb.CreateAccount(addr, true)
		statedb.SetCode(addr, []byte{0x60, 0x00})
		statedb.SetBalance(addr, uint256.NewInt(7))
		if err := statedb.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsCancun: true}, writer); err != nil {
			return err
		}

		if statedb.WasCreatedInCurrentTx(addr) {
			t.Fatal("expected created flag to be cleared before the next transaction")
		}

		statedb.AddBalance(beneficiary, uint256.NewInt(7))
		statedb.Selfdestruct6780(addr, beneficiary)
		if err := statedb.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsCancun: true}, writer); err != nil {
			return err
		}

		if !statedb.Exist(addr) {
			t.Fatal("expected contract created in a previous tx to survive EIP-6780 selfdestruct")
		}
		if statedb.GetCodeSize(addr) == 0 {
			t.Fatal("expected surviving contract code to remain after EIP-6780 selfdestruct")
		}
		if !statedb.GetBalance(addr).IsZero() {
			t.Fatal("expected selfdestructed contract balance to be cleared")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestSelfDestructStorageWipe verifies that SELFDESTRUCT + CreateAccount at the same
// address clears all old storage. This is the core correctness property that incarnation
// was designed to guarantee — we must maintain it after removing incarnation.
func TestSelfDestructStorageWipe(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	addr := types.HexToAddress("0x1000000000000000000000000000000000000001")
	beneficiary := types.HexToAddress("0x1000000000000000000000000000000000000002")
	slot0 := types.Hash{0x00}
	slot1 := types.Hash{0x01}
	db := memdb.NewTestDB(t)

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := NewPlainStateWriter(tx, tx, 1)

		// Block 1: deploy contract, write storage.
		sdb := New(NewPlainState(tx, 1))
		sdb.CreateAccount(addr, true)
		sdb.SetCode(addr, []byte{0x60, 0x00})
		sdb.SetBalance(addr, uint256.NewInt(100))
		sdb.SetState(addr, &slot0, *uint256.NewInt(42))
		sdb.SetState(addr, &slot1, *uint256.NewInt(99))
		if err := sdb.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, writer); err != nil {
			return err
		}
		if err := sdb.CommitBlock(&params.Rules{IsSpuriousDragon: true}, writer); err != nil {
			return err
		}

		// Verify storage was written.
		var v0, v1 uint256.Int
		sdb.GetState(addr, &slot0, &v0)
		sdb.GetState(addr, &slot1, &v1)
		if v0 != *uint256.NewInt(42) || v1 != *uint256.NewInt(99) {
			t.Fatalf("storage not written: slot0=%v slot1=%v", v0, v1)
		}

		// Block 2: SELFDESTRUCT the contract (pre-Cancun behavior: full wipe).
		sdb.AddBalance(beneficiary, uint256.NewInt(100))
		sdb.Selfdestruct(addr)
		if err := sdb.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, writer); err != nil {
			return err
		}
		if err := sdb.CommitBlock(&params.Rules{IsSpuriousDragon: true}, writer); err != nil {
			return err
		}

		// Block 3: re-create contract at same address.
		sdb.CreateAccount(addr, true)
		sdb.SetCode(addr, []byte{0x60, 0x01})
		sdb.SetBalance(addr, uint256.NewInt(50))

		// CRITICAL: old storage MUST be gone.
		sdb.GetState(addr, &slot0, &v0)
		sdb.GetState(addr, &slot1, &v1)
		if !v0.IsZero() {
			t.Fatalf("old storage[0] leaked after SELFDESTRUCT+CREATE: got %v, want 0", v0)
		}
		if !v1.IsZero() {
			t.Fatalf("old storage[1] leaked after SELFDESTRUCT+CREATE: got %v, want 0", v1)
		}

		// Write new storage and verify.
		sdb.SetState(addr, &slot0, *uint256.NewInt(999))
		sdb.GetState(addr, &slot0, &v0)
		if v0 != *uint256.NewInt(999) {
			t.Fatalf("new storage write failed: got %v, want 999", v0)
		}

		if err := sdb.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, writer); err != nil {
			return err
		}
		return sdb.CommitBlock(&params.Rules{IsSpuriousDragon: true}, writer)
	})
	if err != nil {
		t.Fatal(err)
	}

	// Reload from DB and verify persistence.
	err = db.View(context.Background(), func(tx kv.Tx) error {
		sdb := New(NewPlainStateReader(tx))
		var v0, v1 uint256.Int
		sdb.GetState(addr, &slot0, &v0)
		sdb.GetState(addr, &slot1, &v1)
		if v0 != *uint256.NewInt(999) {
			t.Fatalf("persisted storage[0] wrong: got %v, want 999", v0)
		}
		if !v1.IsZero() {
			t.Fatalf("persisted storage[1] should be 0: got %v", v1)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestMultipleSelfDestructCycles verifies that repeated SELFDESTRUCT + CREATE2
// at the same address provides storage isolation each time.
func TestMultipleSelfDestructCycles(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	addr := types.HexToAddress("0x2000000000000000000000000000000000000001")
	slot := types.Hash{0x42}
	db := memdb.NewTestDB(t)

	err := db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := NewPlainStateWriter(tx, tx, 1)
		rules := &params.Rules{IsSpuriousDragon: true}

		for cycle := uint64(1); cycle <= 3; cycle++ {
			sdb := New(NewPlainState(tx, cycle))

			// Create contract and write a unique value.
			sdb.CreateAccount(addr, true)
			sdb.SetCode(addr, []byte{byte(cycle)})
			sdb.SetState(addr, &slot, *uint256.NewInt(cycle * 100))

			// Verify value is set.
			var v uint256.Int
			sdb.GetState(addr, &slot, &v)
			if v != *uint256.NewInt(cycle*100) {
				t.Fatalf("cycle %d: expected %d, got %v", cycle, cycle*100, v)
			}

			if err := sdb.FinalizeTx(rules, writer); err != nil {
				return err
			}
			if err := sdb.CommitBlock(rules, writer); err != nil {
				return err
			}

			// SELFDESTRUCT.
			sdb.Selfdestruct(addr)
			if err := sdb.FinalizeTx(rules, writer); err != nil {
				return err
			}
			if err := sdb.CommitBlock(rules, writer); err != nil {
				return err
			}

			// After SELFDESTRUCT, verify storage is gone.
			sdb2 := New(NewPlainState(tx, cycle))
			sdb2.CreateAccount(addr, true) // triggers incarnation++ or storage wipe
			sdb2.GetState(addr, &slot, &v)
			if !v.IsZero() {
				t.Fatalf("cycle %d: storage leaked after SELFDESTRUCT: got %v", cycle, v)
			}

			// Clean up for next cycle.
			if err := sdb2.FinalizeTx(rules, writer); err != nil {
				return err
			}
			if err := sdb2.CommitBlock(rules, writer); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A snapshotTest checks that reverting IntraBlockState snapshots properly undoes all changes
// captured by the snapshot. Instances of this test with pseudorandom content are created
// by Generate.
//
// The test works as follows:
//
// A new state is created and all actions are applied to it. Several snapshots are taken
// in between actions. The test then reverts each snapshot. For each snapshot the actions
// leading up to it are replayed on a fresh, empty state. The behavior of all public
// accessor methods on the reverted state must match the return value of the equivalent
// methods on the replayed state.
type snapshotTest struct {
	addrs     []types.Address // all account addresses
	actions   []testAction    // modifications to the state
	snapshots []int           // actions indexes at which snapshot is taken
	err       error           // failure details are reported through this field
}

type testAction struct {
	name   string
	fn     func(testAction, *IntraBlockState)
	args   []int64
	noAddr bool
}

// newTestAction creates a random action that changes state.
func newTestAction(addr types.Address, r *rand.Rand) testAction {
	actions := []testAction{
		{
			name: "SetBalance",
			fn: func(a testAction, s *IntraBlockState) {
				s.SetBalance(addr, uint256.NewInt(uint64(a.args[0])))
			},
			args: make([]int64, 1),
		},
		{
			name: "AddBalance",
			fn: func(a testAction, s *IntraBlockState) {
				s.AddBalance(addr, uint256.NewInt(uint64(a.args[0])))
			},
			args: make([]int64, 1),
		},
		{
			name: "SetNonce",
			fn: func(a testAction, s *IntraBlockState) {
				s.SetNonce(addr, uint64(a.args[0]))
			},
			args: make([]int64, 1),
		},
		{
			name: "SetState",
			fn: func(a testAction, s *IntraBlockState) {
				var key types.Hash
				binary.BigEndian.PutUint16(key[:], uint16(a.args[0]))
				val := uint256.NewInt(uint64(a.args[1]))
				s.SetState(addr, &key, *val)
			},
			args: make([]int64, 2),
		},
		{
			name: "SetCode",
			fn: func(a testAction, s *IntraBlockState) {
				code := make([]byte, 16)
				binary.BigEndian.PutUint64(code, uint64(a.args[0]))
				binary.BigEndian.PutUint64(code[8:], uint64(a.args[1]))
				s.SetCode(addr, code)
			},
			args: make([]int64, 2),
		},
		{
			name: "CreateAccount",
			fn: func(a testAction, s *IntraBlockState) {
				s.CreateAccount(addr, true)
			},
		},
		{
			name: "Suicide",
			fn: func(a testAction, s *IntraBlockState) {
				s.Suicide(addr)
			},
		},
		{
			name: "AddRefund",
			fn: func(a testAction, s *IntraBlockState) {
				s.AddRefund(uint64(a.args[0]))
			},
			args:   make([]int64, 1),
			noAddr: true,
		},
		{
			name: "AddLog",
			fn: func(a testAction, s *IntraBlockState) {
				data := make([]byte, 2)
				binary.BigEndian.PutUint16(data, uint16(a.args[0]))
				s.AddLog(&block.Log{Address: addr, Data: data})
			},
			args: make([]int64, 1),
		},
		{
			name: "AddAddressToAccessList",
			fn: func(a testAction, s *IntraBlockState) {
				s.AddAddressToAccessList(addr)
			},
		},
		{
			name: "AddSlotToAccessList",
			fn: func(a testAction, s *IntraBlockState) {
				s.AddSlotToAccessList(addr,
					types.Hash{byte(a.args[0])})
			},
			args: make([]int64, 1),
		},
	}
	action := actions[r.Intn(len(actions))]
	nameargs := make([]string, 0, len(action.args)+1)
	if !action.noAddr {
		nameargs = append(nameargs, addr.Hex())
	}
	for i := range action.args {
		action.args[i] = rand.Int63n(100)
		nameargs = append(nameargs, fmt.Sprint(action.args[i]))
	}
	action.name += strings.Join(nameargs, ", ")
	return action
}

// Generate returns a new snapshot test of the given size. All randomness is
// derived from r.
func (*snapshotTest) Generate(r *rand.Rand, size int) reflect.Value {
	// Generate random actions.
	addrs := make([]types.Address, 50)
	for i := range addrs {
		addrs[i][0] = byte(i)
	}
	actions := make([]testAction, size)
	for i := range actions {
		addr := addrs[r.Intn(len(addrs))]
		actions[i] = newTestAction(addr, r)
	}
	// Generate snapshot indexes.
	nsnapshots := int(math.Sqrt(float64(size)))
	if size > 0 && nsnapshots == 0 {
		nsnapshots = 1
	}
	snapshots := make([]int, nsnapshots)
	snaplen := len(actions) / nsnapshots
	for i := range snapshots {
		// Try to place the snapshots some number of actions apart from each other.
		snapshots[i] = (i * snaplen) + r.Intn(snaplen)
	}
	return reflect.ValueOf(&snapshotTest{addrs, actions, snapshots, nil})
}

func (test *snapshotTest) String() string {
	out := new(bytes.Buffer)
	sindex := 0
	for i, action := range test.actions {
		if len(test.snapshots) > sindex && i == test.snapshots[sindex] {
			fmt.Fprintf(out, "---- snapshot %d ----\n", sindex)
			sindex++
		}
		fmt.Fprintf(out, "%4d: %s\n", i, action.name)
	}
	return out.String()
}

func (test *snapshotTest) run() bool {
	// Run all actions and create snapshots.
	db := memdb.New("")
	defer db.Close()
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		test.err = err
		return false
	}
	defer tx.Rollback()
	var (
		ds           = NewPlainState(tx, 1)
		state        = New(ds)
		snapshotRevs = make([]int, len(test.snapshots))
		sindex       = 0
	)
	for i, action := range test.actions {
		if len(test.snapshots) > sindex && i == test.snapshots[sindex] {
			snapshotRevs[sindex] = state.Snapshot()
			sindex++
		}
		action.fn(action, state)
	}
	// Revert all snapshots in reverse order. Each revert must yield a state
	// that is equivalent to fresh state with all actions up the snapshot applied.
	for sindex--; sindex >= 0; sindex-- {
		checkds := NewPlainState(tx, 1)
		checkstate := New(checkds)
		for _, action := range test.actions[:test.snapshots[sindex]] {
			action.fn(action, checkstate)
		}
		state.RevertToSnapshot(snapshotRevs[sindex])
		if err := test.checkEqual(state, checkstate); err != nil {
			test.err = fmt.Errorf("state mismatch after revert to snapshot %d\n%w", sindex, err)
			return false
		}
	}
	return true
}

// checkEqual checks that methods of state and checkstate return the same values.
func (test *snapshotTest) checkEqual(state, checkstate *IntraBlockState) error {
	for _, addr := range test.addrs {
		addr := addr // pin
		var err error
		checkeq := func(op string, a, b interface{}) bool {
			if err == nil && !reflect.DeepEqual(a, b) {
				err = fmt.Errorf("got %s(%s) == %v, want %v", op, addr.Hex(), a, b)
				return false
			}
			return true
		}
		checkeqBigInt := func(op string, a, b *big.Int) bool {
			if err == nil && a.Cmp(b) != 0 {
				err = fmt.Errorf("got %s(%s) == %d, want %d", op, addr.Hex(), a, b)
				return false
			}
			return true
		}
		// Check basic accessor methods.
		if !checkeq("Exist", state.Exist(addr), checkstate.Exist(addr)) {
			return err
		}
		checkeq("HasSuicided", state.HasSuicided(addr), checkstate.HasSuicided(addr))
		checkeqBigInt("GetBalance", state.GetBalance(addr).ToBig(), checkstate.GetBalance(addr).ToBig())
		checkeq("GetNonce", state.GetNonce(addr), checkstate.GetNonce(addr))
		checkeq("GetCode", state.GetCode(addr), checkstate.GetCode(addr))
		checkeq("GetCodeHash", state.GetCodeHash(addr), checkstate.GetCodeHash(addr))
		checkeq("GetCodeSize", state.GetCodeSize(addr), checkstate.GetCodeSize(addr))
		// Check storage.
		if obj := state.getStateObject(addr); obj != nil {
			for key, value := range obj.dirtyStorage {
				var out uint256.Int
				checkstate.GetState(addr, &key, &out)
				if !checkeq("GetState("+key.Hex()+")", out, value) {
					return err
				}
			}
		}
		if obj := checkstate.getStateObject(addr); obj != nil {
			for key, value := range obj.dirtyStorage {
				var out uint256.Int
				state.GetState(addr, &key, &out)
				if !checkeq("GetState("+key.Hex()+")", out, value) {
					return err
				}
			}
		}
	}

	if state.GetRefund() != checkstate.GetRefund() {
		return fmt.Errorf("got GetRefund() == %d, want GetRefund() == %d",
			state.GetRefund(), checkstate.GetRefund())
	}
	if !reflect.DeepEqual(state.GetLogs(types.Hash{}), checkstate.GetLogs(types.Hash{})) {
		return fmt.Errorf("got GetLogs(types.Hash{}) == %v, want GetLogs(types.Hash{}) == %v",
			state.GetLogs(types.Hash{}), checkstate.GetLogs(types.Hash{}))
	}
	return nil
}
