// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// BridgePublisher: the single N42->ETH finality anchor. Periodically
// batches a configurable number of blocks, runs HotStuff-2 validator
// set checks through HeaderChainProver, and pushes the resulting SP1
// ZK proof to Ethereum via the ProofSubmitter interface. Decoupled
// from concrete dispatchers and the node; interacts with the chain
// through common.IBlockChain only. Supports dry-run (nil submitter)
// and local-only (nil prover) modes for development.

package bridge

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/log"
)

// ProofSubmitter submits header chain proofs to the Ethereum verifier.
// Abstracted for testing — production implementation uses ETHSubmitter.
type ProofSubmitter interface {
	SubmitHeaderChainProof(ctx context.Context, proof *HeaderChainProof) error
}

// BridgePublisher periodically proves N42 block ranges and submits
// ZK proofs + state roots to the Ethereum settlement layer.
//
// This is the single service that anchors N42 finality to Ethereum.
//
// Flow: poll chain → batch N blocks → local verify → SP1 ZK prove → submit to ETH
//
// Decoupling:
//   - Interacts with chain via common.IBlockChain (interface)
//   - Interacts with ETH via ProofSubmitter (interface)
//   - Does not depend on ZKRouter, HyperlaneDispatcher, or node.Node
type BridgePublisher struct {
	chain        common.IBlockChain
	validatorSet *hotstuff.ValidatorSet
	prover       *HeaderChainProver // SP1 prover (nil = local-only mode)
	submitter    ProofSubmitter     // ETH submission (nil = dry-run mode)

	batchSize    uint64
	pollInterval time.Duration
	lastBlock    atomic.Uint64 // thread-safe: accessed from Run() and external queries
}

// PublisherConfig holds bridge publisher configuration.
type PublisherConfig struct {
	BatchSize    uint64        `json:"batchSize" yaml:"batchSize"`
	PollInterval time.Duration `json:"pollInterval" yaml:"pollInterval"`
	StartBlock   uint64        `json:"startBlock" yaml:"startBlock"`
}

// DefaultPublisherConfig returns sensible defaults.
func DefaultPublisherConfig() *PublisherConfig {
	return &PublisherConfig{
		BatchSize:    100,
		PollInterval: 12 * time.Second,
	}
}

// NewBridgePublisher creates a new bridge publisher.
// All dependencies are injected via interfaces for testability and decoupling.
func NewBridgePublisher(
	chain common.IBlockChain,
	vs *hotstuff.ValidatorSet,
	prover *HeaderChainProver,
	submitter ProofSubmitter,
	cfg *PublisherConfig,
) *BridgePublisher {
	if cfg == nil {
		cfg = DefaultPublisherConfig()
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 100
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 12 * time.Second
	}
	p := &BridgePublisher{
		chain:        chain,
		validatorSet: vs,
		prover:       prover,
		submitter:    submitter,
		batchSize:    cfg.BatchSize,
		pollInterval: cfg.PollInterval,
	}
	p.lastBlock.Store(cfg.StartBlock)
	return p
}

// Run starts the publisher main loop. It watches for new N42 blocks,
// batches them, generates ZK proofs, and submits to Ethereum.
func (p *BridgePublisher) Run(ctx context.Context) error {
	log.Info("Bridge publisher started",
		"batchSize", p.batchSize,
		"pollInterval", p.pollInterval,
		"startBlock", p.lastBlock.Load(),
		"sp1Enabled", p.prover != nil && p.prover.prover != nil,
		"submitterEnabled", p.submitter != nil,
	)

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("Bridge publisher stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := p.checkAndPublish(ctx); err != nil {
				log.Warn("Bridge publish cycle failed", "err", err)
			}
		}
	}
}

// LastProvenBlock returns the last proven block number (thread-safe).
func (p *BridgePublisher) LastProvenBlock() uint64 {
	return p.lastBlock.Load()
}

// checkAndPublish checks if enough new blocks exist for a proof batch.
func (p *BridgePublisher) checkAndPublish(ctx context.Context) error {
	current := p.chain.CurrentBlock()
	if current == nil {
		return nil
	}
	currentNum := current.Number64().Uint64()
	last := p.lastBlock.Load()

	if currentNum < last+p.batchSize {
		return nil
	}

	startBlock := last + 1
	endBlock := startBlock + p.batchSize - 1
	if endBlock > currentNum {
		endBlock = currentNum
	}

	// Generate proof (SP1 if available, local-only otherwise)
	var proof *HeaderChainProof
	var err error

	if p.prover != nil && p.prover.prover != nil {
		proof, err = ProveHeaderRangeWithSP1(ctx, p.chain, p.validatorSet, p.prover, startBlock, endBlock)
	} else {
		proof, err = ProveHeaderRange(p.chain, p.validatorSet, startBlock, endBlock)
	}
	if err != nil {
		return fmt.Errorf("prove range %d-%d: %w", startBlock, endBlock, err)
	}

	headerProofsGenerated.Inc()

	// Always submit — even when stateRoot is unchanged, because the Verifier
	// tracks (stateRoot, endBlock) pairs. Skipping would leave a coverage gap
	// that prevents withdrawal proofs for blocks in the skipped range.
	if p.submitter != nil {
		submitStart := time.Now()
		if err := p.submitter.SubmitHeaderChainProof(ctx, proof); err != nil {
			return fmt.Errorf("submit proof: %w", err)
		}
		proofLatency.Observe(time.Since(submitStart).Seconds())
		headerProofsSubmitted.Inc()
	}

	p.lastBlock.Store(endBlock)
	latestVerifiedBlock.Set(float64(endBlock))

	log.Info("Bridge proof published to Ethereum",
		"startBlock", startBlock,
		"endBlock", endBlock,
		"stateRoot", proof.StateRoot,
		"hasZKProof", proof.ProofData != nil,
	)

	return nil
}
