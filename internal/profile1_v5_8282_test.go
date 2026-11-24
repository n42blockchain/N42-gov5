// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// Vector V5, builder request contracts (EIP-8282): code at the builder
// deposit and exit request addresses on a native profile-1 chain. gov5 makes
// no end-of-block EIP-8282 call there (UsesEIP8282BuilderRequests is off), so
// the code never runs: no storage write, no request, state and results as if
// the addresses held no code. The addresses have no deployer a test can use
// (Nick's method, pre-signed and keyless), so the code is placed in the
// chain's alloc additions. Written to V5/builder_request_contracts.json.

// v58282Code increments slot 0 when called: it would change state if any
// call reached it. PUSH0 SLOAD PUSH1 1 ADD PUSH0 SSTORE STOP.
var v58282Code = []byte{0x5f, 0x54, 0x60, 0x01, 0x01, 0x5f, 0x55, 0x00}

type v58282Block struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	Number       uint64          `json:"number"`
	Timestamp    uint64          `json:"timestamp"`
	Txs          []hexutil.Bytes `json:"transactions"`
	StateRoot    types.Hash      `json:"stateRoot"`
	ReceiptsRoot types.Hash      `json:"receiptsRoot"`
	GasUsed      uint64          `json:"gasUsed"`
	Requests     []hexutil.Bytes `json:"requests"`
	RequestsHash types.Hash      `json:"requestsHash"`
	Accounts     []v3AccountOp   `json:"accountOps"`
	Storage      []v3StorageOp   `json:"storageOps"`
}

type v58282File struct {
	Vector         string                                `json:"vector"`
	Case           string                                `json:"case"`
	Description    string                                `json:"description"`
	Genesis        string                                `json:"genesis"`
	AllocAdded     map[types.Address]conf.GenesisAccount `json:"allocAdditions"`
	BuilderDeposit types.Address                         `json:"builderDepositAddress"`
	BuilderExit    types.Address                         `json:"builderExitAddress"`
	CodeHash       types.Hash                            `json:"builderCodeHash"`
	Slot0After     string                                `json:"slot0OfBothAddressesAfterEveryBlock"`
	GenesisRoot    types.Hash                            `json:"genesisQmdbStateRoot"`
	Blocks         []v58282Block                         `json:"blocks"`
	Notes          []string                              `json:"notes"`
}

// v58282Run runs the case's two blocks. flip makes the chain config
// non-native for the execution, which re-enables the EIP-8282 calls.
func v58282Run(t *testing.T, withCode, flip bool) (*v58282File, [][]byte) {
	t.Helper()
	_, sender := v3Key(t)
	alloc := map[types.Address]conf.GenesisAccount{sender: {Balance: v3SenderBalanceWei}}
	if withCode {
		for _, a := range []types.Address{vm.BuilderDepositRequestsAddress, vm.BuilderExitRequestsAddress} {
			alloc[a] = conf.GenesisAccount{Balance: "0x0", Nonce: 1, Code: v58282Code}
		}
	}
	g := new(conf.Genesis)
	if err := json.Unmarshal(v1GenesisJSON(t), g); err != nil {
		t.Fatal(err)
	}
	for a, acct := range alloc {
		g.Alloc[a] = acct
	}
	r, groot := newV3RunnerFrom(t, g)
	if flip {
		if !r.cfg.IsN42Native() || r.cfg.UsesEIP8282BuilderRequests(1) {
			t.Fatal("test chain is not native")
		}
		c := *r.cfg
		c.Consensus, c.HotStuff, c.Apos = params.Faker, nil, nil
		if c.IsN42Native() || !c.UsesEIP8282BuilderRequests(1) {
			t.Fatal("flipped config still gates EIP-8282")
		}
		r.cfg = &c
	}
	out := &v58282File{Vector: "V5", AllocAdded: alloc, GenesisRoot: groot,
		BuilderDeposit: vm.BuilderDepositRequestsAddress, BuilderExit: vm.BuilderExitRequestsAddress,
		CodeHash: crypto.Keccak256Hash(v58282Code)}
	var slots [][]byte
	steps := []struct {
		name, desc string
		txs        func() []*transaction.Transaction
	}{
		{"idle-with-builder-code", "No transactions; both EIP-8282 addresses hold code. No call: no write to either address.", func() []*transaction.Transaction { return nil }},
		{"requests-with-builder-code", "EIP-7002 and EIP-7251 requests as V5 block 7: their end-of-block calls run, the EIP-8282 ones do not; requests hold only types 0x01 and 0x02.", func() []*transaction.Transaction { return p1RequestTxs(r) }},
	}
	for _, s := range steps {
		b := r.run(s.name, s.desc, types.HexToHash(v3BeaconRootNonZero), s.txs())
		res := ExecutedResultFor(r.cfg, b.Header.Number, b.StateRoot, r.lastReceipts, HeaderGasUsed(r.cfg, r.lastHeader, r.lastGP, r.lastUsedGas), ExecutionRequestsHash(r.lastRequests))
		out.Blocks = append(out.Blocks, v58282Block{Name: s.name, Description: s.desc, Number: b.Header.Number, Timestamp: b.Header.Timestamp,
			Txs: b.Txs, StateRoot: b.StateRoot, ReceiptsRoot: res.ReceiptHash, GasUsed: res.GasUsed,
			Requests: append([]hexutil.Bytes{}, r.lastRequests...), RequestsHash: res.RequestsHash, Accounts: b.Accounts, Storage: b.Storage})
		ibs := state.New(state.NewPlainStateReader(r.tx))
		for _, a := range []types.Address{vm.BuilderDepositRequestsAddress, vm.BuilderExitRequestsAddress} {
			var v uint256.Int
			slot := types.Hash{}
			ibs.GetState(a, &slot, &v)
			slots = append(slots, v.Bytes())
			if withCode && !flip && (!v.IsZero() || ibs.GetCodeHash(a) != out.CodeHash || ibs.GetNonce(a) != 1) {
				t.Fatalf("%s: %s changed (slot0 %s)", s.name, a, v.Hex())
			}
		}
	}
	return out, slots
}

func TestProfile1VectorV5BuilderRequestContracts(t *testing.T) {
	with, _ := v58282Run(t, true, false)
	control, _ := v58282Run(t, false, false)
	flipped, flippedSlots := v58282Run(t, true, true)

	for i := range with.Blocks {
		w, c, f := with.Blocks[i], control.Blocks[i], flipped.Blocks[i]
		for _, a := range []types.Address{vm.BuilderDepositRequestsAddress, vm.BuilderExitRequestsAddress} {
			for _, op := range w.Accounts {
				if op.Address == a {
					t.Fatalf("block %d: %s in the write set", w.Number, a)
				}
			}
			for _, op := range w.Storage {
				if op.Address == a {
					t.Fatalf("block %d: %s storage in the write set", w.Number, a)
				}
			}
		}
		// Control: the same chain without the code. Everything but the
		// stateRoot (which holds the two static accounts) is identical.
		wj, _ := json.Marshal(w.Accounts)
		cj, _ := json.Marshal(c.Accounts)
		// The EIP-2935 slot holds the parent hash, which differs with the
		// genesis; every other storage write must be equal.
		noHistory := func(ops []v3StorageOp) []v3StorageOp {
			var out []v3StorageOp
			for _, op := range ops {
				if op.Address != vm.HistoryStorageAddress {
					out = append(out, op)
				}
			}
			return out
		}
		sw, _ := json.Marshal(noHistory(w.Storage))
		sc, _ := json.Marshal(noHistory(c.Storage))
		if !bytes.Equal(wj, cj) || !bytes.Equal(sw, sc) || w.ReceiptsRoot != c.ReceiptsRoot || w.GasUsed != c.GasUsed || w.RequestsHash != c.RequestsHash {
			t.Fatalf("block %d differs from the chain without the code", w.Number)
		}
		// Sensitivity: with the gate flipped the calls run, change state
		// and the roots.
		if f.StateRoot == w.StateRoot {
			t.Fatalf("block %d: flipped gate left the state root unchanged; the case is not sensitive", w.Number)
		}
		touched := map[types.Address]bool{}
		for _, op := range f.Storage {
			touched[op.Address] = true
		}
		if !touched[vm.BuilderDepositRequestsAddress] || !touched[vm.BuilderExitRequestsAddress] {
			t.Fatalf("block %d: flipped gate did not write both addresses", w.Number)
		}
	}
	for _, s := range flippedSlots {
		if len(s) == 0 {
			t.Fatal("flipped run left slot 0 empty")
		}
	}
	// requests on the second block are the V5 block 7 pair only.
	if want := p1WantRequests(func() types.Address { _, s := v3Key(t); return s }()); len(with.Blocks[1].Requests) != 2 ||
		!bytes.Equal(with.Blocks[1].Requests[0], want[0]) || !bytes.Equal(with.Blocks[1].Requests[1], want[1]) {
		t.Fatalf("requests %x", with.Blocks[1].Requests)
	}
	if with.Blocks[0].RequestsHash != rawdb.EmptyRequestsHash {
		t.Fatal("idle block produced requests")
	}

	with.Case = "builder-request-contract-code-no-call"
	with.Description = "Code at the EIP-8282 builder deposit and exit request addresses on a native profile-1 chain: no end-of-block EIP-8282 call is made, state and results equal the same blocks without the calls."
	with.Genesis = "../V1/" + v1Genesis
	with.Slot0After = "0x0 (unchanged, both addresses, after every block)"
	with.Notes = []string{
		"The code is placed through allocAdditions: the real contracts are deployed by Nick's method (keyless pre-signed CREATE) and a test cannot sign that transaction, so it is not deployed mid-chain. Both addresses get nonce 1, balance 0 and the same code (builderCodeHash): PUSH0 SLOAD PUSH1 1 ADD PUSH0 SSTORE STOP, which increments slot 0 if called.",
		"Per block: stateRoot (QMDB), receiptsRoot, gasUsed (EIP-8037), requests and requestsHash are the block's own execution; accountOps/storageOps are its QMDB write set and name neither builder address.",
		"Control: the same genesis without the two accounts and the same blocks give identical accountOps and storageOps (but the EIP-2935 slot, which holds the parent hash), receiptsRoot, gasUsed, requests and requestsHash; only stateRoot differs, by the two static accounts. gov5 asserts this.",
		"Sensitivity: executing the same blocks with the chain config made non-native (EIP-8282 calls on) writes slot 0 of both addresses and changes stateRoot; gov5 asserts it, so the case would catch a client that makes the calls.",
	}
	writeProfile1Vector(t, "V5", "builder_request_contracts.json", with)
}
