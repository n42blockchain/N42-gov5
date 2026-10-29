// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package eldevp2p

import (
	"errors"
	"math/big"
	"testing"

	gethcommon "github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/devp2p"
	"github.com/n42blockchain/N42/internal/ethel"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/params"
)

// sizeTestBlock builds an Osaka-era block with legacy, dynamic-fee and blob
// transactions and two withdrawals, in N42's decoded form, as a devp2p body,
// and as a geth block.
func sizeTestBlock(t *testing.T, extra int) (*block.Header, []*transaction.Transaction, []*ethel.Withdrawal, devp2p.BlockBody, *gethtypes.Block) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	signer := transaction.LatestSignerForChainID(big.NewInt(1))
	to := types.HexToAddress("0x2222222222222222222222222222222222222222")
	var txs []*transaction.Transaction
	for _, inner := range []transaction.TxData{
		&transaction.LegacyTx{Nonce: 0, GasPrice: uint256.NewInt(9), Gas: 21_000, To: &to, Value: uint256.NewInt(1), Data: make([]byte, 300)},
		&transaction.DynamicFeeTx{ChainID: uint256.NewInt(1), Nonce: 1, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(9), Gas: 50_000, To: &to, Value: uint256.NewInt(2), Data: make([]byte, 70)},
		&transaction.BlobTx{ChainID: uint256.NewInt(1), Nonce: 2, GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(9), Gas: 21_000, To: to, Value: uint256.NewInt(0), BlobFeeCap: uint256.NewInt(5), BlobHashes: []types.Hash{{0x01, 0xaa}}},
	} {
		tx, err := transaction.SignTx(transaction.NewTx(inner), signer, key)
		if err != nil {
			t.Fatalf("SignTx: %v", err)
		}
		txs = append(txs, tx)
	}
	withdrawals := []*ethel.Withdrawal{
		{Index: 0, Validator: 5, Address: types.Address{0xab}, Amount: 0},
		{Index: 1_000_000, Validator: 1_234_567, Address: types.Address{0xcd}, Amount: 32_000_000_000},
	}

	h := types.Hash{1}
	blobGas := uint64(131072)
	header := &block.Header{
		ParentHash:       h,
		UncleHash:        hash.EmptyUncleHash,
		Number:           uint256.NewInt(23_935_694),
		GasLimit:         60_000_000,
		GasUsed:          92_000,
		Difficulty:       uint256.NewInt(0),
		Time:             params.EthereumMainnetChainConfig.OsakaTime.Uint64(),
		BaseFee:          uint256.NewInt(7),
		Extra:            make([]byte, extra),
		BlobGasUsed:      &blobGas,
		ExcessBlobGas:    &blobGas,
		ParentBeaconRoot: &h,
		RequestsHash:     &h,
	}

	var body devp2p.BlockBody
	gethTxs := make([]*gethtypes.Transaction, len(txs))
	for i, tx := range txs {
		enc, err := tx.EthEncoded()
		if err != nil {
			t.Fatalf("encode tx: %v", err)
		}
		gethTxs[i] = new(gethtypes.Transaction)
		if err := gethTxs[i].UnmarshalBinary(enc); err != nil {
			t.Fatalf("geth decode tx %d: %v", i, err)
		}
		raw := enc
		if enc[0] < 0xc0 {
			if raw, err = rlp.EncodeToBytes(enc); err != nil {
				t.Fatalf("wrap tx: %v", err)
			}
		}
		body.Transactions = append(body.Transactions, raw)
	}
	gethWithdrawals := make([]*gethtypes.Withdrawal, len(withdrawals))
	withdrawalItems := make([][]byte, len(withdrawals))
	for i, w := range withdrawals {
		gethWithdrawals[i] = &gethtypes.Withdrawal{Index: w.Index, Validator: w.Validator, Address: gethcommon.Address(w.Address), Amount: w.Amount}
		raw, err := rlp.EncodeToBytes(gethWithdrawals[i])
		if err != nil {
			t.Fatalf("encode withdrawal: %v", err)
		}
		body.Withdrawals = append(body.Withdrawals, raw)
		withdrawalItems[i] = raw
	}
	wroot := hash.DeriveShaErigon(rawTxList(withdrawalItems))
	header.WithdrawalsHash = &wroot

	headerRLP, err := rlp.EncodeToBytes(header)
	if err != nil {
		t.Fatalf("encode header: %v", err)
	}
	gethHeader := new(gethtypes.Header)
	if err := rlp.DecodeBytes(headerRLP, gethHeader); err != nil {
		t.Fatalf("geth decode header: %v", err)
	}
	if types.Hash(gethHeader.Hash()) != header.Hash() {
		t.Fatalf("header hash differs from geth")
	}
	gethBlock := gethtypes.NewBlockWithHeader(gethHeader).WithBody(gethtypes.Body{Transactions: gethTxs, Withdrawals: gethWithdrawals})
	return header, txs, withdrawals, body, gethBlock
}

// The EIP-7934 size must be the one geth's Block.Size measures, both from a
// devp2p body and from a decoded block (batch replay).
func TestBlockRLPSizeMatchesGeth(t *testing.T) {
	header, txs, withdrawals, body, gethBlock := sizeTestBlock(t, 32)
	want := gethBlock.Size()

	got, err := wireBlockRLPSize(header, body)
	if err != nil {
		t.Fatalf("wireBlockRLPSize: %v", err)
	}
	if got != want {
		t.Fatalf("wire size %d, geth %d", got, want)
	}
	if got, err = ethel.BlockRLPSize(header, txs, nil, withdrawals); err != nil {
		t.Fatalf("BlockRLPSize: %v", err)
	} else if got != want {
		t.Fatalf("decoded size %d, geth %d", got, want)
	}
}

// From Osaka an oversized devp2p block is invalid, unless its body does not
// match the header (a padding peer), which is refetched instead.
func TestCheckWireBlockSize(t *testing.T) {
	cfg := params.EthereumMainnetChainConfig
	// Grow the extra data so the block lands exactly on and one past the cap.
	sized := func(target uint64) (*block.Header, devp2p.BlockBody) {
		extra := 32
		for i := 0; i < 8; i++ {
			header, _, _, body, _ := sizeTestBlock(t, extra)
			size, err := wireBlockRLPSize(header, body)
			if err != nil {
				t.Fatalf("wireBlockRLPSize: %v", err)
			}
			if size == target {
				return header, body
			}
			extra += int(target) - int(size)
		}
		t.Fatalf("could not size a block to %d bytes", target)
		return nil, devp2p.BlockBody{}
	}

	header, body := sized(vm2.MaxRLPBlockSizeFusaka)
	if refetch, err := checkWireBlockSize(cfg, header, body); err != nil || refetch {
		t.Fatalf("at cap: refetch %v err %v, want accepted", refetch, err)
	}

	header, body = sized(vm2.MaxRLPBlockSizeFusaka + 1)
	if refetch, err := checkWireBlockSize(cfg, header, body); !errors.Is(err, vm2.ErrRLPBlockSizeTooLarge) || refetch {
		t.Fatalf("over cap: refetch %v err %v, want invalid", refetch, err)
	}

	header.Time--
	if refetch, err := checkWireBlockSize(cfg, header, body); err != nil || refetch {
		t.Fatalf("over cap before Osaka: refetch %v err %v, want accepted", refetch, err)
	}
	header.Time++

	padded := body
	padded.Uncles = []rlp.RawValue{make([]byte, 1)}
	if refetch, err := checkWireBlockSize(cfg, header, padded); err == nil || !refetch {
		t.Fatalf("padded body: refetch %v err %v, want refetch", refetch, err)
	}
}
