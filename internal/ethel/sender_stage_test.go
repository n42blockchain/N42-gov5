// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

// ethTTestKey returns a fixed deterministic private key for signing
// throwaway test transactions (not a real fund-holding key anywhere).
func ethTTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := crypto.HexToECDSA("0000000000000000000000000000000000000000000000000000000000000001")
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// ethTFakeHeadersBodiesSource is a minimal in-memory HeadersBodiesSource for
// driving SenderStage without any real freezer/columnar data.
type ethTFakeHeadersBodiesSource struct {
	headers  []*block.Header
	bodies   []*GethBodyResult
	maxBlock uint64
	// failAt, if set, makes Header/Body return an error for this block.
	failAt uint64
	hasFail bool
}

func (s *ethTFakeHeadersBodiesSource) Header(blockNum uint64) (*block.Header, error) {
	if s.hasFail && blockNum == s.failAt {
		return nil, errors.New("synthetic header read failure")
	}
	if blockNum >= uint64(len(s.headers)) {
		return nil, errors.New("out of range")
	}
	return s.headers[blockNum], nil
}

func (s *ethTFakeHeadersBodiesSource) Body(blockNum uint64) (*GethBodyResult, error) {
	if s.hasFail && blockNum == s.failAt {
		return nil, errors.New("synthetic body read failure")
	}
	if blockNum >= uint64(len(s.bodies)) {
		return nil, errors.New("out of range")
	}
	return s.bodies[blockNum], nil
}

func (s *ethTFakeHeadersBodiesSource) MaxBlock() uint64 { return s.maxBlock }
func (s *ethTFakeHeadersBodiesSource) Close()           {}

// ethTSignedTx builds a legacy transaction signed by a throwaway key, usable
// with transaction.Sender for a realistic ecrecover path in SenderStage.
func ethTSignedTx(t *testing.T, chainCfg *params.ChainConfig, nonce uint64) *transaction.Transaction {
	t.Helper()
	key := ethTTestKey(t)
	tx := transaction.NewTransaction(nonce, types.HexToAddress("0xdead"), nil, uint256.NewInt(0), 21000, uint256.NewInt(1), nil)
	signer := transaction.MakeSigner(chainCfg, uint256.NewInt(1).ToBig())
	signed, err := transaction.SignTx(tx, signer, key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func ethTFakeSource(t *testing.T, chainCfg *params.ChainConfig, n int) *ethTFakeHeadersBodiesSource {
	t.Helper()
	src := &ethTFakeHeadersBodiesSource{maxBlock: uint64(n)}
	for i := 0; i < n; i++ {
		h := &block.Header{Number: uint256.NewInt(uint64(i))}
		src.headers = append(src.headers, h)
		var txs []*transaction.Transaction
		if i%3 == 0 {
			txs = append(txs, ethTSignedTx(t, chainCfg, uint64(i)))
		}
		src.bodies = append(src.bodies, &GethBodyResult{Transactions: txs})
	}
	return src
}

func TestSenderStageRunWritesAllBlocks(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	const n = 10
	src := ethTFakeSource(t, chainCfg, n)

	fz := ethTMkFreezer(t)
	stage := NewSenderStage(src, fz, chainCfg, 2)
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	tbl, err := fz.EnsureTableCompressed("senders", "c")
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != n {
		t.Fatalf("Items() = %d, want %d", got, n)
	}
}

func TestSenderStageRunIsResumable(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	const n = 5
	src := ethTFakeSource(t, chainCfg, n)

	fz := ethTMkFreezer(t)
	stage := NewSenderStage(src, fz, chainCfg, 1)
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Re-running against the same (now up-to-date) output is a no-op.
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	tbl, err := fz.EnsureTableCompressed("senders", "c")
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != n {
		t.Fatalf("Items() = %d, want %d after resume no-op", got, n)
	}
}

func TestSenderStageSetRangeRejectsGap(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	src := ethTFakeSource(t, chainCfg, 5)
	fz := ethTMkFreezer(t)
	stage := NewSenderStage(src, fz, chainCfg, 1)
	stage.SetRange(3, 0) // tbl.Items() is 0, so start=3 would create a gap
	if err := stage.Run(context.Background()); err == nil {
		t.Fatal("expected gap error for forceStart != resumeAt")
	}
}

func TestSenderStageSetRangeEndBeyondMaxBlockErrors(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	src := ethTFakeSource(t, chainCfg, 5)
	fz := ethTMkFreezer(t)
	stage := NewSenderStage(src, fz, chainCfg, 1)
	stage.SetRange(0, 100)
	if err := stage.Run(context.Background()); err == nil {
		t.Fatal("expected error for forceEnd exceeding input maxBlock")
	}
}

func TestSenderStageSetRangeLimitsEnd(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	src := ethTFakeSource(t, chainCfg, 10)
	fz := ethTMkFreezer(t)
	stage := NewSenderStage(src, fz, chainCfg, 2)
	stage.SetRange(0, 4)
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	tbl, err := fz.EnsureTableCompressed("senders", "c")
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != 4 {
		t.Fatalf("Items() = %d, want 4", got)
	}
}

func TestSenderStageReaderHeaderErrorPropagates(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	src := ethTFakeSource(t, chainCfg, 10)
	src.hasFail = true
	src.failAt = 3 // not the last block, so this is a hard error
	fz := ethTMkFreezer(t)
	stage := NewSenderStage(src, fz, chainCfg, 1)
	if err := stage.Run(context.Background()); err == nil {
		t.Fatal("expected error propagated from mid-range header read failure")
	}
}

func TestSenderStageReaderEndOfInputErrorIsTolerated(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	const n = 10
	src := ethTFakeSource(t, chainCfg, n)
	src.hasFail = true
	src.failAt = n - 1 // last block — tolerated as a clean stop
	fz := ethTMkFreezer(t)
	stage := NewSenderStage(src, fz, chainCfg, 1)
	if err := stage.Run(context.Background()); err != nil {
		t.Fatalf("expected end-of-input read error to be tolerated, got %v", err)
	}
	tbl, err := fz.EnsureTableCompressed("senders", "c")
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != n-1 {
		t.Fatalf("Items() = %d, want %d", got, n-1)
	}
}

func TestSenderStageAlreadyUpToDateIsNoop(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	src := ethTFakeSource(t, chainCfg, 3)
	fz := ethTMkFreezer(t)
	stage := NewSenderStage(src, fz, chainCfg, 1)
	stage.SetRange(0, 3)
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	// forceEnd == forceStart's resulting resume point: already up to date.
	stage2 := NewSenderStage(src, fz, chainCfg, 1)
	stage2.SetRange(0, 3)
	if err := stage2.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSenderStageDefaultWorkerCount(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	src := ethTFakeSource(t, chainCfg, 1)
	fz := ethTMkFreezer(t)
	stage := NewSenderStage(src, fz, chainCfg, 0) // 0 => runtime.NumCPU()
	if stage.workers <= 0 {
		t.Fatalf("workers = %d, want > 0", stage.workers)
	}
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSenderStageLargeRunExercisesBatchFlushAndFsync(t *testing.T) {
	chainCfg := params.EthereumMainnetChainConfig
	const n = freezer.BatchSize*2 + 7
	src := ethTFakeSource(t, chainCfg, n)
	fz := ethTMkFreezer(t)
	stage := NewSenderStage(src, fz, chainCfg, 4)
	if err := stage.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	tbl, err := fz.EnsureTableCompressed("senders", "c")
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.Items(); got != n {
		t.Fatalf("Items() = %d, want %d", got, n)
	}
}
