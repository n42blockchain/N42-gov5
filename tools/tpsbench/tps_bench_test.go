// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// TPS Benchmark Tests - Fine-grained performance testing

package main

import (
	"crypto/ecdsa"
	"math/big"
	"runtime"
	"sync"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Benchmark: Account Generation
// =============================================================================

func BenchmarkAccountGeneration(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		key, _ := crypto.GenerateKey()
		_ = crypto.PubkeyToAddress(key.PublicKey)
	}
}

func BenchmarkAccountGenerationParallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			key, _ := crypto.GenerateKey()
			_ = crypto.PubkeyToAddress(key.PublicKey)
		}
	})
}

// =============================================================================
// Benchmark: Transaction Creation
// =============================================================================

func BenchmarkTransactionCreation(b *testing.B) {
	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	value := uint256.NewInt(1)
	gasPrice := uint256.NewInt(0)
	chainID := big.NewInt(42)
	signer := transaction.LatestSignerForChainID(chainID)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		tx := transaction.NewTransaction(0, from, &to, value, 21000, gasPrice, nil)
		_, _ = transaction.SignTx(tx, signer, key)
	}
}

func BenchmarkTransactionCreationParallel(b *testing.B) {
	chainID := big.NewInt(42)
	signer := transaction.LatestSignerForChainID(chainID)

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		key, _ := crypto.GenerateKey()
		from := crypto.PubkeyToAddress(key.PublicKey)
		to := types.HexToAddress("0x1234567890123456789012345678901234567890")
		value := uint256.NewInt(1)
		gasPrice := uint256.NewInt(0)

		for pb.Next() {
			tx := transaction.NewTransaction(0, from, &to, value, 21000, gasPrice, nil)
			_, _ = transaction.SignTx(tx, signer, key)
		}
	})
}

// =============================================================================
// Benchmark: Signature Verification
// =============================================================================

func BenchmarkSignatureVerification(b *testing.B) {
	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	chainID := big.NewInt(42)
	signer := transaction.LatestSignerForChainID(chainID)

	tx := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(0), nil)
	signedTx, _ := transaction.SignTx(tx, signer, key)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = transaction.Sender(signer, signedTx)
	}
}

func BenchmarkSignatureVerificationParallel(b *testing.B) {
	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	chainID := big.NewInt(42)
	signer := transaction.LatestSignerForChainID(chainID)

	tx := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(0), nil)
	signedTx, _ := transaction.SignTx(tx, signer, key)

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = transaction.Sender(signer, signedTx)
		}
	})
}

// =============================================================================
// Benchmark: State Operations
// =============================================================================

func BenchmarkStateGetBalance(b *testing.B) {
	db := NewMockStateDB()
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = db.GetBalance(addr)
	}
}

func BenchmarkStateGetBalanceParallel(b *testing.B) {
	db := NewMockStateDB()
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")

	b.ReportAllocs()
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = db.GetBalance(addr)
		}
	})
}

func BenchmarkStateAddBalance(b *testing.B) {
	db := NewMockStateDB()
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	amount := uint256.NewInt(1)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		db.AddBalance(addr, amount)
	}
}

func BenchmarkStateSubBalance(b *testing.B) {
	db := NewMockStateDB()
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")
	amount := uint256.NewInt(1)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		db.SubBalance(addr, amount)
	}
}

func BenchmarkStateSetNonce(b *testing.B) {
	db := NewMockStateDB()
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		db.SetNonce(addr, uint64(i))
	}
}

// =============================================================================
// Benchmark: Simple Transfer (No EVM)
// =============================================================================

func BenchmarkSimpleTransfer(b *testing.B) {
	chainID := big.NewInt(42)
	signer := transaction.LatestSignerForChainID(chainID)
	db := NewMockStateDB()

	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	value := uint256.NewInt(1)

	tx := transaction.NewTransaction(0, from, &to, value, 21000, uint256.NewInt(0), nil)
	signedTx, _ := transaction.SignTx(tx, signer, key)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		sender, _ := transaction.Sender(signer, signedTx)
		db.SubBalance(sender, value)
		db.AddBalance(to, value)
		db.SetNonce(sender, db.GetNonce(sender)+1)
	}
}

func BenchmarkSimpleTransferParallel(b *testing.B) {
	chainID := big.NewInt(42)
	db := NewMockStateDB()

	// Pre-generate many transactions
	numTxs := 10000
	txs := make([]*transaction.Transaction, numTxs)
	keys := make([]*ecdsa.PrivateKey, numTxs)
	signer := transaction.LatestSignerForChainID(chainID)

	for i := 0; i < numTxs; i++ {
		key, _ := crypto.GenerateKey()
		from := crypto.PubkeyToAddress(key.PublicKey)
		to := types.HexToAddress("0x1234567890123456789012345678901234567890")
		value := uint256.NewInt(1)

		tx := transaction.NewTransaction(0, from, &to, value, 21000, uint256.NewInt(0), nil)
		signedTx, _ := transaction.SignTx(tx, signer, key)
		txs[i] = signedTx
		keys[i] = key
	}

	b.ReportAllocs()
	b.ResetTimer()

	var idx uint64
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := int(atomic_add(&idx, 1) % uint64(numTxs))
			tx := txs[i]
			sender, _ := transaction.Sender(signer, tx)
			value := tx.Value()
			db.SubBalance(sender, value)
			if tx.To() != nil {
				db.AddBalance(*tx.To(), value)
			}
			db.SetNonce(sender, db.GetNonce(sender)+1)
		}
	})
}

var counterMu sync.Mutex
var counter uint64

func atomic_add(ptr *uint64, delta uint64) uint64 {
	counterMu.Lock()
	defer counterMu.Unlock()
	*ptr += delta
	return *ptr
}

// =============================================================================
// Benchmark: EVM Transfer
// =============================================================================

func BenchmarkEVMTransfer(b *testing.B) {
	chainID := big.NewInt(42)
	signer := transaction.LatestSignerForChainID(chainID)
	db := NewMockStateDB()

	chainConfig := &params.ChainConfig{
		ChainID:               chainID,
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
	}

	vmConfig := vm.Config{
		NoBaseFee:    true,
		NoReceipts:   true,
		SkipAnalysis: true,
	}

	blockCtx := evmtypes.BlockContext{
		CanTransfer: func(db evmtypes.IntraBlockState, addr types.Address, amount *uint256.Int) bool {
			return true
		},
		Transfer: func(db evmtypes.IntraBlockState, sender, recipient types.Address, amount *uint256.Int, bailout bool) {
			db.SubBalance(sender, amount)
			db.AddBalance(recipient, amount)
		},
		GetHash: func(n uint64) types.Hash {
			return types.Hash{}
		},
		Coinbase:    types.Address{},
		GasLimit:    ^uint64(0),
		BlockNumber: 1,
		Time:        1000000,
		Difficulty:  big.NewInt(1),
		BaseFee:     uint256.NewInt(0),
	}

	evm := vm.NewEVM(blockCtx, evmtypes.TxContext{}, db, chainConfig, vmConfig)

	key, _ := crypto.GenerateKey()
	from := crypto.PubkeyToAddress(key.PublicKey)
	to := types.HexToAddress("0x1234567890123456789012345678901234567890")
	value := uint256.NewInt(1)

	tx := transaction.NewTransaction(0, from, &to, value, 21000, uint256.NewInt(0), nil)
	signedTx, _ := transaction.SignTx(tx, signer, key)

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		sender, _ := transaction.Sender(signer, signedTx)
		txCtx := evmtypes.TxContext{
			Origin:   sender,
			GasPrice: signedTx.GasPrice(),
		}
		evm.Reset(txCtx, db)
		evm.Call(vm.AccountRef(sender), to, nil, 21000, value, false)
	}
}

// =============================================================================
// Benchmark: Full Transaction Pipeline
// =============================================================================

func BenchmarkFullPipeline(b *testing.B) {
	chainID := big.NewInt(42)
	signer := transaction.LatestSignerForChainID(chainID)
	db := NewMockStateDB()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		// 1. Create account
		key, _ := crypto.GenerateKey()
		from := crypto.PubkeyToAddress(key.PublicKey)
		to := types.HexToAddress("0x1234567890123456789012345678901234567890")

		// 2. Create transaction
		tx := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(0), nil)

		// 3. Sign transaction
		signedTx, _ := transaction.SignTx(tx, signer, key)

		// 4. Verify signature
		sender, _ := transaction.Sender(signer, signedTx)

		// 5. Execute transfer
		value := signedTx.Value()
		db.SubBalance(sender, value)
		db.AddBalance(*signedTx.To(), value)
		db.SetNonce(sender, db.GetNonce(sender)+1)
	}
}

// =============================================================================
// Benchmark: Batch Processing
// =============================================================================

func BenchmarkBatchProcessing_1K(b *testing.B) {
	benchmarkBatchProcessing(b, 1000)
}

func BenchmarkBatchProcessing_10K(b *testing.B) {
	benchmarkBatchProcessing(b, 10000)
}

func BenchmarkBatchProcessing_100K(b *testing.B) {
	benchmarkBatchProcessing(b, 100000)
}

func benchmarkBatchProcessing(b *testing.B, batchSize int) {
	chainID := big.NewInt(42)
	signer := transaction.LatestSignerForChainID(chainID)

	// Pre-generate transactions
	txs := make([]*transaction.Transaction, batchSize)
	for i := 0; i < batchSize; i++ {
		key, _ := crypto.GenerateKey()
		from := crypto.PubkeyToAddress(key.PublicKey)
		to := types.HexToAddress("0x1234567890123456789012345678901234567890")
		tx := transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(0), nil)
		signedTx, _ := transaction.SignTx(tx, signer, key)
		txs[i] = signedTx
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		db := NewMockStateDB()

		// Parallel execution
		numWorkers := runtime.NumCPU()
		workerBatch := (batchSize + numWorkers - 1) / numWorkers
		var wg sync.WaitGroup

		for w := 0; w < numWorkers; w++ {
			wg.Add(1)
			go func(workerID int) {
				defer wg.Done()
				startIdx := workerID * workerBatch
				endIdx := startIdx + workerBatch
				if endIdx > batchSize {
					endIdx = batchSize
				}

				for j := startIdx; j < endIdx; j++ {
					tx := txs[j]
					sender, _ := transaction.Sender(signer, tx)
					value := tx.Value()
					db.SubBalance(sender, value)
					if tx.To() != nil {
						db.AddBalance(*tx.To(), value)
					}
					db.SetNonce(sender, db.GetNonce(sender)+1)
				}
			}(w)
		}
		wg.Wait()
	}
}

// =============================================================================
// Tests
// =============================================================================

func TestMockStateDB(t *testing.T) {
	db := NewMockStateDB()
	addr := types.HexToAddress("0x1234567890123456789012345678901234567890")

	// Test initial balance (should be max)
	bal := db.GetBalance(addr)
	if bal.IsZero() {
		t.Error("Initial balance should not be zero")
	}

	// Test nonce
	if db.GetNonce(addr) != 0 {
		t.Error("Initial nonce should be 0")
	}

	db.SetNonce(addr, 5)
	if db.GetNonce(addr) != 5 {
		t.Error("Nonce should be 5")
	}
}

func TestParallelExecutor(t *testing.T) {
	executor := NewParallelExecutor(4)
	if executor.numWorkers != 4 {
		t.Errorf("Expected 4 workers, got %d", executor.numWorkers)
	}
}

func TestTxGenerator(t *testing.T) {
	gen := NewTxGenerator(big.NewInt(42), 10)
	if len(gen.accounts) != 10 {
		t.Errorf("Expected 10 accounts, got %d", len(gen.accounts))
	}

	txs := gen.GenerateTransactions(10)
	if len(txs) != 10 {
		t.Errorf("Expected 10 transactions, got %d", len(txs))
	}
}

