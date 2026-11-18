// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// The cross-client fixture for gov5's Amsterdam execution on an N42-native
// chain (the n42-rs gated devnet genesis, chain 1143, glamsterdamTime 0):
// one block of the transaction shapes the native chain carries, executed
// with gov5's serial execution, written as JSON for the Rust client
// (n42-rs docs/GLAMSTERDAM_EXECUTION_PARITY.md). N42_WRITE_FIXTURES=1
// rewrites the file; otherwise the test checks that gov5 still produces it.

const glamFixtureDir = "testdata/glamsterdam_native"

type glamMapReader struct {
	accounts map[types.Address]*account.StateAccount
	storage  map[types.Address]map[types.Hash][]byte
	codes    map[types.Hash][]byte
}

func (m *glamMapReader) ReadAccountData(a types.Address) (*account.StateAccount, error) {
	acc, ok := m.accounts[a]
	if !ok {
		return nil, nil
	}
	cp := *acc
	return &cp, nil
}

func (m *glamMapReader) ReadAccountStorage(a types.Address, k *types.Hash) ([]byte, error) {
	return m.storage[a][*k], nil
}

func (m *glamMapReader) ReadAccountCode(_ types.Address, h types.Hash) ([]byte, error) {
	return m.codes[h], nil
}

func (m *glamMapReader) ReadAccountCodeSize(a types.Address, h types.Hash) (int, error) {
	return len(m.codes[h]), nil
}

type glamFixtureAccount struct {
	Address string            `json:"address"`
	Balance string            `json:"balance"`
	Nonce   uint64            `json:"nonce"`
	Code    string            `json:"code"`
	Storage map[string]string `json:"storage,omitempty"`
}

type glamFixtureLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

type glamFixtureReceipt struct {
	Type              uint8            `json:"type"`
	Status            uint64           `json:"status"`
	GasUsed           uint64           `json:"gasUsed"`
	CumulativeGasUsed uint64           `json:"cumulativeGasUsed"`
	Logs              []glamFixtureLog `json:"logs"`
}

type glamFixture struct {
	Comment       string               `json:"comment"`
	Genesis       string               `json:"genesis"`
	Number        uint64               `json:"number"`
	Timestamp     uint64               `json:"timestamp"`
	GasLimit      uint64               `json:"gasLimit"`
	BaseFee       uint64               `json:"baseFee"`
	Coinbase      string               `json:"coinbase"`
	SlotNumber    uint64               `json:"slotNumber"`
	Pre           []glamFixtureAccount `json:"pre"`
	Transactions  []string             `json:"transactions"`
	Receipts      []glamFixtureReceipt `json:"receipts"`
	GasUsed       uint64               `json:"gasUsed"`
	ReceiptsGas   uint64               `json:"receiptsCumulativeGasUsed"`
	ReceiptsRoot  string               `json:"receiptsRoot"`
	Post          []glamFixtureAccount `json:"post"`
	PostWatchSlot string               `json:"postWatchSlot"`
}

func glamHex(b []byte) string { return "0x" + hex.EncodeToString(b) }

func glamAddr(b byte, tail byte) types.Address {
	var a types.Address
	a[0] = b
	a[19] = tail
	return a
}

func TestGlamsterdamNativeExecutionFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(glamFixtureDir, "n42_devnet_mobileanchor.json"))
	require.NoError(t, err)
	gen := new(conf.Genesis)
	require.NoError(t, json.Unmarshal(raw, gen))
	cfg := params.NormalizeConsensus(gen.Config)
	require.True(t, cfg.IsN42Native(), "the gated devnet is a native chain")
	require.Equal(t, uint64(1143), cfg.ChainID.Uint64())

	const (
		number    = uint64(1)
		timestamp = uint64(1_000)
		gasLimit  = uint64(30_000_000)
		baseFee   = uint64(7)
	)
	require.True(t, cfg.IsGlamsterdam(timestamp))

	keyA, err := crypto.HexToECDSA("1111111111111111111111111111111111111111111111111111111111111111")
	require.NoError(t, err)
	keyB, err := crypto.HexToECDSA("2222222222222222222222222222222222222222222222222222222222222222")
	require.NoError(t, err)
	senderA := crypto.PubkeyToAddress(keyA.PublicKey)
	senderB := crypto.PubkeyToAddress(keyB.PublicKey)

	existing := glamAddr(0xee, 0x01)
	coinbase := glamAddr(0xcb, 0x01)
	freshByTransfer := glamAddr(0xf1, 0x01)
	freshByCall := glamAddr(0xf2, 0x01)
	freshUnpaid := glamAddr(0xf3, 0x01)
	storeValue := glamAddr(0xc0, 0x01) // CALLVALUE PUSH1 0 SSTORE STOP
	forwarder := glamAddr(0xc0, 0x02)  // CALL(gas, freshByCall, callvalue, 0,0,0,0)
	emitter := glamAddr(0xc0, 0x03)    // MSTORE 42, LOG1(0x01) over 32 bytes
	slotReader := glamAddr(0xc0, 0x04) // SLOTNUM PUSH1 0 SSTORE STOP

	codes := map[types.Address][]byte{
		storeValue: {0x34, 0x60, 0x00, 0x55, 0x00},
		forwarder: append(append([]byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x34, 0x73}, freshByCall[:]...),
			0x5a, 0xf1, 0x50, 0x00),
		emitter:    {0x60, 0x2a, 0x60, 0x00, 0x52, 0x60, 0x01, 0x60, 0x20, 0x60, 0x00, 0xa1, 0x00},
		slotReader: {0x4b, 0x60, 0x00, 0x55, 0x00},
	}

	reader := &glamMapReader{
		accounts: map[types.Address]*account.StateAccount{},
		storage:  map[types.Address]map[types.Hash][]byte{},
		codes:    map[types.Hash][]byte{},
	}
	var pre []glamFixtureAccount
	put := func(addr types.Address, balance *uint256.Int, nonce uint64, code []byte) {
		acc := account.NewAccount()
		acc.Initialised = true
		acc.Nonce = nonce
		acc.Balance.Set(balance)
		acc.CodeHash = crypto.Keccak256Hash(code)
		if len(code) == 0 {
			acc.CodeHash = crypto.Keccak256Hash(nil)
		} else {
			reader.codes[acc.CodeHash] = code
		}
		reader.accounts[addr] = &acc
		pre = append(pre, glamFixtureAccount{Address: addr.Hex(), Balance: balance.Hex(), Nonce: nonce, Code: glamHex(code)})
	}
	thousandEth := new(uint256.Int).Mul(uint256.NewInt(1_000), uint256.NewInt(1e18))
	put(senderA, thousandEth, 0, nil)
	put(senderB, thousandEth, 0, nil)
	put(existing, uint256.NewInt(1), 0, nil)
	put(coinbase, uint256.NewInt(1), 0, nil)
	for _, a := range []types.Address{storeValue, forwarder, emitter, slotReader} {
		put(a, uint256.NewInt(0), 1, codes[a])
	}

	signer := transaction.LatestSignerForChainID(cfg.ChainID)
	nonces := map[types.Address]uint64{}
	var txs []*transaction.Transaction
	add := func(key *ecdsa.PrivateKey, from types.Address, to *types.Address, value uint64, gas uint64, data []byte) {
		n := nonces[from]
		nonces[from] = n + 1
		tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
			ChainID:   uint256.MustFromBig(cfg.ChainID),
			Nonce:     n,
			GasTipCap: uint256.NewInt(2),
			GasFeeCap: uint256.NewInt(100),
			Gas:       gas,
			To:        to,
			Value:     uint256.NewInt(value),
			Data:      data,
		}), signer, key)
		require.NoError(t, err)
		txs = append(txs, tx)
	}
	ptr := func(a types.Address) *types.Address { return &a }
	nonzero := make([]byte, 100)
	for i := range nonzero {
		nonzero[i] = 0xab
	}
	// The init code returns the two bytes 0x6000 as the runtime code.
	initCode := []byte{0x61, 0x60, 0x00, 0x60, 0x00, 0x52, 0x60, 0x02, 0x60, 0x1e, 0xf3}

	add(keyA, senderA, ptr(existing), 1e18, 21_000, nil)      // 0: value transfer to an existing account
	add(keyA, senderA, ptr(freshByTransfer), 1, 210_000, nil) // 1: transfer creating its recipient
	add(keyA, senderA, ptr(existing), 0, 15_000, nil)         // 2: zero-value call (EIP-2780)
	add(keyA, senderA, ptr(senderA), 5, 12_000, nil)          // 3: self-transfer (EIP-2780)
	add(keyA, senderA, ptr(storeValue), 5, 200_000, nil)      // 4: SSTORE 0 -> 5 (EIP-8037/8038)
	add(keyA, senderA, ptr(forwarder), 9, 300_000, nil)       // 5: inner CALL with value creating an account
	add(keyA, senderA, ptr(emitter), 0, 100_000, nil)         // 6: a contract log (ERC-20-like)
	add(keyA, senderA, nil, 7, 300_000, initCode)             // 7: contract creation with endowment
	add(keyB, senderB, ptr(slotReader), 0, 200_000, nil)      // 8: SLOTNUM stored (EIP-7843)
	add(keyB, senderB, ptr(freshUnpaid), 3, 21_000, nil)      // 9: creating transfer without the state gas: halts
	add(keyB, senderB, ptr(existing), 0, 30_000, nonzero)     // 10: calldata floor (EIP-7623/7976)

	slot := number
	header := &block.Header{
		Number:     uint256.NewInt(number),
		Time:       timestamp,
		GasLimit:   gasLimit,
		BaseFee:    uint256.NewInt(baseFee),
		Difficulty: uint256.NewInt(0),
		Coinbase:   coinbase,
		SlotNumber: &slot,
	}

	ibs := state.New(reader)
	ibs.SetHeight(number)
	gp := new(common.GasPool).AddGas(gasLimit)
	engine := coreTFakerEngine{}
	hashFn := func(uint64) types.Hash { return types.Hash{} }
	var cumulative uint64
	var receipts []*block.Receipt
	for i, tx := range txs {
		ibs.Prepare(tx.Hash(), types.Hash{}, i)
		receipt, _, err := ApplyTransaction(cfg, hashFn, engine, &coinbase, gp, ibs, state.NewNoopWriter(), header, tx, &cumulative, vm2.Config{})
		require.NoError(t, err, "tx %d", i)
		receipts = append(receipts, receipt)
	}
	require.NoError(t, ibs.Error())

	fx := glamFixture{
		Comment:       "gov5 serial execution of one block on the n42-rs gated devnet genesis (chain 1143, glamsterdamTime 0); written by TestGlamsterdamNativeExecutionFixture",
		Genesis:       "n42_devnet_mobileanchor.json",
		Number:        number,
		Timestamp:     timestamp,
		GasLimit:      gasLimit,
		BaseFee:       baseFee,
		Coinbase:      coinbase.Hex(),
		SlotNumber:    slot,
		Pre:           pre,
		GasUsed:       HeaderGasUsed(cfg, header, gp, cumulative),
		ReceiptsGas:   cumulative,
		ReceiptsRoot:  ReceiptsRootFor(cfg, number, receipts).Hex(),
		PostWatchSlot: types.Hash{}.Hex(),
	}
	for _, tx := range txs {
		enc, err := tx.EthEncoded()
		require.NoError(t, err)
		fx.Transactions = append(fx.Transactions, glamHex(enc))
	}
	for _, r := range receipts {
		fr := glamFixtureReceipt{Type: r.Type, Status: r.Status, GasUsed: r.GasUsed, CumulativeGasUsed: r.CumulativeGasUsed, Logs: []glamFixtureLog{}}
		for _, l := range r.Logs {
			fl := glamFixtureLog{Address: l.Address.Hex(), Data: glamHex(l.Data), Topics: []string{}}
			for _, topic := range l.Topics {
				fl.Topics = append(fl.Topics, topic.Hex())
			}
			fr.Logs = append(fr.Logs, fl)
		}
		fx.Receipts = append(fx.Receipts, fr)
	}
	created := crypto.CreateAddress(senderA, 7)
	var zero types.Hash
	for _, a := range []types.Address{senderA, senderB, existing, coinbase, freshByTransfer, freshByCall, freshUnpaid,
		storeValue, forwarder, emitter, slotReader, created} {
		var v uint256.Int
		ibs.GetState(a, &zero, &v)
		post := glamFixtureAccount{
			Address: a.Hex(),
			Balance: ibs.GetBalance(a).Hex(),
			Nonce:   ibs.GetNonce(a),
			Code:    glamHex(ibs.GetCode(a)),
		}
		if !v.IsZero() {
			post.Storage = map[string]string{zero.Hex(): v.Hex()}
		}
		if !ibs.Exist(a) {
			post.Balance, post.Code = "absent", ""
		}
		fx.Post = append(fx.Post, post)
	}

	out, err := json.MarshalIndent(fx, "", "  ")
	require.NoError(t, err)
	out = append(out, '\n')
	path := filepath.Join(glamFixtureDir, "block1.json")
	if os.Getenv("N42_WRITE_FIXTURES") == "1" {
		require.NoError(t, os.WriteFile(path, out, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "run with N42_WRITE_FIXTURES=1 to write %s", path)
	require.Equal(t, string(want), string(out), fmt.Sprintf("gov5 no longer produces %s", path))
}
