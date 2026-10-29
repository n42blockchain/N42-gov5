// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package streamverify

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// The leader runs the start-of-block system calls (EIP-4788 beacon root,
// EIP-2935 parent hash) before the first transaction, and their account and
// storage reads open the read log. The verifier consumes the log by position,
// so it has to run the same calls first; without them the first sender read
// picked up the beacon-roots contract (nonce 1) and every block with a
// transaction failed with "nonce too high".
func TestProducerVerifier_BlockStartSystemCallsRoundtrip(t *testing.T) {
	cfg := params.ChainConfigByChainName("mainnet_qmdb_staggered")
	if cfg == nil {
		t.Fatal("mainnet_qmdb_staggered chain config missing")
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := crypto.PubkeyToAddress(key.PublicKey)
	to := types.HexToAddress("0x00000000000000000000000000000000000c0ffe")

	senderAcc := account.NewAccount()
	senderAcc.Initialised = true
	senderAcc.Nonce = 577
	senderAcc.Balance.SetUint64(1e18)
	sysCode := []byte{0x60, 0x00, 0x60, 0x00, 0xf3}
	sysAcc := account.NewAccount()
	sysAcc.Initialised = true
	sysAcc.Nonce = 1
	sysAcc.CodeHash = crypto.Keccak256Hash(sysCode)
	beaconAcc, historyAcc := sysAcc, sysAcc
	reader := &stubReader{
		accounts: map[types.Address]*account.StateAccount{
			sender:                    &senderAcc,
			params.BeaconRootsAddress: &beaconAcc,
			vm.HistoryStorageAddress:  &historyAcc,
		},
		codes: map[types.Hash][]byte{sysAcc.CodeHash: sysCode},
	}

	signed, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.MustFromBig(cfg.ChainID),
		Nonce:     577,
		GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(10_000_000_000),
		Gas:       21_000,
		To:        &to,
		Value:     uint256.NewInt(1),
	}), transaction.NewLondonSigner(cfg.ChainID), key)
	if err != nil {
		t.Fatal(err)
	}

	beaconRoot := types.Hash{0xbe}
	hdr := validHeader(t, 14_300_000, types.Hash{})
	hdr.Time = 1_790_000_000 // Osaka (and so Cancun/Prague) active
	hdr.ParentBeaconRoot = &beaconRoot

	// Producer: block-start system calls, then the transaction, all through
	// the recorder, as the miner does.
	rec := NewReadLogRecorder(reader, nil)
	ibs := state.New(rec)
	engine := apos.NewFaker()
	if err := internal.ProcessExecutionBlockStart(hdr.ParentBeaconRoot, cfg, ibs, hdr, engine); err != nil {
		t.Fatalf("block start: %v", err)
	}
	gp := new(common.GasPool).AddGas(hdr.GasLimit)
	var used uint64
	ibs.Prepare(signed.Hash(), types.Hash{}, 0)
	receipt, _, err := internal.ApplyTransaction(cfg, func(uint64) types.Hash { return types.Hash{} }, engine, nil, gp,
		ibs, state.NewNoopWriter(), hdr, signed, &used, vm.Config{})
	if err != nil {
		t.Fatalf("producer apply: %v", err)
	}
	hdr.GasUsed = used
	hdr.ReceiptHash = evmsdk.EthReceiptHash([]*block.Receipt{receipt})

	pkt, err := BuildStreamPacket(hdr, transaction.Transactions{signed}, rec)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := evmsdk.EncodeStreamPacket(pkt)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := evmsdk.DecodeStreamPacket(enc)
	if err != nil {
		t.Fatal(err)
	}
	res, err := evmsdk.ExecuteAndVerifyV2(decoded, evmsdk.NewCodeCache(16), cfg)
	if err != nil {
		t.Fatalf("ExecuteAndVerifyV2: %v", err)
	}
	if res.TxCount != 1 || res.ComputedReceiptsRoot != hdr.ReceiptHash {
		t.Fatalf("txCount=%d root=%x, want 1 and %x", res.TxCount, res.ComputedReceiptsRoot, hdr.ReceiptHash)
	}
}
