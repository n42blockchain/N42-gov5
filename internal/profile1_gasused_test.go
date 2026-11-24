// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// The gasUsed a profile-1 header carries for its parent (A10, B13) is the
// EIP-8037 block figure, max(block regular gas, block state gas), with the
// regular gas taken before refunds -- not the receipts' cumulative gas.
func TestProfile1DeferredGasUsedIsEIP8037(t *testing.T) {
	_, sender := v3Key(t)
	r, _ := newV3Runner(t, map[types.Address]conf.GenesisAccount{sender: {Balance: v3SenderBalanceWei}})
	contract := crypto.CreateAddress(sender, 0)
	fresh := types.HexToAddress("0x00000000000000000000000000000000000000cc")

	type want struct {
		name                 string
		stateAbove, refunded bool
	}
	check := func(w want) {
		t.Helper()
		exec, st := r.lastGP.AmsterdamCounters()
		blockGas := max(exec, st)
		res := profile1ExecutedResult(r)
		if res.GasUsed != blockGas {
			t.Errorf("%s: carried gasUsed %d, want max(regular %d, state %d) = %d (receipts' cumulative %d)",
				w.name, res.GasUsed, exec, st, blockGas, r.lastUsedGas)
		}
		if w.stateAbove && st <= exec {
			t.Fatalf("%s: state gas %d not above regular %d", w.name, st, exec)
		}
		if w.refunded && r.lastUsedGas >= exec {
			t.Fatalf("%s: no refund: receipts' cumulative %d, regular %d", w.name, r.lastUsedGas, exec)
		}
	}

	r.run("idle", "", types.Hash{}, nil)
	check(want{name: "idle"})
	if res := profile1ExecutedResult(r); res.GasUsed != 0 {
		t.Fatalf("idle: gasUsed %d", res.GasUsed)
	}

	r.run("create", "", types.Hash{}, []*transaction.Transaction{r.sign(nil, uint256.NewInt(0), 10_000_000, v3StoreInitcode)})
	check(want{name: "contract-create", stateAbove: true})

	r.run("new-account", "", types.Hash{}, []*transaction.Transaction{r.sign(&fresh, uint256.NewInt(1), 10_000_000, nil)})
	check(want{name: "new-account", stateAbove: true})

	r.run("plain-transfer", "", types.Hash{}, []*transaction.Transaction{r.sign(&fresh, uint256.NewInt(1), 10_000_000, nil)})
	check(want{name: "plain-transfer"})
	if res := profile1ExecutedResult(r); res.GasUsed != 21000 || r.lastUsedGas != 21000 {
		t.Fatalf("plain-transfer: gasUsed %d, receipts %d, want 21000", res.GasUsed, r.lastUsedGas)
	}

	r.run("sstore-clear", "", types.Hash{}, []*transaction.Transaction{r.sign(&contract, uint256.NewInt(0), 10_000_000, storeCall(1, 0))})
	check(want{name: "sstore-clear", refunded: true})
}

// profile1ExecutedResult is the deferred result gov5 stores for the
// runner's last block.
func profile1ExecutedResult(r *v3Runner) rawdb.ExecutedResult {
	h := r.lastHeader
	gasUsed := HeaderGasUsed(r.cfg, h, r.lastGP, r.lastUsedGas)
	return ExecutedResultFor(r.cfg, h.Number.Uint64(), r.lastRoot, r.lastReceipts, gasUsed, ExecutionRequestsHash(r.lastRequests))
}
