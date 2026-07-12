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

	// Load-test ERC20 (deployed by the faucet key once funding completes).
	erc20Addr     types.Address
	erc20Deployed atomic.Bool // deploy tx submitted
	erc20Seeded   atomic.Bool // test accounts hold token balances

	// skipTick marks senders to avoid for the current batch (e.g. insufficient
	// funds) so the generator never keeps submitting unviable transactions.
	skipTick map[types.Address]bool

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
				if !g.funded.Load() {
					continue
				}
				// Deploy + seed the load-test ERC20 (contract-path coverage),
				// then generate the mixed native/ERC20 load.
				if g.coinbaseKey != nil && !g.erc20Seeded.Load() {
					g.setupERC20()
				}
				g.generateAndSubmitTxs()
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

// setupERC20 deploys the load-test ERC20 from the faucet key, then seeds every
// test account with a token balance. Retryable: each stage is guarded by its
// own flag and re-attempted on the next tick until it lands.
func (g *Generator) setupERC20() {
	if !g.erc20Deployed.Load() {
		nonce := g.txPool.Nonce(g.coinbase)
		deployTx := transaction.NewTx(&transaction.LegacyTx{
			Nonce:    nonce,
			GasPrice: uint256.NewInt(g.config.GasPrice),
			Gas:      300000,
			To:       nil, // contract creation
			Value:    uint256.NewInt(0),
			Data:     erc20DeployCode(),
			From:     &g.coinbase,
		})
		signed, err := g.signTx(deployTx, g.coinbaseKey)
		if err != nil {
			log.Warn("TxGen: ERC20 deploy sign failed", "err", err)
			return
		}
		if err := g.txPool.AddLocal(signed); err != nil {
			log.Warn("TxGen: ERC20 deploy submit failed, will retry", "err", err)
			return
		}
		g.erc20Addr = crypto.CreateAddress(g.coinbase, nonce)
		g.erc20Deployed.Store(true)
		log.Info("TxGen: ERC20 deployed", "address", g.erc20Addr.Hex(), "nonce", nonce)
		return // let the deployment mine before seeding
	}

	// Seed each test account with tokens from the deployer's supply.
	nonce := g.txPool.Nonce(g.coinbase)
	seeded := 0
	for _, acc := range g.accounts {
		tx := transaction.NewTx(&transaction.LegacyTx{
			Nonce:    nonce,
			GasPrice: uint256.NewInt(g.config.GasPrice),
			Gas:      60000,
			To:       &g.erc20Addr,
			Value:    uint256.NewInt(0),
			Data:     erc20TransferCalldata(acc.address, uint256.MustFromDecimal("1000000000000000000000")), // 1e21
			From:     &g.coinbase,
		})
		signed, err := g.signTx(tx, g.coinbaseKey)
		if err != nil {
			log.Warn("TxGen: ERC20 seed sign failed", "err", err)
			return
		}
		if err := g.txPool.AddLocal(signed); err != nil {
			log.Warn("TxGen: ERC20 seed submit failed, will retry", "seeded", seeded, "err", err)
			return
		}
		nonce++
		seeded++
	}
	g.erc20Seeded.Store(true)
	log.Info("TxGen: ERC20 test accounts seeded", "accounts", seeded, "token", g.erc20Addr.Hex())
}

// generateAndSubmitTxs generates and submits a batch of transactions: ~70%
// native transfers, ~30% ERC20 transfers once the token is seeded.
//
// Failure adaptation — the generator must never keep submitting transactions
// it knows cannot land:
//   - nonce errors invalidate the local nonce view for that sender (the next
//     use re-queries the pool, which reconciles against post-replay state)
//   - insufficient funds sidelines the sender for the rest of the batch
//   - a full pool aborts the batch (back-off to the next tick)
func (g *Generator) generateAndSubmitTxs() {
	if len(g.accounts) < 2 {
		return
	}

	// Per-batch nonce ledger: within one tick the pool's pending view may lag
	// our own submissions, so consecutive txs from one sender must self-assign.
	nonces := make(map[types.Address]uint64, len(g.accounts))
	g.skipTick = make(map[types.Address]bool)
	nextNonce := func(addr types.Address) uint64 {
		if n, ok := nonces[addr]; ok {
			return n
		}
		n := g.txPool.Nonce(addr)
		nonces[addr] = n
		return n
	}

	numTxs := misc.SecureIntn(g.config.MaxTxsPerBlock) + 1
	successCount, failCount := 0, 0
	for i := 0; i < numTxs; i++ {
		senderIdx := misc.SecureIntn(len(g.accounts))
		sender := g.accounts[senderIdx]
		if sender == nil || g.skipTick[sender.address] {
			continue
		}
		receiver := g.accounts[(senderIdx+1+misc.SecureIntn(len(g.accounts)-1))%len(g.accounts)]
		if receiver == nil {
			continue
		}

		nonce := nextNonce(sender.address)
		var inner *transaction.LegacyTx
		if g.erc20Seeded.Load() && misc.SecureIntn(10) < 3 {
			// ERC20 transfer (~30%): contract execution + storage + Transfer log.
			inner = &transaction.LegacyTx{
				Nonce:    nonce,
				GasPrice: uint256.NewInt(g.config.GasPrice),
				Gas:      60000,
				To:       &g.erc20Addr,
				Value:    uint256.NewInt(0),
				Data:     erc20TransferCalldata(receiver.address, uint256.NewInt(uint64(misc.SecureIntn(1000)+1))),
				From:     &sender.address,
			}
		} else {
			inner = &transaction.LegacyTx{
				Nonce:    nonce,
				GasPrice: uint256.NewInt(g.config.GasPrice),
				Gas:      g.config.GasLimit,
				To:       &receiver.address,
				Value:    uint256.NewInt(uint64(misc.SecureIntn(1000) + 1)),
				From:     &sender.address,
			}
		}

		signedTx, err := g.signTx(transaction.NewTx(inner), sender.privateKey)
		if err != nil {
			failCount++
			continue
		}
		if err := g.txPool.AddLocal(signedTx); err != nil {
			failCount++
			// First failure per tick surfaces at Warn regardless of class - a
			// persistently failing generator was invisible below (observed
			// live: hours of failed=N submitted=0 with no reason in the log).
			if failCount == 1 {
				log.Warn("TxGen: AddLocal failed (first this tick)", "err", err)
			}
			msg := err.Error()
			switch {
			case strings.Contains(msg, "nonce"):
				// Stale nonce view (post-unwind/replay reconciliation): drop the
				// cached value so the next use re-queries the pool. Do NOT retry
				// this exact tx — it is unviable by construction.
				delete(nonces, sender.address)
			case strings.Contains(msg, "insufficient funds"):
				g.skipTick[sender.address] = true
			case strings.Contains(msg, "txpool is full") || strings.Contains(msg, "pool is full"):
				log.Warn("TxGen: pool full, backing off", "submitted", successCount)
				return
			default:
				log.Debug("TxGen: AddLocal failed", "err", err)
			}
			continue
		}
		nonces[sender.address] = nonce + 1
		successCount++
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
