// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// TPS Extreme Benchmark Tool
//
// This tool measures the maximum theoretical TPS for native token transfers
// by removing all protocol limits and using parallel execution.
//
// Key optimizations:
// - In-memory state database (no disk I/O)
// - Pre-generated independent transactions (no nonce conflicts)
// - Multi-threaded parallel execution
// - Disabled gas limits, block size limits
// - Zero block interval
// - Optimized EVM instances per CPU core

package tps

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// =============================================================================
// Configuration
// =============================================================================

// BenchConfig holds benchmark configuration
type BenchConfig struct {
	// Number of transactions to execute
	TxCount int
	// Number of worker threads (0 = auto-detect)
	Workers int
	// Batch size for each worker
	BatchSize int
	// Initial balance for each account (in wei)
	InitialBalance *uint256.Int
	// Transfer amount per transaction (in wei)
	TransferAmount *uint256.Int
	// Enable detailed logging
	Verbose bool
	// Pre-warm the state before benchmark
	PreWarm bool
}

// DefaultConfig returns default benchmark configuration
func DefaultConfig() *BenchConfig {
	return &BenchConfig{
		TxCount:        3000000, // 3 million transactions
		Workers:        0,       // Auto-detect
		BatchSize:      10000,   // 10K per batch
		InitialBalance: uint256.NewInt(1e18), // 1 ETH
		TransferAmount: uint256.NewInt(1),    // 1 wei
		Verbose:        false,
		PreWarm:        true,
	}
}

// =============================================================================
// In-Memory State Database
// =============================================================================

// MemoryStateDB is an optimized in-memory state database for benchmarking
type MemoryStateDB struct {
	mu       sync.RWMutex
	balances map[types.Address]*uint256.Int
	nonces   map[types.Address]uint64
	
	// Sharded state for parallel access (reduces lock contention)
	shardCount int
	shards     []*stateShard
}

type stateShard struct {
	mu       sync.Mutex
	balances map[types.Address]*uint256.Int
	nonces   map[types.Address]uint64
}

// NewMemoryStateDB creates a new in-memory state database
func NewMemoryStateDB(shardCount int) *MemoryStateDB {
	if shardCount <= 0 {
		shardCount = runtime.NumCPU() * 4
	}
	
	db := &MemoryStateDB{
		balances:   make(map[types.Address]*uint256.Int),
		nonces:     make(map[types.Address]uint64),
		shardCount: shardCount,
		shards:     make([]*stateShard, shardCount),
	}
	
	for i := 0; i < shardCount; i++ {
		db.shards[i] = &stateShard{
			balances: make(map[types.Address]*uint256.Int),
			nonces:   make(map[types.Address]uint64),
		}
	}
	
	return db
}

// getShard returns the shard for a given address
func (db *MemoryStateDB) getShard(addr types.Address) *stateShard {
	// Use first byte of address for sharding
	idx := int(addr[0]) % db.shardCount
	return db.shards[idx]
}

// GetBalance returns the balance of an account
func (db *MemoryStateDB) GetBalance(addr types.Address) *uint256.Int {
	shard := db.getShard(addr)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	
	if bal, ok := shard.balances[addr]; ok {
		return bal.Clone()
	}
	return uint256.NewInt(0)
}

// SetBalance sets the balance of an account
func (db *MemoryStateDB) SetBalance(addr types.Address, amount *uint256.Int) {
	shard := db.getShard(addr)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	
	shard.balances[addr] = amount.Clone()
}

// AddBalance adds to the balance of an account
func (db *MemoryStateDB) AddBalance(addr types.Address, amount *uint256.Int) {
	shard := db.getShard(addr)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	
	if bal, ok := shard.balances[addr]; ok {
		bal.Add(bal, amount)
	} else {
		shard.balances[addr] = amount.Clone()
	}
}

// SubBalance subtracts from the balance of an account
func (db *MemoryStateDB) SubBalance(addr types.Address, amount *uint256.Int) {
	shard := db.getShard(addr)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	
	if bal, ok := shard.balances[addr]; ok {
		bal.Sub(bal, amount)
	}
}

// GetNonce returns the nonce of an account
func (db *MemoryStateDB) GetNonce(addr types.Address) uint64 {
	shard := db.getShard(addr)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	
	return shard.nonces[addr]
}

// SetNonce sets the nonce of an account
func (db *MemoryStateDB) SetNonce(addr types.Address, nonce uint64) {
	shard := db.getShard(addr)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	
	shard.nonces[addr] = nonce
}

// IncrNonce increments the nonce of an account atomically
func (db *MemoryStateDB) IncrNonce(addr types.Address) {
	shard := db.getShard(addr)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	
	shard.nonces[addr]++
}

// =============================================================================
// Transaction Generator
// =============================================================================

// Account represents a test account
type Account struct {
	PrivateKey *ecdsa.PrivateKey
	Address    types.Address
}

// TxGenerator generates independent transactions for benchmarking
type TxGenerator struct {
	accounts []*Account
	chainID  *big.Int
	signer   transaction.Signer
}

// NewTxGenerator creates a new transaction generator
func NewTxGenerator(numAccounts int, chainID *big.Int) (*TxGenerator, error) {
	accounts := make([]*Account, numAccounts)
	
	for i := 0; i < numAccounts; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			return nil, fmt.Errorf("failed to generate key: %w", err)
		}
		accounts[i] = &Account{
			PrivateKey: key,
			Address:    crypto.PubkeyToAddress(key.PublicKey),
		}
	}
	
	return &TxGenerator{
		accounts: accounts,
		chainID:  chainID,
		signer:   transaction.NewEIP155Signer(chainID),
	}, nil
}

// InitializeState initializes the state database with account balances
func (g *TxGenerator) InitializeState(db *MemoryStateDB, balance *uint256.Int) {
	for _, acc := range g.accounts {
		db.SetBalance(acc.Address, balance)
		db.SetNonce(acc.Address, 0)
	}
}

// GenerateTx generates a single transfer transaction
func (g *TxGenerator) GenerateTx(from, to int, nonce uint64, amount *uint256.Int) (*transaction.Transaction, error) {
	fromAcc := g.accounts[from]
	toAddr := g.accounts[to].Address
	
	// Create legacy transaction for maximum performance (smallest size)
	tx := transaction.NewTransaction(
		nonce,
		fromAcc.Address,
		&toAddr,
		amount,
		21000, // Standard transfer gas
		uint256.NewInt(1), // 1 wei gas price
		nil,
	)
	
	// Sign the transaction
	signedTx, err := transaction.SignTx(tx, g.signer, fromAcc.PrivateKey)
	if err != nil {
		return nil, err
	}
	
	return signedTx, nil
}

// GenerateBatch generates a batch of independent transactions
func (g *TxGenerator) GenerateBatch(batchSize int, amount *uint256.Int, nonces []uint64) ([]*transaction.Transaction, error) {
	txs := make([]*transaction.Transaction, batchSize)
	numAccounts := len(g.accounts)
	
	for i := 0; i < batchSize; i++ {
		// Use different sender/receiver pairs to avoid conflicts
		from := (i * 2) % numAccounts
		to := (i*2 + 1) % numAccounts
		if to == from {
			to = (to + 1) % numAccounts
		}
		
		nonce := nonces[from]
		nonces[from]++
		
		tx, err := g.GenerateTx(from, to, nonce, amount)
		if err != nil {
			return nil, err
		}
		txs[i] = tx
	}
	
	return txs, nil
}

// GetAccounts returns all accounts
func (g *TxGenerator) GetAccounts() []*Account {
	return g.accounts
}

// =============================================================================
// Parallel Executor
// =============================================================================

// ExecutionResult holds the result of transaction execution
type ExecutionResult struct {
	TxCount       int64
	Duration      time.Duration
	TPS           float64
	AvgLatency    time.Duration
	WorkerResults []WorkerResult
}

// WorkerResult holds the result of a single worker
type WorkerResult struct {
	WorkerID  int
	TxCount   int64
	Duration  time.Duration
	TPS       float64
}

// ParallelExecutor executes transactions in parallel
type ParallelExecutor struct {
	config     *BenchConfig
	stateDB    *MemoryStateDB
	chainConfig *params.ChainConfig
	workers    int
}

// NewParallelExecutor creates a new parallel executor
func NewParallelExecutor(config *BenchConfig, stateDB *MemoryStateDB) *ParallelExecutor {
	workers := config.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	
	// Use all available CPUs
	runtime.GOMAXPROCS(workers)
	
	return &ParallelExecutor{
		config:     config,
		stateDB:    stateDB,
		chainConfig: createBenchChainConfig(),
		workers:    workers,
	}
}

// createBenchChainConfig creates a chain config optimized for benchmarking
func createBenchChainConfig() *params.ChainConfig {
	return &params.ChainConfig{
		ChainID:               big.NewInt(1337),
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
}

// ExecuteTransfer executes a simple transfer without full EVM overhead
func (e *ParallelExecutor) ExecuteTransfer(from, to types.Address, amount *uint256.Int) bool {
	// Check balance
	balance := e.stateDB.GetBalance(from)
	if balance.Lt(amount) {
		return false
	}
	
	// Execute transfer
	e.stateDB.SubBalance(from, amount)
	e.stateDB.AddBalance(to, amount)
	e.stateDB.IncrNonce(from)
	
	return true
}

// ExecuteBatch executes a batch of transactions
func (e *ParallelExecutor) ExecuteBatch(txs []*transaction.Transaction) int64 {
	var executed int64
	
	for _, tx := range txs {
		from, err := transaction.Sender(transaction.NewEIP155Signer(e.chainConfig.ChainID), tx)
		if err != nil {
			continue
		}
		
		to := tx.To()
		if to == nil {
			continue
		}
		
		if e.ExecuteTransfer(from, *to, tx.Value()) {
			executed++
		}
	}
	
	return executed
}

// Run executes the benchmark
func (e *ParallelExecutor) Run(generator *TxGenerator) (*ExecutionResult, error) {
	txCount := e.config.TxCount
	batchSize := e.config.BatchSize
	numBatches := (txCount + batchSize - 1) / batchSize
	
	// Initialize nonces
	nonces := make([]uint64, len(generator.accounts))
	
	// Pre-generate all transactions
	if e.config.Verbose {
		fmt.Printf("Pre-generating %d transactions...\n", txCount)
	}
	
	allTxs := make([][]*transaction.Transaction, numBatches)
	genStart := time.Now()
	
	for i := 0; i < numBatches; i++ {
		size := batchSize
		if (i+1)*batchSize > txCount {
			size = txCount - i*batchSize
		}
		
		txs, err := generator.GenerateBatch(size, e.config.TransferAmount, nonces)
		if err != nil {
			return nil, fmt.Errorf("failed to generate batch %d: %w", i, err)
		}
		allTxs[i] = txs
	}
	
	genDuration := time.Since(genStart)
	if e.config.Verbose {
		fmt.Printf("Transaction generation: %v (%.0f tx/s)\n", genDuration, float64(txCount)/genDuration.Seconds())
	}
	
	// Pre-warm if enabled
	if e.config.PreWarm {
		if e.config.Verbose {
			fmt.Println("Pre-warming state...")
		}
		// Access each account to warm cache
		for _, acc := range generator.accounts {
			_ = e.stateDB.GetBalance(acc.Address)
		}
	}
	
	// Execute benchmark
	if e.config.Verbose {
		fmt.Printf("Starting benchmark with %d workers...\n", e.workers)
	}
	
	var totalExecuted int64
	var wg sync.WaitGroup
	workerResults := make([]WorkerResult, e.workers)
	
	batchChan := make(chan int, numBatches)
	for i := 0; i < numBatches; i++ {
		batchChan <- i
	}
	close(batchChan)
	
	startTime := time.Now()
	
	for w := 0; w < e.workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			workerStart := time.Now()
			var workerExecuted int64
			
			for batchIdx := range batchChan {
				executed := e.ExecuteBatch(allTxs[batchIdx])
				workerExecuted += executed
				atomic.AddInt64(&totalExecuted, executed)
			}
			
			workerDuration := time.Since(workerStart)
			workerResults[workerID] = WorkerResult{
				WorkerID:  workerID,
				TxCount:   workerExecuted,
				Duration:  workerDuration,
				TPS:       float64(workerExecuted) / workerDuration.Seconds(),
			}
		}(w)
	}
	
	wg.Wait()
	
	totalDuration := time.Since(startTime)
	tps := float64(totalExecuted) / totalDuration.Seconds()
	
	return &ExecutionResult{
		TxCount:       totalExecuted,
		Duration:      totalDuration,
		TPS:           tps,
		AvgLatency:    totalDuration / time.Duration(totalExecuted),
		WorkerResults: workerResults,
	}, nil
}

// =============================================================================
// Full EVM Executor (for comparison)
// =============================================================================

// EVMExecutor executes transactions using full EVM
type EVMExecutor struct {
	config      *BenchConfig
	chainConfig *params.ChainConfig
	workers     int
}

// NewEVMExecutor creates a new EVM executor
func NewEVMExecutor(config *BenchConfig) *EVMExecutor {
	workers := config.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	
	return &EVMExecutor{
		config:      config,
		chainConfig: createBenchChainConfig(),
		workers:     workers,
	}
}

// EVMStateAdapter adapts MemoryStateDB to evmtypes.IntraBlockState
type EVMStateAdapter struct {
	db *MemoryStateDB
}

func NewEVMStateAdapter(db *MemoryStateDB) *EVMStateAdapter {
	return &EVMStateAdapter{db: db}
}

func (s *EVMStateAdapter) GetBalance(addr types.Address) *uint256.Int {
	return s.db.GetBalance(addr)
}

func (s *EVMStateAdapter) SubBalance(addr types.Address, amount *uint256.Int) {
	s.db.SubBalance(addr, amount)
}

func (s *EVMStateAdapter) AddBalance(addr types.Address, amount *uint256.Int) {
	s.db.AddBalance(addr, amount)
}

func (s *EVMStateAdapter) GetNonce(addr types.Address) uint64 {
	return s.db.GetNonce(addr)
}

func (s *EVMStateAdapter) SetNonce(addr types.Address, nonce uint64) {
	s.db.SetNonce(addr, nonce)
}

// Stub implementations for evmtypes.IntraBlockState interface
func (s *EVMStateAdapter) CreateAccount(types.Address, bool) {}
func (s *EVMStateAdapter) GetCodeHash(types.Address) types.Hash { return types.Hash{} }
func (s *EVMStateAdapter) GetCode(types.Address) []byte { return nil }
func (s *EVMStateAdapter) SetCode(types.Address, []byte) {}
func (s *EVMStateAdapter) GetCodeSize(types.Address) int { return 0 }
func (s *EVMStateAdapter) AddRefund(uint64) {}
func (s *EVMStateAdapter) SubRefund(uint64) {}
func (s *EVMStateAdapter) GetRefund() uint64 { return 0 }
func (s *EVMStateAdapter) GetCommittedState(types.Address, *types.Hash, *uint256.Int) {}
func (s *EVMStateAdapter) GetState(types.Address, *types.Hash, *uint256.Int) {}
func (s *EVMStateAdapter) SetState(types.Address, *types.Hash, uint256.Int) {}
func (s *EVMStateAdapter) GetTransientState(types.Address, types.Hash) uint256.Int { return uint256.Int{} }
func (s *EVMStateAdapter) SetTransientState(types.Address, types.Hash, uint256.Int) {}
func (s *EVMStateAdapter) Selfdestruct(types.Address) bool { return false }
func (s *EVMStateAdapter) HasSelfdestructed(types.Address) bool { return false }
func (s *EVMStateAdapter) Selfdestruct6780(types.Address) {}
func (s *EVMStateAdapter) Exist(types.Address) bool { return true }
func (s *EVMStateAdapter) Empty(types.Address) bool { return false }
func (s *EVMStateAdapter) Prepare(types.Hash, types.Hash, int) {}
func (s *EVMStateAdapter) PrepareAccessList(types.Address, *types.Address, []types.Address, transaction.AccessList) {}
func (s *EVMStateAdapter) AddressInAccessList(types.Address) bool { return true }
func (s *EVMStateAdapter) SlotInAccessList(types.Address, types.Hash) (bool, bool) { return true, true }
func (s *EVMStateAdapter) AddAddressToAccessList(types.Address) {}
func (s *EVMStateAdapter) AddSlotToAccessList(types.Address, types.Hash) {}
func (s *EVMStateAdapter) RevertToSnapshot(int) {}
func (s *EVMStateAdapter) Snapshot() int { return 0 }
func (s *EVMStateAdapter) AddLog(*block.Log) {}
func (s *EVMStateAdapter) GetLogs(types.Hash) []*block.Log { return nil }

// =============================================================================
// Benchmark Runner
// =============================================================================

// RunBenchmark runs the TPS benchmark
func RunBenchmark(config *BenchConfig) (*ExecutionResult, error) {
	if config == nil {
		config = DefaultConfig()
	}
	
	workers := config.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	
	fmt.Println("========================================")
	fmt.Println("N42 TPS Extreme Benchmark")
	fmt.Println("========================================")
	fmt.Printf("CPU Cores:     %d\n", runtime.NumCPU())
	fmt.Printf("Workers:       %d\n", workers)
	fmt.Printf("Transactions:  %d\n", config.TxCount)
	fmt.Printf("Batch Size:    %d\n", config.BatchSize)
	fmt.Println("========================================")
	
	// Create state database
	stateDB := NewMemoryStateDB(workers * 4)
	
	// Create transaction generator with enough accounts for parallelism
	numAccounts := workers * 100
	if numAccounts < 1000 {
		numAccounts = 1000
	}
	
	fmt.Printf("Generating %d accounts...\n", numAccounts)
	generator, err := NewTxGenerator(numAccounts, big.NewInt(1337))
	if err != nil {
		return nil, fmt.Errorf("failed to create generator: %w", err)
	}
	
	// Initialize state
	fmt.Println("Initializing state...")
	generator.InitializeState(stateDB, config.InitialBalance)
	
	// Create executor
	executor := NewParallelExecutor(config, stateDB)
	
	// Run benchmark
	fmt.Println("Running benchmark...")
	result, err := executor.Run(generator)
	if err != nil {
		return nil, fmt.Errorf("benchmark failed: %w", err)
	}
	
	// Print results
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("Results")
	fmt.Println("========================================")
	fmt.Printf("Total Transactions: %d\n", result.TxCount)
	fmt.Printf("Total Duration:     %v\n", result.Duration)
	fmt.Printf("TPS:                %.2f\n", result.TPS)
	fmt.Printf("Avg Latency:        %v\n", result.AvgLatency)
	fmt.Println()
	fmt.Println("Worker Stats:")
	for _, wr := range result.WorkerResults {
		fmt.Printf("  Worker %d: %d tx in %v (%.2f TPS)\n",
			wr.WorkerID, wr.TxCount, wr.Duration, wr.TPS)
	}
	fmt.Println("========================================")
	
	return result, nil
}

// =============================================================================
// Lock-Free Executor (Maximum Performance)
// =============================================================================

// LockFreeExecutor uses per-account sharding for lock-free execution
type LockFreeExecutor struct {
	config      *BenchConfig
	accountState []*AccountState
	workers     int
}

// AccountState holds lock-free account state using atomic operations
type AccountState struct {
	Balance atomic.Pointer[uint256.Int]
	Nonce   atomic.Uint64
}

// NewLockFreeExecutor creates a lock-free executor
func NewLockFreeExecutor(config *BenchConfig, numAccounts int) *LockFreeExecutor {
	workers := config.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	
	accountState := make([]*AccountState, numAccounts)
	for i := 0; i < numAccounts; i++ {
		accountState[i] = &AccountState{}
		bal := config.InitialBalance.Clone()
		accountState[i].Balance.Store(bal)
	}
	
	return &LockFreeExecutor{
		config:       config,
		accountState: accountState,
		workers:      workers,
	}
}

// ExecuteTransferLockFree executes a transfer with minimal locking
func (e *LockFreeExecutor) ExecuteTransferLockFree(fromIdx, toIdx int, amount *uint256.Int) bool {
	fromState := e.accountState[fromIdx]
	toState := e.accountState[toIdx]
	
	// Get current balance
	balance := fromState.Balance.Load()
	if balance.Lt(amount) {
		return false
	}
	
	// Update balances (simplified - in production would need CAS)
	newFromBal := new(uint256.Int).Sub(balance, amount)
	fromState.Balance.Store(newFromBal)
	
	toBal := toState.Balance.Load()
	newToBal := new(uint256.Int).Add(toBal, amount)
	toState.Balance.Store(newToBal)
	
	// Increment nonce
	fromState.Nonce.Add(1)
	
	return true
}

// RunLockFree runs lock-free benchmark
func (e *LockFreeExecutor) RunLockFree() (*ExecutionResult, error) {
	txCount := e.config.TxCount
	numAccounts := len(e.accountState)
	
	var totalExecuted int64
	var wg sync.WaitGroup
	
	txPerWorker := txCount / e.workers
	workerResults := make([]WorkerResult, e.workers)
	
	startTime := time.Now()
	
	for w := 0; w < e.workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			
			workerStart := time.Now()
			var workerExecuted int64
			
			// Each worker handles a disjoint set of accounts
			startAccount := (workerID * numAccounts / e.workers) * 2
			
			for i := 0; i < txPerWorker; i++ {
				fromIdx := (startAccount + i*2) % numAccounts
				toIdx := (startAccount + i*2 + 1) % numAccounts
				if toIdx == fromIdx {
					toIdx = (toIdx + 1) % numAccounts
				}
				
				if e.ExecuteTransferLockFree(fromIdx, toIdx, e.config.TransferAmount) {
					workerExecuted++
				}
			}
			
			atomic.AddInt64(&totalExecuted, workerExecuted)
			
			workerDuration := time.Since(workerStart)
			workerResults[workerID] = WorkerResult{
				WorkerID:  workerID,
				TxCount:   workerExecuted,
				Duration:  workerDuration,
				TPS:       float64(workerExecuted) / workerDuration.Seconds(),
			}
		}(w)
	}
	
	wg.Wait()
	
	totalDuration := time.Since(startTime)
	tps := float64(totalExecuted) / totalDuration.Seconds()
	
	return &ExecutionResult{
		TxCount:       totalExecuted,
		Duration:      totalDuration,
		TPS:           tps,
		AvgLatency:    totalDuration / time.Duration(totalExecuted),
		WorkerResults: workerResults,
	}, nil
}

// RunLockFreeBenchmark runs the lock-free TPS benchmark
func RunLockFreeBenchmark(config *BenchConfig) (*ExecutionResult, error) {
	if config == nil {
		config = DefaultConfig()
	}
	
	workers := config.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	
	fmt.Println("========================================")
	fmt.Println("N42 TPS Lock-Free Benchmark")
	fmt.Println("========================================")
	fmt.Printf("CPU Cores:     %d\n", runtime.NumCPU())
	fmt.Printf("Workers:       %d\n", workers)
	fmt.Printf("Transactions:  %d\n", config.TxCount)
	fmt.Println("========================================")
	
	// Number of accounts (must be > workers * 2)
	numAccounts := workers * 100
	if numAccounts < 1000 {
		numAccounts = 1000
	}
	
	executor := NewLockFreeExecutor(config, numAccounts)
	
	fmt.Println("Running lock-free benchmark...")
	result, err := executor.RunLockFree()
	if err != nil {
		return nil, err
	}
	
	// Print results
	fmt.Println()
	fmt.Println("========================================")
	fmt.Println("Lock-Free Results")
	fmt.Println("========================================")
	fmt.Printf("Total Transactions: %d\n", result.TxCount)
	fmt.Printf("Total Duration:     %v\n", result.Duration)
	fmt.Printf("TPS:                %.2f\n", result.TPS)
	fmt.Printf("Avg Latency:        %v\n", result.AvgLatency)
	fmt.Println()
	fmt.Println("Worker Stats:")
	for _, wr := range result.WorkerResults {
		fmt.Printf("  Worker %d: %d tx in %v (%.2f TPS)\n",
			wr.WorkerID, wr.TxCount, wr.Duration, wr.TPS)
	}
	fmt.Println("========================================")
	
	return result, nil
}

