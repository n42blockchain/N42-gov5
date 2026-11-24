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
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// Vector V4 of the mixed production standard (Profile 1): transaction
// validity. Each case is one transaction in EIP-2718 bytes, judged by gov5
// as block validity does on the standard genesis (plus the funded test
// sender of V3) in block 1: decode, the type rule (C1-C4), the deferred
// includability check, then execution.

type v4Check struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type v4Case struct {
	Case        string        `json:"case"`
	Rule        string        `json:"rule"`
	Description string        `json:"description"`
	Type        string        `json:"typeByte"`
	Raw         hexutil.Bytes `json:"raw"`
	Verdict     string        `json:"verdict"` // "valid" or "invalid"
	Stage       string        `json:"refusedAt,omitempty"`
	Decode      v4Check       `json:"decode"`
	TypeRule    *v4Check      `json:"typeRule,omitempty"`
	Includable  *v4Check      `json:"includable,omitempty"`
	Execution   *v4Check      `json:"execution,omitempty"`
}

type v4File struct {
	Vector      string                                `json:"vector"`
	Description string                                `json:"description"`
	Genesis     string                                `json:"genesis"`
	AllocAdded  map[types.Address]conf.GenesisAccount `json:"allocAdditions"`
	Block       v3Header                              `json:"block"`
	Cases       []v4Case                              `json:"cases"`
	Notes       []string                              `json:"notes"`
}

func v4CheckOf(err error) *v4Check {
	if err != nil {
		return &v4Check{Error: err.Error()}
	}
	return &v4Check{OK: true}
}

// judge decides raw's validity on a fresh copy of the V4 chain.
func v4Judge(t *testing.T, alloc map[types.Address]conf.GenesisAccount, name, rule, desc string, raw []byte) v4Case {
	t.Helper()
	c := v4Case{Case: name, Rule: rule, Description: desc, Type: hexutil.Encode(raw[:1]), Raw: raw}
	if raw[0] >= 0xc0 {
		c.Type = "0x00"
	}
	r, _ := newV3Runner(t, alloc)
	header := &block.Header{
		ParentHash:       r.parent.Hash(),
		Coinbase:         r.coinbase,
		Number:           uint256.NewInt(1),
		GasLimit:         r.parent.GasLimit,
		Time:             r.parent.Time + 1,
		BaseFee:          uint256.NewInt(v3BaseFee),
		Difficulty:       uint256.NewInt(0),
		ParentBeaconRoot: &types.Hash{},
	}
	tx, err := transaction.DecodeEthereumTransaction(raw)
	c.Decode = *v4CheckOf(err)
	fail := func(stage string) v4Case {
		c.Verdict, c.Stage = "invalid", stage
		return c
	}
	if err != nil {
		return fail("decode")
	}
	if c.TypeRule = v4CheckOf(CheckProfile1TxType(r.cfg, tx.Type())); !c.TypeRule.OK {
		return fail("typeRule")
	}
	_, err = deferredTxPlan(r.cfg, header, []*transaction.Transaction{tx})
	if c.Includable = v4CheckOf(err); !c.Includable.OK {
		return fail("includable")
	}
	ibs := state.New(state.NewPlainStateReader(r.tx))
	ibs.ApplyUnchangedAccountRule(r.cfg, header.Time)
	if err := ProcessExecutionBlockStart(header.ParentBeaconRoot, r.cfg, ibs, header, nil); err != nil {
		t.Fatal(err)
	}
	gp := new(common.GasPool).AddGas(header.GasLimit)
	var used uint64
	ibs.Prepare(tx.Hash(), types.Hash{}, 0)
	_, _, err = ApplyTransaction(r.cfg, func(uint64) types.Hash { return types.Hash{} }, nil, &r.coinbase, gp, ibs, state.NewNoopWriter(), header, tx, &used, vm2.Config{})
	if c.Execution = v4CheckOf(err); !c.Execution.OK {
		return fail("execution")
	}
	c.Verdict = "valid"
	return c
}

// TestProfile1VectorV4 generates and checks the V4 transaction-validity
// vectors.
func TestProfile1VectorV4(t *testing.T) {
	key, sender := v3Key(t)
	alloc := map[types.Address]conf.GenesisAccount{sender: {Balance: v3SenderBalanceWei}}
	g, err := loadProfile1Genesis(t, "testdata/profile1/standard_genesis.json")
	if err != nil {
		t.Fatal(err)
	}
	cfg := g.Config
	chainID, _ := uint256.FromBig(cfg.ChainID)
	signer := transaction.LatestSignerForChainID(cfg.ChainID)
	to := types.HexToAddress("0x00000000000000000000000000000000000000aa")
	sign := func(inner transaction.TxData) []byte {
		t.Helper()
		tx, err := transaction.SignTx(transaction.NewTx(inner), signer, key)
		if err != nil {
			t.Fatal(err)
		}
		enc, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			t.Fatal(err)
		}
		return enc
	}
	const gas = 10_000_000
	tip, feeCap := uint256.NewInt(v3TipCap), uint256.NewInt(v3FeeCap)
	dynamic := func(to *types.Address, data []byte, gasLimit uint64) []byte {
		return sign(&transaction.DynamicFeeTx{ChainID: chainID, GasTipCap: tip, GasFeeCap: feeCap, Gas: gasLimit, To: to, Value: uint256.NewInt(1), Data: data})
	}
	authKey, err := crypto.ToECDSA(crypto.Keccak256([]byte("n42-profile1-vectors-authority")))
	if err != nil {
		t.Fatal(err)
	}
	auth := signAuthorization(t, authKey, &transaction.Authorization{ChainID: *chainID, Address: types.HexToAddress("0x00000000000000000000000000000000000000cc")})
	setCode := func(to *types.Address, auths transaction.AuthorizationList) []byte {
		return sign(&transaction.SetCodeTx{ChainID: chainID, GasTipCap: tip, GasFeeCap: feeCap, Gas: gas, To: to, Value: uint256.NewInt(0), AuthList: auths})
	}
	retype := func(typ byte, raw []byte) []byte {
		out := append([]byte(nil), raw...)
		out[0] = typ
		return out
	}
	type2 := dynamic(&to, nil, gas)
	blob := sign(&transaction.BlobTx{ChainID: chainID, GasTipCap: tip, GasFeeCap: feeCap, Gas: gas, To: to, Value: uint256.NewInt(1),
		BlobFeeCap: feeCap, BlobHashes: []types.Hash{{transaction.VersionedHashVersionKZG}}})

	cases := []struct {
		name, rule, desc string
		raw              []byte
		valid            bool
	}{
		{"type-0-legacy", "C1", "EIP-155 legacy transfer.", sign(&transaction.LegacyTx{GasPrice: feeCap, Gas: gas, To: &to, Value: uint256.NewInt(1)}), true},
		{"type-1-access-list", "C1", "EIP-2930 transfer.", sign(&transaction.AccessListTx{ChainID: chainID, GasPrice: feeCap, Gas: gas, To: &to, Value: uint256.NewInt(1)}), true},
		{"type-2-dynamic-fee", "C1", "EIP-1559 transfer.", type2, true},
		{"type-3-blob", "C2", "EIP-4844 blob transaction: invalid on Profile 1 (O2).", blob, false},
		{"type-4-set-code", "C3", "EIP-7702 transaction with one authorization.", setCode(&to, transaction.AuthorizationList{auth}), true},
		{"type-4-empty-auth-list", "C3", "EIP-7702 transaction with an empty authorization list.", setCode(&to, transaction.AuthorizationList{}), false},
		{"type-4-create", "C3", "EIP-7702 transaction without a recipient.", setCode(nil, transaction.AuthorizationList{auth}), false},
		{"type-5-post-quantum", "C4", "gov5's post-quantum type byte 0x05 on the type-2 payload: not in Profile 1.", retype(0x05, type2), false},
		{"type-6", "C4", "Type byte 0x06 on the type-2 payload: not in Profile 1.", retype(0x06, type2), false},
		{"type-0x50", "C4", "Type byte 0x50 (N42 Ed25519, O3) on the type-2 payload.", retype(0x50, type2), false},
		{"type-0x51", "C4", "Type byte 0x51 (reserved) on the type-2 payload.", retype(0x51, type2), false},
		{"type-0x5f", "C4", "Type byte 0x5F (reserved) on the type-2 payload.", retype(0x5f, type2), false},
		{"gas-at-block-limit", "C5", "Type-2 transfer with gas = the 480M block gas limit.", dynamic(&to, nil, 480_000_000), true},
		{"gas-at-cap", "C5", "Type-2 transfer with gas 2^32-1 (EIP-8037 cap): above the 480M block gas limit, so gov5's includability check refuses it (OPEN, see README).", dynamic(&to, nil, params.MaxTxTotalGasEIP8037), false},
		{"gas-over-cap", "C5", "Type-2 transfer with gas 2^32.", dynamic(&to, nil, params.MaxTxTotalGasEIP8037+1), false},
		{"initcode-at-cap", "C6", "Creation with 131,072 bytes of initcode (EIP-7954).", dynamic(nil, make([]byte, params.MaxInitCodeSizeEIP7954), 100_000_000), true},
		{"initcode-over-cap", "C6", "Creation with 131,073 bytes of initcode.", dynamic(nil, make([]byte, params.MaxInitCodeSizeEIP7954+1), 100_000_000), false},
	}
	file := v4File{
		Vector:      "V4",
		Description: "Transaction validity on Standard Profile 1, as gov5's block validity decides it.",
		Genesis:     "internal/testdata/profile1/standard_genesis.json",
		AllocAdded:  alloc,
		Notes: []string{
			"Each case is judged alone in block 1 on the standard genesis plus the funded test sender (key = keccak256(\"" + v3SenderSeed + "\")); the block header is given in 'block'.",
			"Stages in order: decode (EIP-2718 bytes), typeRule (C1-C4: only 0x00, 0x01, 0x02, 0x04), includable (deferred-execution includability: gas, fee cap, intrinsic gas, initcode cap, EIP-7702 shape), execution. refusedAt names the first stage that refused; the error strings are gov5's and informative only.",
			"Pool policy is a subset of these verdicts (C7): a gov5 Profile-1 pool refuses every case marked invalid.",
		},
	}
	for _, tc := range cases {
		c := v4Judge(t, alloc, tc.name, tc.rule, tc.desc, tc.raw)
		if (c.Verdict == "valid") != tc.valid {
			t.Errorf("%s: verdict %s at %s (%+v %+v %+v %+v), want valid=%v", tc.name, c.Verdict, c.Stage, c.Decode, c.TypeRule, c.Includable, c.Execution, tc.valid)
		}
		file.Cases = append(file.Cases, c)
	}
	// Every type byte the decoder accepts is still held to the type rule.
	for _, typ := range []uint8{transaction.BlobTxType, transaction.PostQuantumTxType, 0x50, 0x51, 0x5f} {
		if err := CheckProfile1TxType(cfg, typ); !errors.Is(err, ErrTxTypeNotInProfile1) {
			t.Errorf("type 0x%02x passes the type rule: %v", typ, err)
		}
	}
	r, _ := newV3Runner(t, alloc)
	file.Block = v3Header{Number: 1, Timestamp: r.parent.Time + 1, ParentHash: r.parent.Hash(), Coinbase: r.coinbase,
		GasLimit: r.parent.GasLimit, BaseFeePerGas: v3BaseFee}
	writeProfile1Vector(t, "V4", "transactions.json", file)
	mirrorProfile1File(t, "V4", "README.md")
}
