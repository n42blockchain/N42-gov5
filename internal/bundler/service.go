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

// Package bundler implements an ERC-4337 bundler service.
//
// The bundler maintains a separate mempool for UserOperations and periodically
// constructs bundle transactions that call the EntryPoint contract's handleOps
// method. Bundles are submitted as standard transactions to the txpool.
//
// Architecture:
//
//	                   ┌────────────────────┐
//	  RPC endpoint ──▸ │    BundlerService   │
//	                   │  ┌──────────────┐  │
//	  eth_sendUserOp   │  │  Validator    │  │
//	  ──────────────▸  │  └──────┬───────┘  │
//	                   │         ▼          │
//	                   │  ┌──────────────┐  │
//	                   │  │  UserOpPool   │  │
//	                   │  └──────┬───────┘  │
//	                   │         ▼          │
//	                   │  ┌──────────────┐  │  ──▸ Standard TxPool
//	                   │  │ BundleBuilder│  │
//	                   │  └──────────────┘  │
//	                   └────────────────────┘
package bundler

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/log"
)

// TxSubmitter abstracts transaction submission to the txpool.
type TxSubmitter interface {
	SubmitBundleTx(entryPoint types.Address, calldata []byte, gasLimit uint64) error
}

// BundlerService is the main ERC-4337 bundler service.
type BundlerService struct {
	config      *Config
	pool        *UserOpPool
	validator   *Validator
	builder     *BundleBuilder
	chainID     uint64
	txSubmitter TxSubmitter

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// SetTxSubmitter sets the transaction submitter for bundle submission.
func (s *BundlerService) SetTxSubmitter(ts TxSubmitter) {
	s.txSubmitter = ts
}

// NewBundlerService creates a new bundler service.
func NewBundlerService(config *Config, chainID uint64) *BundlerService {
	if config == nil {
		config = DefaultConfig()
	}
	entryPoint := vm.EntryPointV07
	if len(config.EntryPoints) > 0 {
		entryPoint = config.EntryPoints[0]
	}
	return &BundlerService{
		config:    config,
		pool:      NewUserOpPool(config.MaxPoolSize, entryPoint, chainID),
		validator: NewValidator(config),
		builder:   NewBundleBuilder(entryPoint),
		chainID:   chainID,
	}
}

// Start starts the background bundle creation loop.
func (s *BundlerService) Start(ctx context.Context) {
	ctx, s.cancel = context.WithCancel(ctx)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in bundler loop, recovered", "panic", r)
			}
		}()
		s.bundleLoop(ctx)
	}()
	log.Info("Bundler service started",
		"entryPoints", len(s.config.EntryPoints),
		"interval", s.config.BundleInterval,
		"maxPool", s.config.MaxPoolSize)
}

// Stop stops the bundler service.
func (s *BundlerService) Stop() {
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
	log.Info("Bundler service stopped", "remaining", s.pool.Count())
}

// SendUserOperation validates and adds a UserOperation to the mempool.
func (s *BundlerService) SendUserOperation(op *vm.UserOperation, entryPoint types.Address) (types.Hash, error) {
	opsReceived.Inc()

	// Static validation.
	if err := s.validator.ValidateStatic(op, entryPoint); err != nil {
		opsRejected.Inc()
		return types.Hash{}, err
	}

	// Add to pool.
	hash, err := s.pool.Add(op)
	if err != nil {
		opsRejected.Inc()
		return types.Hash{}, err
	}

	opsValidated.Inc()
	poolSize.Set(uint64(s.pool.Count()))
	return hash, nil
}

// SendUserOperationWithState validates (including state checks) and adds a UserOperation.
func (s *BundlerService) SendUserOperationWithState(op *vm.UserOperation, entryPoint types.Address, state StateReader) (types.Hash, error) {
	opsReceived.Inc()

	// Static validation.
	if err := s.validator.ValidateStatic(op, entryPoint); err != nil {
		opsRejected.Inc()
		return types.Hash{}, err
	}

	// Stateful validation.
	if err := s.validator.ValidateStateful(op, state); err != nil {
		opsRejected.Inc()
		return types.Hash{}, err
	}

	// Add to pool.
	hash, err := s.pool.Add(op)
	if err != nil {
		opsRejected.Inc()
		return types.Hash{}, err
	}

	opsValidated.Inc()
	poolSize.Set(uint64(s.pool.Count()))
	return hash, nil
}

// GetUserOperation returns a pending UserOperation by hash.
func (s *BundlerService) GetUserOperation(hash types.Hash) (*vm.UserOperation, bool) {
	return s.pool.Get(hash)
}

// SupportedEntryPoints returns the list of supported EntryPoint addresses.
func (s *BundlerService) SupportedEntryPoints() []types.Address {
	return s.config.EntryPoints
}

// EstimateUserOperationGas estimates gas values for a UserOperation.
func (s *BundlerService) EstimateUserOperationGas(op *vm.UserOperation) (*GasEstimate, error) {
	if err := s.validator.ValidateStatic(op, s.config.EntryPoints[0]); err != nil {
		return nil, err
	}

	return &GasEstimate{
		PreVerificationGas:   vm.CalcPreVerificationGas(op, nil),
		VerificationGasLimit: vm.VerificationGasLimit,
		CallGasLimit:         vm.UserOperationCallGasLimit,
	}, nil
}

// Pool returns the underlying UserOp mempool (for testing/inspection).
func (s *BundlerService) Pool() *UserOpPool {
	return s.pool
}

// GasEstimate contains estimated gas values for a UserOperation.
type GasEstimate struct {
	PreVerificationGas   uint64 `json:"preVerificationGas"`
	VerificationGasLimit uint64 `json:"verificationGasLimit"`
	CallGasLimit         uint64 `json:"callGasLimit"`
}

// bundleLoop periodically creates bundles from pending UserOperations.
func (s *BundlerService) bundleLoop(ctx context.Context) {
	ticker := time.NewTicker(s.config.BundleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.tryCreateBundle(); err != nil {
				log.Warn("Failed to create bundle", "err", err)
				bundlesFailed.Inc()
			}
		}
	}
}

// tryCreateBundle attempts to create a bundle from pending operations.
func (s *BundlerService) tryCreateBundle() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	ops := s.pool.Pending(s.config.MaxBundleSize)
	if len(ops) == 0 {
		return nil
	}

	// Build calldata for handleOps.
	// Use zero address as beneficiary — the actual bundler address should be configured.
	calldata := s.builder.BuildCalldata(ops, types.Address{})
	if calldata == nil {
		return errors.New("failed to build bundle calldata")
	}

	gasEstimate := s.builder.EstimateGas(ops)

	gasLimit := gasEstimate.Uint64()

	// Submit bundle transaction to the txpool.
	if s.txSubmitter != nil {
		entryPoint := s.config.EntryPoints[0]
		if err := s.txSubmitter.SubmitBundleTx(entryPoint, calldata, gasLimit); err != nil {
			return err
		}
	} else {
		log.Warn("Bundle created but no tx submitter configured — dropping",
			"ops", len(ops), "calldata", len(calldata), "gasEstimate", gasLimit)
	}

	// Remove bundled operations from pool.
	for _, op := range ops {
		hash := UserOpHash(op, s.config.EntryPoints[0], s.chainID)
		s.pool.Remove(hash)
	}

	bundlesSent.Inc()
	poolSize.Set(uint64(s.pool.Count()))
	return nil
}
