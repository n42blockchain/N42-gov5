// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// TPS Benchmark Tool - Extreme Performance Testing
//
// This tool tests the maximum TPS (Transactions Per Second) for native token
// transfers on the N42 blockchain by:
// - Removing all gas/block size limits
// - Using parallel EVM execution across all CPU cores
// - Pre-generating millions of independent transactions
// - Measuring raw execution throughput
//
// Usage: go run tools/tpsbench/tps_bench.go -txcount=3000000 -workers=0

package main

import (
	"crypto/ecdsa"
	"flag"
	"fmt"
	"math/big"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Configuration
// =============================================================================

var (
	txCount   = flag.Int("txcount", 100000, "Number of transactions to generate and execute")
	workers   = flag.Int("workers", 0, "Number of worker goroutines (0 = auto-detect CPU cores)")
	batchSize = flag.Int("batch", 10000, "Batch size for processing")
)

// =============================================================================
// Mock State Database (In-Memory, Lock-Free for Read)
// =============================================================================

// MockStateDB is an optimized in-memory state database for benchmarking
type MockStateDB struct {
	accounts   sync.Map // map[types.Address]*AccountState
	defaultBal *uint256.Int
}

type AccountState struct {
	Balance *uint256.Int
	Nonce   uint64
	mu      sync.Mutex
}

func NewMockStateDB() *MockStateDB {
	defaultBal := uint256.NewInt(0)
	defaultBal.SetAllOne() // Max uint256
	return &MockStateDB{
		defaultBal: defaultBal,
	}
}

func (m *MockStateDB) getAccount(addr types.Address) *AccountState {
	if acc, ok := m.accounts.Load(addr); ok {
		return acc.(*AccountState)
	}
	acc := &AccountState{
		Balance: m.defaultBal.Clone(),
		Nonce:   0,
	}
	actual, _ := m.accounts.LoadOrStore(addr, acc)
	return actual.(*AccountState)
}

func (m *MockStateDB) GetBalance(addr types.Address) *uint256.Int {
	return m.getAccount(addr).Balance.Clone()
}

func (m *MockStateDB) GetNonce(addr types.Address) uint64 {
	return m.getAccount(addr).Nonce
}

func (m *MockStateDB) SetNonce(addr types.Address, nonce uint64) {
	acc := m.getAccount(addr)
	acc.mu.Lock()
	acc.Nonce = nonce
	acc.mu.Unlock()
}

func (m *MockStateDB) AddBalance(addr types.Address, amount *uint256.Int) {
	acc := m.getAccount(addr)
	acc.mu.Lock()
	acc.Balance.Add(acc.Balance, amount)
	acc.mu.Unlock()
}

func (m *MockStateDB) SubBalance(addr types.Address, amount *uint256.Int) {
	acc := m.getAccount(addr)
	acc.mu.Lock()
	acc.Balance.Sub(acc.Balance, amount)
	acc.mu.Unlock()
}

func (m *MockStateDB) GetCodeHash(addr types.Address) types.Hash {
	return types.Hash{}
}

func (m *MockStateDB) GetCode(addr types.Address) []byte {
	return nil
}

func (m *MockStateDB) GetCodeSize(addr types.Address) int {
	return 0
}

func (m *MockStateDB) SetCode(addr types.Address, code []byte) {
	// No-op for benchmark
}

func (m *MockStateDB) GetRefund() uint64 {
	return 0
}

func (m *MockStateDB) Exist(addr types.Address) bool {
	_, ok := m.accounts.Load(addr)
	return ok
}

func (m *MockStateDB) Empty(addr types.Address) bool {
	return !m.Exist(addr)
}

func (m *MockStateDB) GetState(addr types.Address, key *types.Hash, value *uint256.Int) {
	value.Clear()
}

func (m *MockStateDB) GetCommittedState(addr types.Address, key *types.Hash, value *uint256.Int) {
	value.Clear()
}

func (m *MockStateDB) SetState(addr types.Address, key *types.Hash, value uint256.Int) {
}

func (m *MockStateDB) CreateAccount(addr types.Address, contractCreation bool) {
	m.getAccount(addr)
}

func (m *MockStateDB) Selfdestruct(addr types.Address) bool {
	return false
}

func (m *MockStateDB) HasSelfdestructed(addr types.Address) bool {
	return false
}

func (m *MockStateDB) RevertToSnapshot(int) {}
func (m *MockStateDB) Snapshot() int        { return 0 }
func (m *MockStateDB) AddLog(*block.Log)    {}
func (m *MockStateDB) GetLogs(types.Hash) []*block.Log {
	return nil
}

func (m *MockStateDB) Prepare(thash types.Hash, bhash types.Hash, ti int) {}
func (m *MockStateDB) TxIndex() int                                       { return 0 }
func (m *MockStateDB) AddRefund(uint64)                                   {}
func (m *MockStateDB) SubRefund(uint64)                                   {}
func (m *MockStateDB) AddAddressToAccessList(addr types.Address)          {}
func (m *MockStateDB) AddSlotToAccessList(addr types.Address, slot types.Hash) {
}
func (m *MockStateDB) PrepareAccessList(sender types.Address, dest *types.Address, precompiles []types.Address, txAccesses transaction.AccessList) {
}
func (m *MockStateDB) AddressInAccessList(addr types.Address) bool {
	return true
}
func (m *MockStateDB) SlotInAccessList(addr types.Address, slot types.Hash) (addressOk bool, slotOk bool) {
	return true, true
}

func (m *MockStateDB) GetTransientState(addr types.Address, key types.Hash) uint256.Int {
	return uint256.Int{}
}

func (m *MockStateDB) SetTransientState(addr types.Address, key types.Hash, value uint256.Int) {
}

// =============================================================================
// Transaction Generator
// =============================================================================

type TxGenerator struct {
	chainID  *big.Int
	signer   transaction.Signer
	accounts []*ecdsa.PrivateKey
	addrs    []types.Address
}

func NewTxGenerator(chainID *big.Int, numAccounts int) *TxGenerator {
	fmt.Printf("Generating %d accounts...\n", numAccounts)
	start := time.Now()

	accounts := make([]*ecdsa.PrivateKey, numAccounts)
	addrs := make([]types.Address, numAccounts)

	numWorkers := runtime.NumCPU()
	workerBatch := (numAccounts + numWorkers - 1) / numWorkers
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			startIdx := workerID * workerBatch
			endIdx := startIdx + workerBatch
			if endIdx > numAccounts {
				endIdx = numAccounts
			}
			for i := startIdx; i < endIdx; i++ {
				key, _ := crypto.GenerateKey()
				accounts[i] = key
				addrs[i] = crypto.PubkeyToAddress(key.PublicKey)
			}
		}(w)
	}
	wg.Wait()

	fmt.Printf("Account generation took: %v\n", time.Since(start))

	return &TxGenerator{
		chainID:  chainID,
		signer:   transaction.LatestSignerForChainID(chainID),
		accounts: accounts,
		addrs:    addrs,
	}
}

// GenerateTransactions generates independent transactions (one tx per account)
func (g *TxGenerator) GenerateTransactions(count int) []*transaction.Transaction {
	fmt.Printf("Generating %d transactions...\n", count)
	start := time.Now()

	txs := make([]*transaction.Transaction, count)
	numWorkers := runtime.NumCPU()
	workerBatch := (count + numWorkers - 1) / numWorkers
	var wg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			startIdx := workerID * workerBatch
			endIdx := startIdx + workerBatch
			if endIdx > count {
				endIdx = count
			}

			value := uint256.NewInt(1)
			gasPrice := uint256.NewInt(0)
			gasLimit := uint64(21000)

			for i := startIdx; i < endIdx; i++ {
				fromIdx := i % len(g.accounts)
				toIdx := (i + 1) % len(g.accounts)
				toAddr := g.addrs[toIdx]

				tx := transaction.NewTransaction(
					0,                   // nonce
					g.addrs[fromIdx],    // from
					&toAddr,             // to
					value,               // value
					gasLimit,            // gas limit
					gasPrice,            // gas price
					nil,                 // no data for simple transfer
				)

				signedTx, _ := transaction.SignTx(tx, g.signer, g.accounts[fromIdx])
				txs[i] = signedTx
			}
		}(w)
	}
	wg.Wait()

	fmt.Printf("Transaction generation took: %v\n", time.Since(start))
	return txs
}

// =============================================================================
// Parallel Transaction Executor
// =============================================================================

type ParallelExecutor struct {
	chainConfig *params.ChainConfig
	vmConfig    vm.Config
	numWorkers  int
	stateDB     *MockStateDB
}

func NewParallelExecutor(numWorkers int) *ParallelExecutor {
	if numWorkers <= 0 {
		numWorkers = runtime.NumCPU()
	}

	chainConfig := &params.ChainConfig{
		ChainID:               big.NewInt(42),
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
		ReadOnly:     false,
		SkipAnalysis: true,
	}

	return &ParallelExecutor{
		chainConfig: chainConfig,
		vmConfig:    vmConfig,
		numWorkers:  numWorkers,
		stateDB:     NewMockStateDB(),
	}
}

// ExecuteTransactions executes all transactions in parallel (simple transfer)
func (e *ParallelExecutor) ExecuteTransactions(txs []*transaction.Transaction) ExecutionResult {
	fmt.Printf("\nStarting parallel execution with %d workers...\n", e.numWorkers)
	fmt.Printf("Total transactions: %d\n", len(txs))

	var (
		totalGas     uint64
		successCount int64
		failCount    int64
	)

	workerBatch := (len(txs) + e.numWorkers - 1) / e.numWorkers
	var wg sync.WaitGroup

	start := time.Now()

	for w := 0; w < e.numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			startIdx := workerID * workerBatch
			endIdx := startIdx + workerBatch
			if endIdx > len(txs) {
				endIdx = len(txs)
			}

			localGas := uint64(0)
			localSuccess := int64(0)
			localFail := int64(0)

			for i := startIdx; i < endIdx; i++ {
				tx := txs[i]
				gas, err := e.executeSimpleTransfer(tx)
				localGas += gas
				if err == nil {
					localSuccess++
				} else {
					localFail++
				}
			}

			atomic.AddUint64(&totalGas, localGas)
			atomic.AddInt64(&successCount, localSuccess)
			atomic.AddInt64(&failCount, localFail)
		}(w)
	}

	wg.Wait()
	duration := time.Since(start)

	return ExecutionResult{
		TotalTxs:     len(txs),
		SuccessTxs:   int(successCount),
		FailedTxs:    int(failCount),
		TotalGas:     totalGas,
		Duration:     duration,
		TPS:          float64(successCount) / duration.Seconds(),
		GasPerSecond: float64(totalGas) / duration.Seconds(),
	}
}

// executeSimpleTransfer performs a simple value transfer (no EVM)
func (e *ParallelExecutor) executeSimpleTransfer(tx *transaction.Transaction) (uint64, error) {
	const transferGas = uint64(21000)

	from, err := transaction.Sender(transaction.LatestSignerForChainID(e.chainConfig.ChainID), tx)
	if err != nil {
		return 0, err
	}

	value := tx.Value()
	e.stateDB.SubBalance(from, value)

	if tx.To() != nil {
		e.stateDB.AddBalance(*tx.To(), value)
	}

	e.stateDB.SetNonce(from, e.stateDB.GetNonce(from)+1)

	return transferGas, nil
}

// ExecuteWithEVM executes using full EVM
func (e *ParallelExecutor) ExecuteWithEVM(txs []*transaction.Transaction) ExecutionResult {
	fmt.Printf("\nStarting EVM execution with %d workers...\n", e.numWorkers)

	var (
		totalGas     uint64
		successCount int64
		failCount    int64
	)

	workerBatch := (len(txs) + e.numWorkers - 1) / e.numWorkers
	var wg sync.WaitGroup

	start := time.Now()

	for w := 0; w < e.numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			startIdx := workerID * workerBatch
			endIdx := startIdx + workerBatch
			if endIdx > len(txs) {
				endIdx = len(txs)
			}

			blockCtx := e.createBlockContext()
			evm := vm.NewEVM(blockCtx, evmtypes.TxContext{}, e.stateDB, e.chainConfig, e.vmConfig)

			localGas := uint64(0)
			localSuccess := int64(0)
			localFail := int64(0)

			for i := startIdx; i < endIdx; i++ {
				tx := txs[i]
				gas, err := e.executeWithEVMSingle(evm, tx)
				localGas += gas
				if err == nil {
					localSuccess++
				} else {
					localFail++
				}
			}

			atomic.AddUint64(&totalGas, localGas)
			atomic.AddInt64(&successCount, localSuccess)
			atomic.AddInt64(&failCount, localFail)
		}(w)
	}

	wg.Wait()
	duration := time.Since(start)

	return ExecutionResult{
		TotalTxs:     len(txs),
		SuccessTxs:   int(successCount),
		FailedTxs:    int(failCount),
		TotalGas:     totalGas,
		Duration:     duration,
		TPS:          float64(successCount) / duration.Seconds(),
		GasPerSecond: float64(totalGas) / duration.Seconds(),
	}
}

func (e *ParallelExecutor) createBlockContext() evmtypes.BlockContext {
	return evmtypes.BlockContext{
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
		Time:        uint64(time.Now().Unix()),
		Difficulty:  big.NewInt(1),
		BaseFee:     uint256.NewInt(0),
	}
}

func (e *ParallelExecutor) executeWithEVMSingle(evm *vm.EVM, tx *transaction.Transaction) (uint64, error) {
	const transferGas = uint64(21000)

	from, err := transaction.Sender(transaction.LatestSignerForChainID(e.chainConfig.ChainID), tx)
	if err != nil {
		return 0, err
	}

	txCtx := evmtypes.TxContext{
		Origin:   from,
		GasPrice: tx.GasPrice(),
	}
	evm.Reset(txCtx, e.stateDB)

	value := tx.Value()
	if tx.To() != nil {
		_, _, err = evm.Call(vm.AccountRef(from), *tx.To(), nil, transferGas, value, false)
	}

	return transferGas, err
}

// =============================================================================
// Result Types
// =============================================================================

type ExecutionResult struct {
	TotalTxs     int
	SuccessTxs   int
	FailedTxs    int
	TotalGas     uint64
	Duration     time.Duration
	TPS          float64
	GasPerSecond float64
}

func (r ExecutionResult) Print() {
	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("EXECUTION RESULTS")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Total Transactions:    %d\n", r.TotalTxs)
	fmt.Printf("Successful:            %d\n", r.SuccessTxs)
	fmt.Printf("Failed:                %d\n", r.FailedTxs)
	fmt.Printf("Total Gas Used:        %d\n", r.TotalGas)
	fmt.Printf("Execution Time:        %v\n", r.Duration)
	fmt.Println(strings.Repeat("-", 60))
	fmt.Printf("TPS (Tx/Second):       %.2f\n", r.TPS)
	fmt.Printf("Gas/Second:            %.2f\n", r.GasPerSecond)
	if r.SuccessTxs > 0 {
		fmt.Printf("Avg Tx Time:           %v\n", time.Duration(float64(r.Duration)/float64(r.SuccessTxs)))
	}
	fmt.Println(strings.Repeat("=", 60))
}

// =============================================================================
// Main
// =============================================================================

func main() {
	flag.Parse()

	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("N42 TPS BENCHMARK - EXTREME PERFORMANCE TEST")
	fmt.Println(strings.Repeat("=", 60))

	workerCount := *workers
	if workerCount <= 0 {
		workerCount = runtime.NumCPU()
	}

	fmt.Printf("\nSystem Configuration:\n")
	fmt.Printf("  CPU Cores:      %d\n", runtime.NumCPU())
	fmt.Printf("  GOMAXPROCS:     %d\n", runtime.GOMAXPROCS(0))
	fmt.Printf("  Workers:        %d\n", workerCount)
	fmt.Printf("  Transaction #:  %d\n", *txCount)
	fmt.Printf("  Batch Size:     %d\n", *batchSize)

	runtime.GOMAXPROCS(runtime.NumCPU())

	numAccounts := *txCount
	if numAccounts > 1000000 {
		numAccounts = 1000000
	}

	generator := NewTxGenerator(big.NewInt(42), numAccounts)
	txs := generator.GenerateTransactions(*txCount)

	executor := NewParallelExecutor(*workers)

	fmt.Println("\n--- Simple Transfer Mode (No EVM) ---")
	result1 := executor.ExecuteTransactions(txs)
	result1.Print()

	fmt.Println("\n--- EVM Transfer Mode ---")
	result2 := executor.ExecuteWithEVM(txs)
	result2.Print()

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("PERFORMANCE COMPARISON")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Printf("Simple Transfer TPS: %.2f\n", result1.TPS)
	fmt.Printf("EVM Transfer TPS:    %.2f\n", result2.TPS)
	if result2.TPS > 0 {
		fmt.Printf("EVM Overhead:        %.2fx slower\n", result1.TPS/result2.TPS)
	}
	fmt.Println(strings.Repeat("=", 60))

	writeResultsToFile(result1, result2)
}

func writeResultsToFile(simple, evm ExecutionResult) {
	f, err := os.Create("tps_benchmark_results.txt")
	if err != nil {
		fmt.Printf("Error creating results file: %v\n", err)
		return
	}
	defer f.Close()

	fmt.Fprintf(f, "N42 TPS Benchmark Results\n")
	fmt.Fprintf(f, "========================\n\n")
	fmt.Fprintf(f, "System: %d CPU cores\n", runtime.NumCPU())
	fmt.Fprintf(f, "Date: %s\n\n", time.Now().Format(time.RFC3339))

	fmt.Fprintf(f, "Simple Transfer Mode:\n")
	fmt.Fprintf(f, "  TPS: %.2f\n", simple.TPS)
	fmt.Fprintf(f, "  Duration: %v\n", simple.Duration)
	fmt.Fprintf(f, "  Transactions: %d\n\n", simple.TotalTxs)

	fmt.Fprintf(f, "EVM Transfer Mode:\n")
	fmt.Fprintf(f, "  TPS: %.2f\n", evm.TPS)
	fmt.Fprintf(f, "  Duration: %v\n", evm.Duration)
	fmt.Fprintf(f, "  Transactions: %d\n", evm.TotalTxs)

	fmt.Println("\nResults written to tps_benchmark_results.txt")
}
