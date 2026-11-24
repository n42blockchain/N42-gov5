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

package internal

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// Vector V3 of the mixed production standard (Profile 1): the QMDB write set
// of a block. Each chain starts from the standard genesis fixture plus the
// alloc additions it lists, then runs its blocks the way a Profile-1 import
// does (EIP-4788 and EIP-2935 at block start, the transactions, the EIP-7002
// and EIP-7251 calls at block end, the A8 reward, then the no-op rule A9 and
// the storage net-change rule D5) and records the operations the state root
// applies to QMDB, with the root after them.
//
// N42_PROFILE1_VECTORS_UPDATE=1 rewrites testdata/profile1/vectors;
// N42_PROFILE1_VECTORS_MIRROR=<dir> also writes the files there.

const (
	profile1VectorsDir  = "testdata/profile1/vectors"
	profile1VectorsEnv  = "N42_PROFILE1_VECTORS_UPDATE"
	profile1MirrorEnv   = "N42_PROFILE1_VECTORS_MIRROR"
	v3BaseFee           = 1_000_000_000
	v3TipCap            = 1_000_000_000
	v3FeeCap            = 2_000_000_000
	v3SenderSeed        = "n42-profile1-vectors-sender"
	v3SenderBalanceWei  = "0x56bc75e2d63100000" // 100 ether
	v3BeaconRootNonZero = "0x1111111111111111111111111111111111111111111111111111111111111111"
)

// v3StoreRuntime stores calldata[32:64] at slot calldata[0:32]:
// PUSH1 0x20 CALLDATALOAD PUSH1 0x00 CALLDATALOAD SSTORE STOP.
var v3StoreRuntime = []byte{0x60, 0x20, 0x35, 0x60, 0x00, 0x35, 0x55, 0x00}

// v3StoreInitcode sets slot 1 = 1, sets slot 2 = 5 and resets slot 2 = 0,
// then deploys v3StoreRuntime.
var v3StoreInitcode = append([]byte{
	0x60, 0x01, 0x60, 0x01, 0x55, // SSTORE(1, 1)
	0x60, 0x05, 0x60, 0x02, 0x55, // SSTORE(2, 5)
	0x60, 0x00, 0x60, 0x02, 0x55, // SSTORE(2, 0)
	0x60, 0x08, 0x60, 0x1b, 0x60, 0x00, 0x39, // CODECOPY(0, 27, 8)
	0x60, 0x08, 0x60, 0x00, 0xf3, // RETURN(0, 8)
}, v3StoreRuntime...)

type v3AccountOp struct {
	Address   types.Address `json:"address"`
	KeyHash   types.Hash    `json:"qmdbKeyHash"`
	Op        string        `json:"op"` // "write" or "delete"
	Nonce     *uint64       `json:"nonce,omitempty"`
	Balance   string        `json:"balance,omitempty"`
	CodeHash  *types.Hash   `json:"codeHash,omitempty"`
	QMDBValue hexutil.Bytes `json:"qmdbValue,omitempty"`
}

type v3StorageOp struct {
	Address types.Address `json:"address"`
	Slot    types.Hash    `json:"slot"`
	KeyHash types.Hash    `json:"qmdbKeyHash"`
	Op      string        `json:"op"`
	Value   *types.Hash   `json:"value,omitempty"`
}

type v3Header struct {
	Number           uint64        `json:"number"`
	Timestamp        uint64        `json:"timestamp"`
	ParentHash       types.Hash    `json:"parentHash"`
	Coinbase         types.Address `json:"coinbase"`
	GasLimit         uint64        `json:"gasLimit"`
	BaseFeePerGas    uint64        `json:"baseFeePerGas"`
	ParentBeaconRoot types.Hash    `json:"parentBeaconBlockRoot"`
	PrevRandao       types.Hash    `json:"prevRandao"`
}

type v3Block struct {
	Case        string          `json:"case"`
	Description string          `json:"description"`
	Header      v3Header        `json:"header"`
	Txs         []hexutil.Bytes `json:"transactions"`
	Accounts    []v3AccountOp   `json:"accountOps"`
	Storage     []v3StorageOp   `json:"storageOps"`
	Absent      []types.Address `json:"mustNotAppear,omitempty"`
	StateRoot   types.Hash      `json:"qmdbStateRoot"`
}

type v3Chain struct {
	Vector      string                                `json:"vector"`
	Chain       string                                `json:"chain"`
	Description string                                `json:"description"`
	Genesis     string                                `json:"genesis"`
	AllocAdded  map[types.Address]conf.GenesisAccount `json:"allocAdditions"`
	GenesisRoot types.Hash                            `json:"genesisQmdbStateRoot"`
	Blocks      []v3Block                             `json:"blocks"`
	Notes       []string                              `json:"notes"`
}

// v3Key is the deterministic test sender of the vectors.
func v3Key(t *testing.T) (*ecdsa.PrivateKey, types.Address) {
	t.Helper()
	key, err := crypto.ToECDSA(crypto.Keccak256([]byte(v3SenderSeed)))
	if err != nil {
		t.Fatal(err)
	}
	return key, crypto.PubkeyToAddress(key.PublicKey)
}

// v3Runner executes blocks on a Profile-1 chain held in a memdb, with one
// QMDB root computer carried from the genesis.
type v3Runner struct {
	t        *testing.T
	cfg      *params.ChainConfig
	tx       kv.RwTx
	rc       *commitment.QMDBRootComputer
	parent   *block.Header
	coinbase types.Address
	faucet   types.Address
	reward   *uint256.Int
	key      *ecdsa.PrivateKey
	sender   types.Address
	nonce    uint64
	signer   transaction.Signer
}

func newV3Runner(t *testing.T, add map[types.Address]conf.GenesisAccount) (*v3Runner, types.Hash) {
	t.Helper()
	g, err := loadProfile1Genesis(t, filepath.Join(profile1Dir, profile1StandardFile))
	if err != nil {
		t.Fatalf("standard genesis: %v", err)
	}
	for addr, acct := range add {
		g.Alloc[addr] = acct
	}
	db := memdb.NewTestDB(t)
	rwtx := memdb.BeginRw(t, db)
	// The genesis header from ToBlock; the alloc written to plain state as
	// WriteGenesisState writes it (its QMDB forest flush needs tables the
	// test database does not open; the forest is rebuilt below instead).
	genesis, _, err := (&GenesisBlock{GenesisConfig: g}).ToBlock()
	if err != nil {
		t.Fatalf("genesis block: %v", err)
	}
	gs := state.New(state.NewPlainStateReader(rwtx))
	for addr, acct := range g.Alloc {
		balance, err := decodeGenesisBalance(acct.Balance)
		if err != nil {
			t.Fatal(err)
		}
		gs.AddBalance(addr, balance)
		gs.SetCode(addr, acct.Code)
		gs.SetNonce(addr, acct.Nonce)
		for key, value := range acct.Storage {
			k := key
			gs.SetState(addr, &k, *new(uint256.Int).SetBytes(value.Bytes()))
		}
	}
	if err := gs.FinalizeTx(g.Config.Rules(0), state.NewPlainStateWriter(rwtx, rwtx, 0)); err != nil {
		t.Fatal(err)
	}
	accounts, storage, err := readPlainStateForQMDB(rwtx)
	if err != nil {
		t.Fatal(err)
	}
	rc := commitment.NewQMDBRootComputer()
	root, err := rc.ComputeRoot(accounts, storage)
	if err != nil {
		t.Fatal(err)
	}
	if root != genesis.StateRoot() {
		t.Fatalf("genesis qmdb root %s, header %s", root, genesis.StateRoot())
	}
	key, sender := v3Key(t)
	cfg := g.Config
	r := &v3Runner{
		t:        t,
		cfg:      cfg,
		rc:       rc,
		parent:   genesis.Header().(*block.Header),
		coinbase: types.HexToAddress(cfg.HotStuff.Validators[0].Address),
		faucet:   *cfg.HotStuff.DevFaucetAddress,
		reward:   cfg.HotStuff.DevBlockReward.Amount(),
		key:      key,
		sender:   sender,
		signer:   transaction.LatestSignerForChainID(cfg.ChainID),
		tx:       rwtx,
	}
	return r, root
}

// sign builds a dynamic-fee transaction from the test sender.
func (r *v3Runner) sign(to *types.Address, value *uint256.Int, gas uint64, data []byte) *transaction.Transaction {
	r.t.Helper()
	chainID, _ := uint256.FromBig(r.cfg.ChainID)
	tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   chainID,
		Nonce:     r.nonce,
		GasTipCap: uint256.NewInt(v3TipCap),
		GasFeeCap: uint256.NewInt(v3FeeCap),
		Gas:       gas,
		To:        to,
		Value:     value,
		Data:      data,
	}), r.signer, r.key)
	if err != nil {
		r.t.Fatal(err)
	}
	r.nonce++
	return tx
}

// run executes one block on top of the runner's parent and returns its V3
// record. It mirrors StateProcessor.Process and HotStuff's Finalize.
func (r *v3Runner) run(name, desc string, beaconRoot types.Hash, txs []*transaction.Transaction, absent ...types.Address) v3Block {
	r.t.Helper()
	rwtx := r.tx
	number := r.parent.Number.Uint64() + 1
	header := &block.Header{
		ParentHash:       r.parent.Hash(),
		Coinbase:         r.coinbase,
		Number:           uint256.NewInt(number),
		GasLimit:         r.parent.GasLimit,
		Time:             r.parent.Time + 1,
		BaseFee:          uint256.NewInt(v3BaseFee),
		Difficulty:       uint256.NewInt(0),
		ParentBeaconRoot: &beaconRoot,
	}
	ibs := state.New(state.NewPlainStateReader(rwtx))
	ibs.SetRootComputer(r.rc)
	ibs.ApplyUnchangedAccountRule(r.cfg, header.Time)
	if err := ProcessExecutionBlockStart(header.ParentBeaconRoot, r.cfg, ibs, header, nil); err != nil {
		r.t.Fatalf("%s: block start: %v", name, err)
	}
	gp := new(common.GasPool).AddGas(header.GasLimit)
	var usedGas uint64
	hashFn := func(uint64) types.Hash { return types.Hash{} }
	raw := make([]hexutil.Bytes, 0, len(txs))
	for i, tx := range txs {
		ibs.Prepare(tx.Hash(), types.Hash{}, i)
		if _, _, err := ApplyTransaction(r.cfg, hashFn, nil, &r.coinbase, gp, ibs, state.NewNoopWriter(), header, tx, &usedGas, vm2.Config{}); err != nil {
			r.t.Fatalf("%s: tx %d: %v", name, i, err)
		}
		enc, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			r.t.Fatal(err)
		}
		raw = append(raw, enc)
	}
	if _, err := ProcessExecutionBlockEnd(nil, r.cfg, ibs, header, nil); err != nil {
		r.t.Fatalf("%s: block end: %v", name, err)
	}
	// A8: devBlockReward to the coinbase and the faucet (HotStuff Finalize).
	ibs.AddBalance(r.coinbase, r.reward)
	ibs.AddBalance(r.faucet, r.reward)
	ibs.ApplyUnchangedAccountRule(r.cfg, header.Time)
	ibs.SetRootComputer(r.rc)
	root := ibs.IntermediateRoot()
	accounts, storage := ibs.LastRootDirtySet()
	rules := r.cfg.Rules(number)
	if err := ibs.CommitBlock(rules, state.NewPlainStateWriter(rwtx, rwtx, number)); err != nil {
		r.t.Fatalf("%s: commit: %v", name, err)
	}
	header.Root = root
	r.parent = header

	out := v3Block{
		Case:        name,
		Description: desc,
		Header: v3Header{
			Number:           number,
			Timestamp:        header.Time,
			ParentHash:       header.ParentHash,
			Coinbase:         header.Coinbase,
			GasLimit:         header.GasLimit,
			BaseFeePerGas:    v3BaseFee,
			ParentBeaconRoot: beaconRoot,
		},
		Txs:       raw,
		Accounts:  v3AccountOps(accounts),
		Storage:   v3StorageOps(storage),
		Absent:    absent,
		StateRoot: root,
	}
	for _, a := range absent {
		if _, ok := accounts[a]; ok {
			r.t.Fatalf("%s: %s appears in the write set", name, a)
		}
		if _, ok := storage[a]; ok {
			r.t.Fatalf("%s: %s storage appears in the write set", name, a)
		}
	}
	return out
}

func v3AccountOps(accounts map[types.Address]*account.StateAccount) []v3AccountOp {
	ops := make([]v3AccountOp, 0, len(accounts))
	for addr, a := range accounts {
		op := v3AccountOp{Address: addr, KeyHash: types.Hash(commitment.AccountKeyHash(addr))}
		if a == nil || a.Nonce == 0 && a.Balance.IsZero() && (a.CodeHash == (types.Hash{}) || a.IsEmptyCodeHash()) {
			op.Op = "delete"
		} else {
			op.Op = "write"
			n := a.Nonce
			op.Nonce = &n
			op.Balance = a.Balance.Hex()
			ch := a.CodeHash
			op.CodeHash = &ch
			op.QMDBValue = commitment.EncodeAccountValue(a)
		}
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].KeyHash[:], ops[j].KeyHash[:]) < 0 })
	return ops
}

func v3StorageOps(storage map[types.Address]map[types.Hash]*uint256.Int) []v3StorageOp {
	var ops []v3StorageOp
	for addr, slots := range storage {
		for slot, v := range slots {
			op := v3StorageOp{Address: addr, Slot: slot, KeyHash: types.Hash(commitment.StorageKeyHash(addr, slot))}
			if v == nil || v.IsZero() {
				op.Op = "delete"
			} else {
				op.Op = "write"
				h := types.Hash(v.Bytes32())
				op.Value = &h
			}
			ops = append(ops, op)
		}
	}
	sort.Slice(ops, func(i, j int) bool { return bytes.Compare(ops[i].KeyHash[:], ops[j].KeyHash[:]) < 0 })
	return ops
}

func storeCall(slot, value uint64) []byte {
	data := make([]byte, 64)
	new(big.Int).SetUint64(slot).FillBytes(data[:32])
	new(big.Int).SetUint64(value).FillBytes(data[32:])
	return data
}

func (b v3Block) account(addr types.Address) (v3AccountOp, bool) {
	for _, op := range b.Accounts {
		if op.Address == addr {
			return op, true
		}
	}
	return v3AccountOp{}, false
}

func (b v3Block) slot(addr types.Address, slot uint64) (v3StorageOp, bool) {
	key := types.Hash(uint256.NewInt(slot).Bytes32())
	for _, op := range b.Storage {
		if op.Address == addr && op.Slot == key {
			return op, true
		}
	}
	return v3StorageOp{}, false
}

// writeProfile1Vector compares a vector file with testdata and, when asked,
// rewrites it (and its mirror).
func writeProfile1Vector(t *testing.T, vector, name string, v any) {
	t.Helper()
	enc, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	enc = append(enc, '\n')
	path := filepath.Join(profile1VectorsDir, vector, name)
	if os.Getenv(profile1VectorsEnv) == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, enc, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if dir := os.Getenv(profile1MirrorEnv); dir != "" {
		out := filepath.Join(dir, vector, name)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, enc, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	have, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v (set %s=1 to generate)", path, err, profile1VectorsEnv)
	}
	if !bytes.Equal(have, enc) {
		t.Fatalf("%s differs from the generated vector (set %s=1 to regenerate)", path, profile1VectorsEnv)
	}
}

// mirrorProfile1File copies a static vector file (a README) to the mirror.
func mirrorProfile1File(t *testing.T, vector, name string) {
	t.Helper()
	dir := os.Getenv(profile1MirrorEnv)
	if dir == "" {
		return
	}
	src, err := os.ReadFile(filepath.Join(profile1VectorsDir, vector, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, vector), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, vector, name), src, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestProfile1VectorV3 generates and checks the V3 write-set vectors.
func TestProfile1VectorV3(t *testing.T) {
	_, sender := v3Key(t)
	systemAddr := state.SystemAddress
	beacon := types.HexToHash(v3BeaconRootNonZero)
	historyAddr := vm2.HistoryStorageAddress
	senderAlloc := map[types.Address]conf.GenesisAccount{sender: {Balance: v3SenderBalanceWei}}
	notes := []string{
		"Write set = the operations the state root applies to QMDB (gov5 IntraBlockState.LastRootDirtySet): accountOps and storageOps, each sorted by qmdbKeyHash (D6). op=delete removes the key; a delete of a key the tree does not hold changes nothing.",
		"qmdbValue is the qmdb_compat account encoding (EncodeAccountValue); storage values are 32-byte words.",
		"Execution per block: EIP-4788 then EIP-2935 at block start, the transactions, EIP-7002 then EIP-7251 at block end, then the A8 reward (devBlockReward to coinbase and to devFaucetAddress), then A9 (unchanged accounts not written) and D5 (storage net change) from block 1.",
		"Header fields are inputs as given (baseFee fixed at 1 gwei, parentHash = gov5 hash of the previous vector header); header validity is V5's subject, not V3's. gov5 writes the EIP-4788 ring-buffer slots directly on a native chain; the values equal those the canonical contract call stores.",
		"mustNotAppear lists addresses that must have neither an account nor a storage op in the block.",
	}

	// Chain "main": the standard genesis plus a funded test sender.
	r, groot := newV3Runner(t, senderAlloc)
	main := v3Chain{
		Vector:      "V3",
		Chain:       "main",
		Description: "Standard Profile-1 genesis plus one funded test sender (key = keccak256(\"" + v3SenderSeed + "\")); 0xff..fe does not exist.",
		Genesis:     "internal/testdata/profile1/standard_genesis.json",
		AllocAdded:  senderAlloc,
		GenesisRoot: groot,
		Notes:       notes,
	}
	x := types.HexToAddress("0x00000000000000000000000000000000000000aa")
	never := types.HexToAddress("0x00000000000000000000000000000000000000bb")
	contract := crypto.CreateAddress(sender, 1)

	b := r.run("empty-block", "No transactions: the system calls and the reward only. The EIP-4788 root slot keeps value 0 (zero parentBeaconBlockRoot), so only its timestamp slot is written; 0xff..fe never existed and does not appear (D3).",
		types.Hash{}, nil, systemAddr)
	if _, ok := b.slot(historyAddr, 0); !ok {
		t.Fatal("empty-block: EIP-2935 slot 0 not written")
	}
	main.Blocks = append(main.Blocks, b)

	b = r.run("plain-transfer", "1 ether from the sender to a new account 0x..aa; non-zero parentBeaconBlockRoot (both EIP-4788 slots written).",
		beacon, []*transaction.Transaction{r.sign(&x, uint256.NewInt(1e18), 10_000_000, nil)}, systemAddr)
	if op, ok := b.account(x); !ok || op.Op != "write" {
		dbg, _ := json.MarshalIndent(b, "", " ")
		t.Fatalf("plain-transfer: recipient not written\n%s", dbg)
	}
	main.Blocks = append(main.Blocks, b)

	b = r.run("contract-create", "The sender deploys a store contract whose initcode sets slot 1 = 1 and sets slot 2 = 5 then back to 0 (D5: slot 2 starts and ends at 0, so no op).",
		types.Hash{}, []*transaction.Transaction{r.sign(nil, uint256.NewInt(0), 10_000_000, v3StoreInitcode)}, systemAddr)
	if op, ok := b.account(contract); !ok || op.Op != "write" {
		t.Fatal("contract-create: contract not written")
	}
	if op, ok := b.slot(contract, 1); !ok || op.Op != "write" {
		t.Fatal("contract-create: slot 1 not written")
	}
	if _, ok := b.slot(contract, 2); ok {
		t.Fatal("contract-create: slot 2 (set and reset to 0 in the creation) appears (D5)")
	}
	main.Blocks = append(main.Blocks, b)

	b = r.run("storage-set", "Slot 1: 1 -> 2 (write); slot 3: 0 -> 4 (write).",
		types.Hash{}, []*transaction.Transaction{
			r.sign(&contract, uint256.NewInt(0), 10_000_000, storeCall(1, 2)),
			r.sign(&contract, uint256.NewInt(0), 10_000_000, storeCall(3, 4)),
		}, systemAddr)
	if op, ok := b.slot(contract, 1); !ok || op.Op != "write" {
		t.Fatal("storage-set: slot 1 not written")
	}
	main.Blocks = append(main.Blocks, b)

	b = r.run("storage-reset-and-unchanged", "Slot 1: 2 -> 0 (delete); slot 3: stored 4 again (unchanged, no op); slot 5: 0 -> 6 -> 0 across two transactions (no op). gov5 writes the contract's account leaf because its storage changed, although nonce, balance and code hash are unchanged (OPEN, see README).",
		types.Hash{}, []*transaction.Transaction{
			r.sign(&contract, uint256.NewInt(0), 10_000_000, storeCall(1, 0)),
			r.sign(&contract, uint256.NewInt(0), 10_000_000, storeCall(3, 4)),
			r.sign(&contract, uint256.NewInt(0), 10_000_000, storeCall(5, 6)),
			r.sign(&contract, uint256.NewInt(0), 10_000_000, storeCall(5, 0)),
		}, systemAddr)
	if op, ok := b.slot(contract, 1); !ok || op.Op != "delete" {
		t.Fatal("storage-reset: slot 1 not deleted")
	}
	if _, ok := b.slot(contract, 3); ok {
		t.Fatal("storage-unchanged: slot 3 appears")
	}
	if _, ok := b.slot(contract, 5); ok {
		t.Fatal("storage-unchanged: slot 5 appears")
	}
	main.Blocks = append(main.Blocks, b)

	b = r.run("a9-no-op", "A zero-value transfer to the existing account 0x..aa: it is touched but unchanged, so it does not appear (A9); the sender (nonce, fee) does.",
		types.Hash{}, []*transaction.Transaction{r.sign(&x, uint256.NewInt(0), 10_000_000, nil)}, systemAddr, x)
	main.Blocks = append(main.Blocks, b)

	b = r.run("empty-account-touch", "A zero-value transfer to 0x..bb, which never existed: EIP-161 removes the touched empty account (D4, no self-destruct). The record shows whether gov5 emits an op for it.",
		types.Hash{}, []*transaction.Transaction{r.sign(&never, uint256.NewInt(0), 10_000_000, nil)}, systemAddr)
	main.Blocks = append(main.Blocks, b)
	writeProfile1Vector(t, "V3", "main.json", main)

	// Chain "system-address-live": 0xff..fe exists with 1 wei; the system
	// calls leave it unchanged, so on Profile 1 it is not written (D3).
	liveAlloc := map[types.Address]conf.GenesisAccount{sender: {Balance: v3SenderBalanceWei}, systemAddr: {Balance: "0x1"}}
	r, groot = newV3Runner(t, liveAlloc)
	live := v3Chain{
		Vector:      "V3",
		Chain:       "system-address-live",
		Description: "Standard Profile-1 genesis plus the funded test sender and 0xff..fe holding 1 wei. The EIP-2935/4788/7002/7251 system calls leave 0xff..fe unchanged, so under A9 it is not written (D3).",
		Genesis:     "internal/testdata/profile1/standard_genesis.json",
		AllocAdded:  liveAlloc,
		GenesisRoot: groot,
		Notes:       notes,
	}
	live.Blocks = append(live.Blocks,
		r.run("empty-block", "No transactions; 0xff..fe exists and is unchanged: no op (D3).", types.Hash{}, nil, systemAddr),
		r.run("plain-transfer", "1 ether to 0x..aa; 0xff..fe still unchanged.", beacon,
			[]*transaction.Transaction{r.sign(&x, uint256.NewInt(1e18), 10_000_000, nil)}, systemAddr))
	writeProfile1Vector(t, "V3", "system_address_live.json", live)
	mirrorProfile1File(t, "V3", "README.md")
}
