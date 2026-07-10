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
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/misc"
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
	CoinbaseKey    string        // Hex secp256k1 private key for the coinbase (faucet); bypasses the keystore
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		Enabled:        false,
		MaxTxsPerBlock: 10,
		Interval:       time.Second,
		GasPrice:       1000000000,          // 1 Gwei
		GasLimit:       21000,               // Basic transfer
		Value:          1000,                // 1000 wei
		FaucetAmount:   1000000000000000000, // 1 ETH per account
	}
}

// Generator generates random transactions for development testing.
type Generator struct {
	config  *Config
	txPool  common.ITxsPool
	chainID *uint256.Int

	// Coinbase (faucet source)
	coinbase    types.Address
	accman      *accounts.Manager
	coinbaseKey *ecdsa.PrivateKey // direct signing key (CoinbaseKey config); nil → keystore lookup

	// Test accounts (generated at startup)
	accounts []*testAccount
	funded   atomic.Bool // Whether test accounts have been funded
	funding  atomic.Bool // Whether funding is in progress (prevent duplicate runs)

	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running atomic.Bool
}

type testAccount struct {
	privateKey *ecdsa.PrivateKey
	address    types.Address
}

// New creates a new transaction generator.
func New(ctx context.Context, config *Config, txPool common.ITxsPool, chainID *uint256.Int, coinbase types.Address, accman *accounts.Manager) *Generator {
	ctx, cancel := context.WithCancel(ctx)
	g := &Generator{
		config:   config,
		txPool:   txPool,
		chainID:  chainID,
		coinbase: coinbase,
		accman:   accman,
		ctx:      ctx,
		cancel:   cancel,
	}

	// Generate test accounts
	g.generateTestAccounts(10)

	// Direct faucet key (validator deployments carry only BLS keys in the
	// keystore, so the accman lookup can never find the coinbase wallet).
	if config.CoinbaseKey != "" {
		key, err := crypto.HexToECDSA(strings.TrimPrefix(config.CoinbaseKey, "0x"))
		if err != nil {
			log.Warn("TxGen: invalid dev.txgen.key, falling back to keystore", "err", err)
		} else if derived := crypto.PubkeyToAddress(key.PublicKey); derived != coinbase {
			log.Warn("TxGen: dev.txgen.key does not match the coinbase; using the key's own address as faucet",
				"keyAddr", derived.Hex(), "coinbase", coinbase.Hex())
			g.coinbaseKey = key
			g.coinbase = derived
		} else {
			g.coinbaseKey = key
		}
	}

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
		log.Debug("Generated test account", "index", i, "address", addr.Hex())
	}
}

// Start begins generating transactions.
func (g *Generator) Start() {
	if !g.config.Enabled {
		return
	}

	if g.running.Load() {
		return
	}
	g.running.Store(true)

	log.Info("TxGen started", "maxTx", g.config.MaxTxsPerBlock, "interval", g.config.Interval)

	g.wg.Add(1)
	go func() {
		defer g.wg.Done()

		// Wait for miner to start
		time.Sleep(3 * time.Second)

		ticker := time.NewTicker(g.config.Interval)
		defer ticker.Stop()

		for {
			select {
			case <-g.ctx.Done():
				return
			case <-ticker.C:
				// Fund test accounts once
				if !g.funded.Load() {
					g.fundTestAccounts()
				}
				// Generate transactions after funding
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
}

// fundTestAccounts sends funds from coinbase to all test accounts (auto faucet).
func (g *Generator) fundTestAccounts() {
	if g.funded.Load() {
		return
	}
	// Single in-flight attempt; RETRYABLE on failure. The old once-only version
	// wedged silently forever when the coinbase wallet wasn't in the keystore
	// (validator deployments carry only BLS keys) or when the coinbase balance
	// was still zero (dev reward accrues one leader turn at a time) — observed
	// live: "TxGen started" then 300+ blocks of zero transactions.
	if !g.funding.CompareAndSwap(false, true) {
		return
	}
	defer g.funding.Store(false)

	if g.coinbase == (types.Address{}) {
		return
	}

	// Signing function: direct key (dev.txgen.key) or keystore wallet.
	sign := func(tx *transaction.Transaction) (*transaction.Transaction, error) {
		if g.coinbaseKey != nil {
			return g.signTx(tx, g.coinbaseKey)
		}
		if g.accman == nil {
			return nil, errors.New("no coinbase key and no keystore")
		}
		wallet, err := g.accman.Find(accounts.Account{Address: g.coinbase})
		if err != nil || wallet == nil {
			return nil, errors.New("coinbase wallet not in keystore (BLS-only keystore?) — set --dev.txgen.key")
		}
		return wallet.SignTx(accounts.Account{Address: g.coinbase}, tx, g.chainID.ToBig())
	}

	nonce := g.txPool.Nonce(g.coinbase)
	successCount := 0
	var lastErr error
	for _, acc := range g.accounts {
		tx := g.createFundingTx(acc.address, nonce)
		if tx == nil {
			continue
		}
		signedTx, err := sign(tx)
		if err != nil {
			lastErr = err
			break // signing won't recover mid-loop
		}
		if err := g.txPool.AddLocal(signedTx); err != nil {
			lastErr = err // typically insufficient funds while rewards accrue — retry next tick
			continue
		}
		nonce++
		successCount++
	}

	if successCount == len(g.accounts) {
		g.funded.Store(true)
		log.Info("Auto-faucet complete", "funded", successCount)
		return
	}
	log.Warn("TxGen: faucet incomplete, will retry", "funded", successCount,
		"total", len(g.accounts), "err", lastErr)
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
		return
	}

	// Random number of transactions (1 to maxTxsPerBlock)
	numTxs := misc.SecureIntn(g.config.MaxTxsPerBlock) + 1

	successCount := 0
	failCount := 0
	for i := 0; i < numTxs; i++ {
		tx := g.createRandomTx()
		if tx == nil {
			failCount++
			continue
		}
		if err := g.txPool.AddLocal(tx); err != nil {
			failCount++
			log.Debug("TxGen: AddLocal failed", "err", err)
		} else {
			successCount++
		}
	}

	if successCount > 0 || failCount > 0 {
		log.Info("TxGen", "submitted", successCount, "failed", failCount)
	}
}

// createRandomTx creates a random transaction between test accounts.
func (g *Generator) createRandomTx() *transaction.Transaction {
	if len(g.accounts) < 2 {
		return nil
	}

	// Select random sender and receiver
	senderIdx := misc.SecureIntn(len(g.accounts))
	receiverIdx := (senderIdx + 1 + misc.SecureIntn(len(g.accounts)-1)) % len(g.accounts)

	sender := g.accounts[senderIdx]
	receiver := g.accounts[receiverIdx]

	// Get nonce from txpool (includes pending txs)
	nonce := g.txPool.Nonce(sender.address)

	// Small random value (avoid running out of funds)
	value := uint256.NewInt(uint64(misc.SecureIntn(1000) + 1))

	innerTx := &transaction.LegacyTx{
		Nonce:    nonce,
		GasPrice: uint256.NewInt(g.config.GasPrice),
		Gas:      g.config.GasLimit,
		To:       &receiver.address,
		Value:    value,
		Data:     nil,
		From:     &sender.address,
	}

	tx := transaction.NewTx(innerTx)
	signedTx, err := g.signTx(tx, sender.privateKey)
	if err != nil {
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

// FundAccounts logs the test account info.
func (g *Generator) FundAccounts() {
	log.Info("TxGen ready", "accounts", len(g.accounts), "coinbase", g.coinbase.Hex())
}
