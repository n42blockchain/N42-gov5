// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/params"
)

type orderedRootComputer struct {
	calls *[]string
}

func (c orderedRootComputer) ComputeRoot(map[types.Address]*account.StateAccount, map[types.Address]map[types.Hash]*uint256.Int) (types.Hash, error) {
	*c.calls = append(*c.calls, "root")
	return types.Hash{1}, nil
}

// The lag pipeline captures a block's post-state from the before-root hook:
// it must run once, before the root computer, with every pending balance
// credit already materialised -- a credit to an account the block never
// loaded is the case CapturePostState alone misses.
func TestBeforeRootHookSeesTheFinalDirtySet(t *testing.T) {
	modules.N42Init()
	prevTables := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevTables })

	credited := types.HexToAddress("0x20000000000000000000000000000000000000c1")
	db := memdb.NewTestDB(t)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		orig := account.NewAccount()
		acc := account.NewAccount()
		acc.Nonce = 1
		acc.Balance.SetUint64(10)
		return NewPlainStateWriter(tx, tx, 1).UpdateAccountData(credited, &orig, &acc)
	}); err != nil {
		t.Fatal(err)
	}
	err := db.View(context.Background(), func(tx kv.Tx) error {
		base := NewPlainStateReader(tx)
		ibs := New(base)
		ibs.AddBalance(credited, uint256.NewInt(5))
		if err := ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true}, NewNoopWriter()); err != nil {
			return err
		}
		var calls []string
		var post *PostState
		ibs.SetRootComputer(orderedRootComputer{calls: &calls})
		ibs.SetBeforeRootHook(func() {
			calls = append(calls, "hook")
			post = CapturePostState(ibs)
		})
		if root := ibs.IntermediateRoot(); root != (types.Hash{1}) {
			t.Fatalf("root %x", root)
		}
		if len(calls) != 2 || calls[0] != "hook" || calls[1] != "root" {
			t.Fatalf("calls %v, want [hook root]", calls)
		}
		got, err := NewPostStateReader(post, base).ReadAccountData(credited)
		if err != nil || got == nil || got.Balance.Uint64() != 15 {
			t.Fatalf("credited account in the captured post-state: %+v %v, want balance 15", got, err)
		}
		// One-shot: a second root computation does not run it again.
		calls = calls[:0]
		ibs.IntermediateRoot()
		if len(calls) != 1 || calls[0] != "root" {
			t.Fatalf("second root calls %v, want [root]", calls)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
