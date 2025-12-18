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

// Package txgen provides automatic transaction generation for development and testing.
package txgen

import (
	"context"
	"crypto/ecdsa"
	crand "crypto/rand"
	mrand "math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	event "github.com/n42blockchain/N42/modules/event/v2"
	"github.com/n42blockchain/N42/log"
)

// Config holds configuration for the transaction generator.
type Config struct {
	Enabled        bool          // Whether tx generation is enabled
	MaxTxsPerBlock int           // Maximum transactions per block (0-31)
	Interval       time.Duration // Interval between tx batches
	GasPrice       uint64        // Gas price in wei
	GasLimit       uint64        // Gas limit per transaction
	Value          uint64        // Value to transfer in wei
	FaucetAmount   uint64        // Amount to fund each test account (in wei)
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled:        false,
		MaxTxsPerBlock: 10,
		Interval:       time.Second,
		GasPrice:       1000000000,        // 1 Gwei
		GasLimit:       21000,             // Basic transfer
		Value:          1000,              // 1000 wei
		FaucetAmount:   1000000000000000000, // 1 ETH per account
	}
}

// Generator generates random transactions for development testing.
type Generator struct {
	config    *Config
	txPool    common.ITxsPool
	chainID   *uint256.Int
	
	// Coinbase (faucet source)
	coinbase  types.Address
	accman    *accounts.Manager
	
	// Test accounts (generated at startup)
	accounts  []*testAccount
	funded    atomic.Bool // Whether test accounts have been funded
	funding   atomic.Bool // Whether funding is in progress (prevent duplicate runs)
	
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	running   atomic.Bool
	
	// Nonce tracking
	nonceMu   sync.Mutex
	nonces    map[types.Address]uint64
}

type testAccount struct {
	privateKey *ecdsa.PrivateKey
	address    types.Address
}

// New creates a new transaction generator.
func New(ctx context.Context, config *Config, txPool common.ITxsPool, chainID *uint256.Int, coinbase types.Address, accman *accounts.Manager) *Generator {
	// Seed random number generator
	mrand.Seed(time.Now().UnixNano())
	
	ctx, cancel := context.WithCancel(ctx)
	g := &Generator{
		config:   config,
		txPool:   txPool,
		chainID:  chainID,
		coinbase: coinbase,
		accman:   accman,
		ctx:      ctx,
		cancel:   cancel,
		nonces:   make(map[types.Address]uint64),
	}
	
	// Generate test accounts
	g.generateTestAccounts(10)
	
	return g
}

// generateTestAccounts creates test accounts with private keys.
func (g *Generator) generateTestAccounts(count int) {
	g.accounts = make([]*testAccount, count)
	for i := 0; i < count; i++ {
		privateKey, err := ecdsa.GenerateKey(crypto.S256(), crand.Reader)
		if err != nil {
			log.Error("Failed to generate test account", "err", err)
			continue
		}
		
		pubKey := privateKey.PublicKey
		addr := crypto.PubkeyToAddress(pubKey)
		
		g.accounts[i] = &testAccount{
			privateKey: privateKey,
			address:    addr,
		}
		g.nonces[addr] = 0
		
		log.Debug("Generated test account", "index", i, "address", addr.Hex())
	}
}

// Start begins generating transactions.
func (g *Generator) Start() {
	if !g.config.Enabled {
		log.Info("Transaction generator is disabled")
		return
	}
	
	if g.running.Load() {
		return
	}
	g.running.Store(true)
	
	log.Info("Starting transaction generator",
		"maxTxsPerBlock", g.config.MaxTxsPerBlock,
		"interval", g.config.Interval,
		"accounts", len(g.accounts),
		"coinbase", g.coinbase.Hex())
	
	// Subscribe to new block events to trigger tx generation
	blockCh := make(chan common.ChainHighestBlock, 10)
	sub := event.GlobalEvent.Subscribe(blockCh)
	
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		defer sub.Unsubscribe()
		
		// Wait a bit for the first block to be mined
		time.Sleep(3 * time.Second)
		
		ticker := time.NewTicker(g.config.Interval)
		defer ticker.Stop()
		
		for {
			select {
			case <-g.ctx.Done():
				log.Info("Transaction generator stopped")
				return
			case block := <-blockCh:
				// New block sealed
				// First, try to fund test accounts if not yet funded
				if !g.funded.Load() && block.Block.Number64().Uint64() >= 1 {
					g.fundTestAccounts()
				}
				// Then generate more transactions
				if g.funded.Load() {
					g.generateAndSubmitTxs()
				}
			case <-ticker.C:
				// Periodic: try funding first, then generate
				if !g.funded.Load() {
					g.fundTestAccounts()
				}
				if g.funded.Load() {
					g.generateAndSubmitTxs()
				}
			}
		}
	}()
}

// Stop stops the transaction generator.
func (g *Generator) Stop() {
	if !g.running.Load() {
		return
	}
	g.running.Store(false)
	g.cancel()
	g.wg.Wait()
	log.Info("Transaction generator stopped")
}

// fundTestAccounts sends funds from coinbase to all test accounts (auto faucet).
func (g *Generator) fundTestAccounts() {
	// Already funded or funding in progress
	if g.funded.Load() || g.funding.Load() {
		return
	}
	
	if g.coinbase == (types.Address{}) {
		// Silent return - coinbase not set yet
		return
	}
	
	if g.accman == nil {
		// Silent return - account manager not available yet
		return
	}
	
	// Find coinbase wallet
	wallet, err := g.accman.Find(accounts.Account{Address: g.coinbase})
	if err != nil || wallet == nil {
		// Silent return - wallet not unlocked yet
		return
	}
	
	// Mark funding as in progress to prevent duplicate runs
	if !g.funding.CompareAndSwap(false, true) {
		return // Another goroutine started funding
	}
	defer func() {
		if !g.funded.Load() {
			g.funding.Store(false) // Reset if failed
		}
	}()
	
	// Only log once we're ready to proceed
	log.Info("=== Auto Faucet: Funding test accounts from coinbase ===",
		"coinbase", g.coinbase.Hex(),
		"amount", g.config.FaucetAmount,
		"accounts", len(g.accounts))
	
	// Get current nonce for coinbase
	g.nonceMu.Lock()
	nonce, exists := g.nonces[g.coinbase]
	if !exists {
		nonce = 0
	}
	g.nonceMu.Unlock()
	
	successCount := 0
	for i, acc := range g.accounts {
		tx := g.createFundingTx(acc.address, nonce)
		if tx == nil {
			continue
		}
		
		// Sign with coinbase wallet
		signedTx, err := wallet.SignTx(accounts.Account{Address: g.coinbase}, tx, g.chainID.ToBig())
		if err != nil {
			log.Debug("Failed to sign funding tx", "err", err, "account", i)
			continue
		}
		
		// Submit to pool
		if err := g.txPool.AddLocal(signedTx); err != nil {
			log.Debug("Failed to submit funding tx", "err", err, "account", i)
			continue
		}
		
		nonce++
		successCount++
		log.Info("Funded test account",
			"index", i,
			"address", acc.address.Hex(),
			"amount", g.config.FaucetAmount)
	}
	
	// Update nonce
	g.nonceMu.Lock()
	g.nonces[g.coinbase] = nonce
	g.nonceMu.Unlock()
	
	if successCount > 0 {
		g.funded.Store(true)
		log.Info("=== Auto Faucet complete ===",
			"funded", successCount,
			"total", len(g.accounts))
	}
}

// createFundingTx creates a transaction to fund a test account from coinbase.
func (g *Generator) createFundingTx(to types.Address, nonce uint64) *transaction.Transaction {
	gasPrice := uint256.NewInt(g.config.GasPrice)
	value := uint256.NewInt(g.config.FaucetAmount)
	
	innerTx := &transaction.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      g.config.GasLimit,
		To:       &to,
		Value:    value,
		Data:     nil,
		From:     &g.coinbase,
	}
	
	return transaction.NewTx(innerTx)
}

// generateAndSubmitTxs generates and submits a batch of transactions.
func (g *Generator) generateAndSubmitTxs() {
	if len(g.accounts) < 2 {
		log.Warn("Not enough test accounts for transaction generation")
		return
	}
	
	// Random number of transactions (0 to maxTxsPerBlock)
	numTxs := mrand.Intn(g.config.MaxTxsPerBlock + 1)
	if numTxs == 0 {
		return
	}
	
	txs := make([]*transaction.Transaction, 0, numTxs)
	
	for i := 0; i < numTxs; i++ {
		tx := g.createRandomTx()
		if tx != nil {
			txs = append(txs, tx)
		}
	}
	
	if len(txs) == 0 {
		return
	}
	
	// Submit to transaction pool
	successCount := 0
	for _, tx := range txs {
		if err := g.txPool.AddLocal(tx); err == nil {
			successCount++
		}
	}
	
	log.Debug("Generated transactions",
		"attempted", numTxs,
		"submitted", len(txs),
		"success", successCount)
}

// createRandomTx creates a random transaction between test accounts.
func (g *Generator) createRandomTx() *transaction.Transaction {
	if len(g.accounts) < 2 {
		return nil
	}
	
	// Select random sender and receiver
	senderIdx := mrand.Intn(len(g.accounts))
	receiverIdx := mrand.Intn(len(g.accounts))
	
	// Ensure sender != receiver
	for receiverIdx == senderIdx {
		receiverIdx = mrand.Intn(len(g.accounts))
	}
	
	sender := g.accounts[senderIdx]
	receiver := g.accounts[receiverIdx]
	
	// Get and increment nonce
	g.nonceMu.Lock()
	nonce := g.nonces[sender.address]
	g.nonces[sender.address]++
	g.nonceMu.Unlock()
	
	// Create transaction
	gasPrice := uint256.NewInt(g.config.GasPrice)
	value := uint256.NewInt(g.config.Value)
	
	// Add some randomness to value
	if mrand.Float32() > 0.5 {
		value = uint256.NewInt(uint64(mrand.Intn(10000) + 1))
	}
	
	innerTx := &transaction.LegacyTx{
		Nonce:    nonce,
		GasPrice: gasPrice,
		Gas:      g.config.GasLimit,
		To:       &receiver.address,
		Value:    value,
		Data:     nil,
		From:     &sender.address,
	}
	
	tx := transaction.NewTx(innerTx)
	
	// Sign transaction
	signedTx, err := g.signTx(tx, sender.privateKey)
	if err != nil {
		log.Debug("Failed to sign transaction", "err", err)
		return nil
	}
	
	return signedTx
}

// signTx signs a transaction with the given private key.
func (g *Generator) signTx(tx *transaction.Transaction, priv *ecdsa.PrivateKey) (*transaction.Transaction, error) {
	signer := transaction.NewLondonSigner(g.chainID.ToBig())
	return transaction.SignTx(tx, signer, priv)
}

// GetTestAccounts returns the test accounts for funding purposes.
func (g *Generator) GetTestAccounts() []types.Address {
	addresses := make([]types.Address, len(g.accounts))
	for i, acc := range g.accounts {
		addresses[i] = acc.address
	}
	return addresses
}

// ResetNonces resets nonce tracking (useful after chain reset).
func (g *Generator) ResetNonces() {
	g.nonceMu.Lock()
	defer g.nonceMu.Unlock()
	
	for addr := range g.nonces {
		g.nonces[addr] = 0
	}
}

// SetNonce sets the nonce for a specific address.
func (g *Generator) SetNonce(addr types.Address, nonce uint64) {
	g.nonceMu.Lock()
	defer g.nonceMu.Unlock()
	g.nonces[addr] = nonce
}

// FundAccounts logs the test account addresses.
// With auto-faucet enabled, these will be automatically funded from coinbase.
func (g *Generator) FundAccounts() {
	log.Info("=== Transaction Generator Test Accounts ===")
	log.Info("These accounts will be auto-funded from coinbase after first block")
	log.Info("Coinbase (faucet source)", "address", g.coinbase.Hex())
	log.Info("Faucet amount per account", "wei", g.config.FaucetAmount)
	for i, acc := range g.accounts {
		log.Debug("Test account", "index", i, "address", acc.address.Hex())
	}
	log.Info("Total test accounts", "count", len(g.accounts))
	log.Info("============================================")
}

