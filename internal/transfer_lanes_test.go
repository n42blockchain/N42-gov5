// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// laneWriteRecorder records the state writes a CommitBlock makes, in order:
// what the block's changesets, history and rows are built from.
type laneWriteRecorder struct{ w []string }

func laneAccString(a *account.StateAccount) string {
	if a == nil {
		return "nil"
	}
	return fmt.Sprintf("{n=%d b=%s ch=%x r=%x i=%v}", a.Nonce, a.Balance.Hex(), a.CodeHash[:4], a.Root[:4], a.Initialised)
}

func (r *laneWriteRecorder) UpdateAccountData(a types.Address, orig, acc *account.StateAccount) error {
	r.w = append(r.w, fmt.Sprintf("acc %x %s -> %s", a, laneAccString(orig), laneAccString(acc)))
	return nil
}

func (r *laneWriteRecorder) UpdateAccountCode(a types.Address, h types.Hash, code []byte) error {
	r.w = append(r.w, fmt.Sprintf("code %x %x %d", a, h[:4], len(code)))
	return nil
}

func (r *laneWriteRecorder) DeleteAccount(a types.Address, orig *account.StateAccount) error {
	r.w = append(r.w, fmt.Sprintf("del %x %s", a, laneAccString(orig)))
	return nil
}

func (r *laneWriteRecorder) WriteAccountStorage(a types.Address, k types.Hash, orig, v uint256.Int) error {
	r.w = append(r.w, fmt.Sprintf("sto %x %x %s -> %s", a, k[:4], orig.Hex(), v.Hex()))
	return nil
}

func (r *laneWriteRecorder) CreateContract(a types.Address) error {
	r.w = append(r.w, fmt.Sprintf("create %x", a))
	return nil
}

// laneBlockOutcome is everything an ahead run of a block hands downstream:
// its error, receipts (encoded, root, bloom), logs, gas, the post-state's
// root and accounts, and the ordered write set CommitBlock makes of it.
type laneBlockOutcome struct {
	err          string
	usedGas      uint64
	receipts     [][]byte
	receiptsRoot types.Hash
	bloom        block.Bloom
	logs         int
	stateRoot    types.Hash
	accounts     []string
	writes       []string
	// lanesUsed: the block took the lanes; lanesChecked: check mode
	// compared a lane run with today's.
	lanesUsed, lanesChecked bool
	// lanesDeclined: the lanes were offered the block and declined it.
	lanesDeclined bool
}

// runLaneBlock runs header+txs through ExecuteParallelAhead on db's state
// with N42_EXEC_TRANSFER_LANES = mode ("off", "on", "check").
func runLaneBlock(t *testing.T, db kv.RwDB, cfg *params.ChainConfig, header *block.Header, txs []*transaction.Transaction, mode string, addrs []types.Address) laneBlockOutcome {
	t.Helper()
	defer SetTransferLanesForTest(mode)()
	bc := &BlockChain{ctx: context.Background(), ChainDB: db}
	sp := NewStateProcessor(cfg, bc, nil)
	blk := block.NewBlock(header, txs).(*block.Block)
	var out laneBlockOutcome
	used0, declined0, checked0, mism0 := TransferLanesStats()
	require.NoError(t, db.View(context.Background(), func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		run, err := sp.ExecuteParallelAhead(blk, ibs, func(uint64) types.Hash { return types.Hash{} })
		if err != nil {
			out.err = err.Error()
			return nil
		}
		out.usedGas = run.UsedGas
		for i := range run.Receipts {
			var buf bytes.Buffer
			run.Receipts.EncodeIndex(i, &buf)
			out.receipts = append(out.receipts, buf.Bytes())
		}
		out.receiptsRoot = DeriveSha(run.Receipts)
		out.bloom = block.CreateBloom(run.Receipts)
		out.logs = len(run.Logs)
		for _, a := range addrs {
			out.accounts = append(out.accounts, fmt.Sprintf("%x n=%d b=%s ch=%x e=%v", a, ibs.GetNonce(a), ibs.GetBalance(a).Hex(), ibs.GetCodeHash(a), ibs.Exist(a)))
		}
		out.stateRoot = ibs.IntermediateRoot()
		rec := &laneWriteRecorder{}
		require.NoError(t, ibs.CommitBlock(cfg.Rules(header.Number.Uint64()), rec))
		out.writes = rec.w
		return nil
	}))
	used1, declined1, checked1, mism1 := TransferLanesStats()
	out.lanesUsed = used1 > used0
	out.lanesDeclined = declined1 > declined0
	out.lanesChecked = checked1 > checked0
	require.Equal(t, mism0, mism1, "check mode found a difference")
	return out
}

// requireSameLaneOutcome compares two outcomes field by field.
func requireSameLaneOutcome(t *testing.T, want, got laneBlockOutcome, what string) {
	t.Helper()
	require.Equal(t, want.err, got.err, "%s: error", what)
	require.Equal(t, want.usedGas, got.usedGas, "%s: gas", what)
	require.Equal(t, want.receipts, got.receipts, "%s: receipts", what)
	require.Equal(t, want.receiptsRoot, got.receiptsRoot, "%s: receipts root", what)
	require.Equal(t, want.bloom, got.bloom, "%s: bloom", what)
	require.Equal(t, want.logs, got.logs, "%s: logs", what)
	require.Equal(t, want.accounts, got.accounts, "%s: accounts", what)
	require.Equal(t, want.stateRoot, got.stateRoot, "%s: state root", what)
	require.Equal(t, want.writes, got.writes, "%s: write set", what)
}

// laneModes runs a block off, on and in check mode and requires the three
// outcomes equal; it returns the "on" outcome.
func laneModes(t *testing.T, db kv.RwDB, cfg *params.ChainConfig, header *block.Header, txs []*transaction.Transaction, addrs []types.Address) (off, on laneBlockOutcome) {
	t.Helper()
	off = runLaneBlock(t, db, cfg, header, txs, "off", addrs)
	require.False(t, off.lanesUsed, "lanes used with the switch off")
	on = runLaneBlock(t, db, cfg, header, txs, "on", addrs)
	check := runLaneBlock(t, db, cfg, header, txs, "check", addrs)
	require.False(t, check.lanesUsed, "check mode used the lanes' result")
	require.Equal(t, on.lanesUsed, check.lanesChecked, "check mode compared exactly the blocks the lanes take")
	requireSameLaneOutcome(t, off, on, "lanes on")
	requireSameLaneOutcome(t, off, check, "lanes check")
	return off, on
}

// TestTransferLanesBenchShape: blocks shaped like the bench's (sender
// chains of ~40 transfers, Zipf or modulo recipients, no sender also a
// recipient) take the lanes and match today's path in every output.
func TestTransferLanesBenchShape(t *testing.T) {
	for _, c := range []struct {
		name                 string
		txs, senders, recips int
		zipf, v              float64
	}{
		{"chains_zipf", 4000, 100, 570, 1.1, 2},
		{"chains_modulo", 3000, 75, 97, 0, 0},
		{"distinct_senders", 2000, 2000, 300, 1.2, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			fx := newTransferBlockFixture(c.txs, c.senders, c.recips, c.zipf, c.v)
			db := memdb.NewTestDB(t)
			fundTransferBlockStateFiller(t, db, fx, 100)
			addrs := append(append(append([]types.Address{}, fx.senders...), fx.recipients...), fx.header.Coinbase)
			_, on := laneModes(t, db, fx.cfg, fx.header, fx.txs, addrs)
			require.True(t, on.lanesUsed, "the bench shape did not take the lanes")
			require.Empty(t, on.err)
			require.Len(t, on.receipts, c.txs)
		})
	}
}

// laneFuzz is a randomised plain-transfer block over a small state: funded
// senders (one poor, one exactly drained, one very rich), existing, empty
// and nearly-full recipients, an absent address, a contract, a delegated
// account, a precompile and the coinbase.
type laneFuzz struct {
	cfg      *params.ChainConfig
	header   *block.Header
	signer   transaction.Signer
	keys     []*ecdsa.PrivateKey
	senders  []types.Address
	nonces   []uint64
	recips   []types.Address
	empty    types.Address
	full     types.Address
	absent   types.Address
	contract types.Address
	delegate types.Address
	addrs    []types.Address
	rnd      *rand.Rand
}

// Sender roles in laneFuzz.senders.
const (
	laneFuzzPoor   = 0 // pays for two transfers
	laneFuzzDrain  = 1 // pays for exactly its transfers
	laneFuzzMega   = 2 // 2^200
	laneFuzzPerTx  = 21000 * 1_000_000_000 // gas * fee cap
	laneFuzzValue  = 1000
	laneFuzzChains = 12
	laneFuzzChain  = 25
)

func newLaneFuzz(t *testing.T, seed int64) *laneFuzz {
	t.Helper()
	f := &laneFuzz{cfg: params.MainnetQMDBChainConfig, rnd: rand.New(rand.NewSource(seed))}
	f.header = &block.Header{
		Number:     uint256.NewInt(1),
		Time:       uint64(time.Now().Unix()),
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(7),
		GasLimit:   1_000_000_000,
		Coinbase:   types.Address{0xC0, 0x1B, 0xAE},
	}
	f.signer = transaction.MakeSignerWithTimestamp(f.cfg, f.header.Number.ToBig(), f.header.Time)
	for i := 0; i < laneFuzzChains; i++ {
		k, err := crypto.GenerateKey()
		require.NoError(t, err)
		f.keys = append(f.keys, k)
		f.senders = append(f.senders, crypto.PubkeyToAddress(k.PublicKey))
	}
	f.nonces = make([]uint64, len(f.keys))
	for i := 0; i < 40; i++ {
		f.recips = append(f.recips, types.BytesToAddress(crypto.Keccak256([]byte("lane-fuzz"), []byte{byte(seed), byte(i)})))
	}
	f.empty = types.Address{0xE0, 0x01}
	f.full = types.Address{0xF0, 0x11}
	f.absent = types.Address{0xAB, 0x5E}
	f.contract = types.Address{0xCC, 0x01}
	f.delegate = types.Address{0xDE, 0x1E}
	f.addrs = append(append(append([]types.Address{}, f.senders...), f.recips...), f.empty, f.full, f.absent, f.contract, f.delegate,
		f.header.Coinbase, types.BytesToAddress([]byte{1}))
	if c := f.cfg.Eip1559FeeCollector; c != nil {
		f.addrs = append(f.addrs, *c)
	}
	return f
}

// fund writes f's state into db; the drained sender gets exactly what its
// transactions in specs cost.
func (f *laneFuzz) fund(t *testing.T, db kv.RwDB, specs []laneTxSpec) {
	t.Helper()
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		writer := state.NewPlainStateWriter(tx, tx, 1)
		ibs := state.New(state.NewPlainState(tx, 1))
		rich := new(uint256.Int).Lsh(uint256.NewInt(1), 100)
		for i, a := range f.senders {
			ibs.CreateAccount(a, true)
			switch i {
			case laneFuzzPoor:
				ibs.SetBalance(a, uint256.NewInt(2*(laneFuzzPerTx+laneFuzzValue)))
			case laneFuzzDrain:
				need := new(uint256.Int)
				for _, sp := range specs {
					if sp.sender == laneFuzzDrain {
						c := new(uint256.Int).Mul(uint256.NewInt(sp.gas), uint256.NewInt(sp.feeCap))
						need.Add(need, c.Add(c, sp.value))
					}
				}
				ibs.SetBalance(a, need)
			case laneFuzzMega:
				ibs.SetBalance(a, new(uint256.Int).Lsh(uint256.NewInt(1), 200))
			default:
				ibs.SetBalance(a, rich)
			}
		}
		for _, a := range f.recips {
			ibs.CreateAccount(a, true)
			ibs.SetBalance(a, uint256.NewInt(1))
		}
		ibs.CreateAccount(f.full, true)
		ibs.SetBalance(f.full, new(uint256.Int).Sub(new(uint256.Int).SetAllOne(), uint256.NewInt(10)))
		ibs.CreateAccount(f.contract, true)
		ibs.SetCode(f.contract, leanCounterCode)
		ibs.CreateAccount(f.delegate, true)
		ibs.SetBalance(f.delegate, uint256.NewInt(5))
		ibs.SetCode(f.delegate, append([]byte{0xef, 0x01, 0x00}, f.recips[0][:]...))
		if err := ibs.FinalizeTx(&params.Rules{IsSpuriousDragon: true, IsBerlin: true, IsLondon: true}, writer); err != nil {
			return err
		}
		// An existing empty account (no EIP-161 sweep for this write).
		ibs2 := state.New(state.NewPlainState(tx, 1))
		ibs2.CreateAccount(f.empty, true)
		return ibs2.FinalizeTx(&params.Rules{}, writer)
	}))
}

// laneTxSpec is one transaction of a fuzz block.
type laneTxSpec struct {
	sender     int
	nonce      uint64
	to         types.Address
	value      *uint256.Int
	gas        uint64
	feeCap     uint64
	tip        uint64
	txType     byte
	accessList transaction.AccessList
	data       []byte
}

// sign signs s.
func (f *laneFuzz) sign(t *testing.T, s laneTxSpec) *transaction.Transaction {
	t.Helper()
	chainID, _ := uint256.FromBig(f.cfg.ChainID)
	to := s.to
	var inner transaction.TxData
	switch s.txType {
	case transaction.LegacyTxType:
		inner = &transaction.LegacyTx{Nonce: s.nonce, GasPrice: uint256.NewInt(s.feeCap), Gas: s.gas, To: &to, Value: s.value, Data: s.data}
	case transaction.AccessListTxType:
		inner = &transaction.AccessListTx{ChainID: chainID, Nonce: s.nonce, GasPrice: uint256.NewInt(s.feeCap), Gas: s.gas, To: &to, Value: s.value, Data: s.data, AccessList: s.accessList}
	default:
		inner = &transaction.DynamicFeeTx{ChainID: chainID, Nonce: s.nonce, GasTipCap: uint256.NewInt(s.tip), GasFeeCap: uint256.NewInt(s.feeCap), Gas: s.gas, To: &to, Value: s.value, Data: s.data}
	}
	tx, err := transaction.SignNewTx(f.keys[s.sender], f.signer, inner)
	require.NoError(t, err)
	return tx
}

// laneFuzzPerturbations names what a fuzz block may contain beyond clean
// transfers.
var laneFuzzPerturbations = []string{
	"senderRecipient", "self", "zeroValue", "absent", "empty", "coinbase", "maxValue", "nearFull",
	"nonceGap", "nonceDup", "poor", "gasHigh", "gasLow", "feeCapLow", "tipOverCap", "accessListEmpty",
	"accessList", "contract", "delegate", "precompile", "call", "legacy", "drain", "blockGasLimit",
}

// block builds a block's specs: every sender's chain in round-robin order,
// with the given perturbations applied to random transactions.
func (f *laneFuzz) block(perturb []string) []laneTxSpec {
	var specs []laneTxSpec
	for n := 0; n < laneFuzzChain; n++ {
		for s := range f.senders {
			if s == laneFuzzPoor && n >= 2 && !slices.Contains(perturb, "poor") {
				continue
			}
			typ := byte(transaction.DynamicFeeTxType)
			if f.rnd.Intn(3) == 0 || s == laneFuzzDrain {
				typ = transaction.LegacyTxType
			}
			specs = append(specs, laneTxSpec{sender: s, nonce: uint64(n), to: f.recips[f.rnd.Intn(len(f.recips))],
				value: uint256.NewInt(1 + uint64(f.rnd.Intn(laneFuzzValue))), gas: params.TxGas, feeCap: 1_000_000_000, tip: uint64(f.rnd.Intn(3)), txType: typ})
		}
	}
	pick := func() *laneTxSpec { return &specs[f.rnd.Intn(len(specs))] }
	for _, p := range perturb {
		sp := pick()
		switch p {
		case "senderRecipient":
			sp.to = f.senders[(sp.sender+1)%len(f.senders)]
		case "self":
			sp.to = f.senders[sp.sender]
		case "zeroValue":
			sp.value = new(uint256.Int)
		case "absent":
			sp.to = f.absent
		case "empty":
			sp.to = f.empty
		case "coinbase":
			sp.to = f.header.Coinbase
		case "maxValue":
			for _, s := range specs {
				if s.sender == laneFuzzMega {
					sp = &specs[slices.IndexFunc(specs, func(x laneTxSpec) bool { return x.sender == laneFuzzMega && x.nonce == s.nonce })]
					break
				}
			}
			sp.value = new(uint256.Int).SetUint64(1<<63 + uint64(f.rnd.Intn(1000)))
		case "nearFull":
			sp.to = f.full
		case "nonceGap":
			sp.nonce += 2
		case "nonceDup":
			if sp.nonce > 0 {
				sp.nonce--
			}
		case "gasHigh":
			sp.gas = 30000
		case "gasLow":
			sp.gas = 20000
		case "feeCapLow":
			sp.feeCap = 6
			sp.tip = 0
		case "tipOverCap":
			sp.txType = transaction.DynamicFeeTxType
			sp.tip = sp.feeCap + 1
		case "accessListEmpty":
			sp.txType = transaction.AccessListTxType
		case "accessList":
			sp.txType = transaction.AccessListTxType
			sp.accessList = transaction.AccessList{{Address: sp.to}}
			sp.gas = params.TxGas + params.TxAccessListAddressGas
		case "contract":
			sp.to = f.contract
		case "delegate":
			sp.to = f.delegate
		case "precompile":
			sp.to = types.BytesToAddress([]byte{1})
		case "call":
			sp.to = f.contract
			sp.gas = 100_000
			sp.data = []byte{1}
		case "legacy":
			sp.txType = transaction.LegacyTxType
		}
	}
	return specs
}

// TestTransferLanesRandomBlocks runs randomised blocks (clean ones and ones
// with one to three perturbations each: senders that receive, self and
// zero-value transfers, absent, empty, nearly-full and coinbase
// recipients, maximal values, nonce gaps and duplicates, a sender out of
// funds mid-chain, gas and fee-cap failures, other transaction types,
// contracts, delegations, precompiles, a block gas limit below a transfer)
// off, on and in check mode: every outcome, the errors included, must be
// the same, and the clean ones must take the lanes.
func TestTransferLanesRandomBlocks(t *testing.T) {
	seeds := 48
	if testing.Short() {
		seeds = 12
	}
	usedClean, used, declined := 0, 0, 0
	for seed := int64(1); seed <= int64(seeds); seed++ {
		f := newLaneFuzz(t, seed)
		var perturb []string
		if seed%4 != 1 {
			for n := 1 + f.rnd.Intn(3); n > 0; n-- {
				perturb = append(perturb, laneFuzzPerturbations[f.rnd.Intn(len(laneFuzzPerturbations))])
			}
		}
		t.Run(fmt.Sprintf("seed%d_%v", seed, perturb), func(t *testing.T) {
			db := memdb.NewTestDB(t)
			specs := f.block(perturb)
			f.fund(t, db, specs)
			txs := make([]*transaction.Transaction, len(specs))
			for i, s := range specs {
				txs[i] = f.sign(t, s)
			}
			header := *f.header
			if slices.Contains(perturb, "blockGasLimit") {
				header.GasLimit = params.TxGas - 1
			}
			_, on := laneModes(t, db, f.cfg, &header, txs, f.addrs)
			if on.lanesUsed {
				used++
			} else {
				declined++
			}
			clean := true
			for _, p := range perturb {
				if p != "empty" && p != "maxValue" && p != "legacy" && p != "drain" {
					clean = false
				}
			}
			if clean {
				require.True(t, on.lanesUsed, "a block of plain transfers did not take the lanes")
				require.Empty(t, on.err)
				usedClean++
			}
		})
	}
	t.Logf("lanes used %d (clean %d), declined %d", used, usedClean, declined)
	require.NotZero(t, usedClean)
	require.NotZero(t, declined)
}

// TestTransferLanesQualification: each perturbation alone. A block the
// lanes cover takes them; every other block takes today's path -- offered
// to the lanes and declined, or never offered when its fees cannot be
// deferred -- with the same outcome, its error included.
func TestTransferLanesQualification(t *testing.T) {
	const (
		used     = "used"
		declined = "declined"
		untried  = "untried"
	)
	for i, c := range []struct {
		perturb string
		want    string
	}{
		{"", used}, {"empty", used}, {"maxValue", used}, {"legacy", used},
		{"senderRecipient", declined}, {"self", declined}, {"zeroValue", declined}, {"absent", declined},
		{"nearFull", declined}, {"nonceGap", declined}, {"nonceDup", declined}, {"poor", declined},
		{"gasHigh", declined}, {"gasLow", declined}, {"feeCapLow", declined}, {"tipOverCap", declined},
		{"accessListEmpty", declined}, {"accessList", declined}, {"contract", declined}, {"delegate", declined},
		{"blockGasLimit", declined},
		{"coinbase", untried}, {"precompile", untried}, {"call", untried},
	} {
		t.Run(c.perturb, func(t *testing.T) {
			f := newLaneFuzz(t, int64(1000+i))
			var perturb []string
			if c.perturb != "" {
				perturb = []string{c.perturb}
			}
			specs := f.block(perturb)
			db := memdb.NewTestDB(t)
			f.fund(t, db, specs)
			txs := make([]*transaction.Transaction, len(specs))
			for i, s := range specs {
				txs[i] = f.sign(t, s)
			}
			header := *f.header
			if c.perturb == "blockGasLimit" {
				header.GasLimit = params.TxGas - 1
			}
			off, on := laneModes(t, db, f.cfg, &header, txs, f.addrs)
			switch c.want {
			case used:
				require.True(t, on.lanesUsed, "not taken")
				require.Empty(t, on.err)
			case declined:
				require.False(t, on.lanesUsed, "taken")
				require.True(t, on.lanesDeclined, "not offered")
			case untried:
				require.False(t, on.lanesUsed, "taken")
				require.False(t, on.lanesDeclined, "offered")
			}
			t.Logf("%s: err %q", c.want, off.err)
		})
	}
}
