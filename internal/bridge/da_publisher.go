// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

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

// DAPublisher publishes N42 state roots and header chain proofs to Ethereum
// as a data availability / settlement layer. This anchors N42 finality to
// Ethereum at regular intervals.
//
// Every `PublishInterval` blocks (~13 minutes at 100 blocks, 8s/block):
//  1. Collects block headers since last publication
//  2. Verifies locally (HotStuff-2 QC chain)
//  3. Generates ZK header chain proof (SP1)
//  4. Submits proof + state root to N42Verifier on Ethereum
//
// Cost: ~300K gas per submission ≈ $3-10 depending on ETH gas price
type DAPublisher struct {
	chain        common.IBlockChain
	headerProver *HeaderChainProver
	validatorSet *hotstuff.ValidatorSet

	// Configuration
	publishInterval uint64
	pollInterval    time.Duration

	// Thread-safe: accessed from Run() goroutine and external queries.
	lastPublished atomic.Uint64

	// ETH submission
	submitter ProofSubmitter
}

// DAPublisherConfig holds DA publisher configuration.
type DAPublisherConfig struct {
	PublishInterval uint64        `json:"publishInterval" yaml:"publishInterval"`
	PollInterval    time.Duration `json:"pollInterval" yaml:"pollInterval"`
	StartBlock      uint64        `json:"startBlock" yaml:"startBlock"`
}

// DefaultDAPublisherConfig returns sensible defaults.
func DefaultDAPublisherConfig() *DAPublisherConfig {
	return &DAPublisherConfig{
		PublishInterval: 100,
		PollInterval:    30 * time.Second,
	}
}

// NewDAPublisher creates a new DA publisher.
func NewDAPublisher(
	chain common.IBlockChain,
	headerProver *HeaderChainProver,
	vs *hotstuff.ValidatorSet,
	submitter ProofSubmitter,
	cfg *DAPublisherConfig,
) *DAPublisher {
	if cfg == nil {
		cfg = DefaultDAPublisherConfig()
	}
	if cfg.PublishInterval == 0 {
		cfg.PublishInterval = 100
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 30 * time.Second
	}
	p := &DAPublisher{
		chain:           chain,
		headerProver:    headerProver,
		validatorSet:    vs,
		publishInterval: cfg.PublishInterval,
		pollInterval:    cfg.PollInterval,
		submitter:       submitter,
	}
	p.lastPublished.Store(cfg.StartBlock)
	return p
}

// Run starts the DA publisher main loop.
func (p *DAPublisher) Run(ctx context.Context) error {
	log.Info("DA publisher started",
		"publishInterval", p.publishInterval,
		"pollInterval", p.pollInterval,
		"startBlock", p.lastPublished.Load(),
	)

	ticker := time.NewTicker(p.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info("DA publisher stopped")
			return ctx.Err()
		case <-ticker.C:
			if err := p.checkAndPublish(ctx); err != nil {
				log.Warn("DA publisher cycle failed", "err", err)
			}
		}
	}
}

// LastPublished returns the last published block number (thread-safe).
func (p *DAPublisher) LastPublished() uint64 {
	return p.lastPublished.Load()
}

// checkAndPublish checks if enough new blocks exist for a publication.
func (p *DAPublisher) checkAndPublish(ctx context.Context) error {
	current := p.chain.CurrentBlock()
	if current == nil {
		return nil
	}
	currentNum := current.Number64().Uint64()
	lastPub := p.lastPublished.Load()

	if currentNum < lastPub+p.publishInterval {
		return nil
	}

	startBlock := lastPub + 1
	endBlock := startBlock + p.publishInterval - 1
	if endBlock > currentNum {
		endBlock = currentNum
	}

	log.Info("DA publisher: generating header chain proof",
		"startBlock", startBlock,
		"endBlock", endBlock,
		"currentHead", currentNum,
	)

	proof, err := ProveHeaderRange(p.chain, p.validatorSet, startBlock, endBlock)
	if err != nil {
		return fmt.Errorf("prove range %d-%d: %w", startBlock, endBlock, err)
	}

	headerProofsGenerated.Inc()

	if p.submitter != nil {
		submitStart := time.Now()
		if err := p.submitter.SubmitHeaderChainProof(ctx, proof); err != nil {
			return fmt.Errorf("submit DA proof: %w", err)
		}
		proofLatency.Observe(time.Since(submitStart).Seconds())
		headerProofsSubmitted.Inc()
	}

	daPublicationsTotal.Inc()
	p.lastPublished.Store(endBlock)
	latestVerifiedBlock.Set(float64(endBlock))

	log.Info("DA publisher: state root anchored to Ethereum",
		"startBlock", startBlock,
		"endBlock", endBlock,
		"stateRoot", proof.StateRoot,
	)

	return nil
}
