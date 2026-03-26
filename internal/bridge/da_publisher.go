// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package bridge

import (
	"context"
	"fmt"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/log"
)

// DAPublisher publishes N42 state roots and header chain proofs to Ethereum
// as a data availability / settlement layer. This anchors N42 finality to
// Ethereum at regular intervals.
//
// Every `PublishInterval` blocks (~13 minutes at 100 blocks, 8s/block),
// DAPublisher:
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
	publishInterval uint64        // Blocks between publications (default: 100)
	pollInterval    time.Duration // How often to check for new blocks
	lastPublished   uint64        // Last block number published to ETH

	// ETH submission
	submitter ProofSubmitter
}

// DAPublisherConfig holds DA publisher configuration.
type DAPublisherConfig struct {
	PublishInterval uint64        `json:"publishInterval" yaml:"publishInterval"` // Blocks per publication
	PollInterval    time.Duration `json:"pollInterval" yaml:"pollInterval"`       // Poll frequency
	StartBlock      uint64        `json:"startBlock" yaml:"startBlock"`           // Block to start from
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
	return &DAPublisher{
		chain:           chain,
		headerProver:    headerProver,
		validatorSet:    vs,
		publishInterval: cfg.PublishInterval,
		pollInterval:    cfg.PollInterval,
		lastPublished:   cfg.StartBlock,
		submitter:       submitter,
	}
}

// Run starts the DA publisher main loop.
func (p *DAPublisher) Run(ctx context.Context) error {
	log.Info("DA publisher started",
		"publishInterval", p.publishInterval,
		"pollInterval", p.pollInterval,
		"startBlock", p.lastPublished,
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

// checkAndPublish checks if enough new blocks exist for a publication.
func (p *DAPublisher) checkAndPublish(ctx context.Context) error {
	current := p.chain.CurrentBlock()
	if current == nil {
		return nil
	}
	currentNum := current.Number64().Uint64()

	// Need at least publishInterval new blocks
	if currentNum < p.lastPublished+p.publishInterval {
		return nil
	}

	startBlock := p.lastPublished + 1
	endBlock := startBlock + p.publishInterval - 1
	if endBlock > currentNum {
		endBlock = currentNum
	}

	log.Info("DA publisher: generating header chain proof",
		"startBlock", startBlock,
		"endBlock", endBlock,
		"currentHead", currentNum,
	)

	// Collect headers for the range
	proof, err := p.proveRange(ctx, startBlock, endBlock)
	if err != nil {
		return fmt.Errorf("prove range %d-%d: %w", startBlock, endBlock, err)
	}

	// Submit to Ethereum
	if p.submitter != nil {
		submitStart := time.Now()
		if err := p.submitter.SubmitHeaderChainProof(ctx, proof); err != nil {
			return fmt.Errorf("submit DA proof: %w", err)
		}
		proofLatency.Observe(time.Since(submitStart).Seconds())
		headerProofsSubmitted.Inc()
	}

	p.lastPublished = endBlock
	latestVerifiedBlock.Set(float64(endBlock))

	log.Info("DA publisher: state root anchored to Ethereum",
		"startBlock", startBlock,
		"endBlock", endBlock,
		"stateRoot", proof.StateRoot,
	)

	return nil
}

// proveRange generates a header chain proof for a block range.
func (p *DAPublisher) proveRange(_ context.Context, startBlock, endBlock uint64) (*HeaderChainProof, error) {
	count := endBlock - startBlock + 1
	headers := make([]*block.Header, 0, count)
	qcs := make([]hotstuff.QuorumCertificate, 0, count)

	for num := startBlock; num <= endBlock; num++ {
		blk, err := p.chain.GetBlockByNumber(uint256.NewInt(num))
		if err != nil || blk == nil {
			return nil, fmt.Errorf("block %d not found", num)
		}
		hdr, ok := blk.Header().(*block.Header)
		if !ok {
			return nil, fmt.Errorf("block %d: invalid header type", num)
		}
		headers = append(headers, hdr)
		qcs = append(qcs, extractQCFromHeader(hdr))
	}

	// Verify locally before generating ZK proof
	if err := VerifyHeaderChainLocally(headers, qcs, p.validatorSet); err != nil {
		return nil, fmt.Errorf("local verification failed: %w", err)
	}

	lastHeader := headers[len(headers)-1]
	stateRoot := lastHeader.Root
	publicInputs := EncodePublicInputs(startBlock, endBlock, types.Hash(stateRoot))

	headerProofsGenerated.Inc()

	return &HeaderChainProof{
		StartBlock:   startBlock,
		EndBlock:     endBlock,
		StateRoot:    types.Hash(stateRoot),
		PublicInputs: publicInputs,
		ProofData:    nil, // SP1 proof will be filled by prover
	}, nil
}

// LastPublished returns the last published block number.
func (p *DAPublisher) LastPublished() uint64 {
	return p.lastPublished
}
